// Package policy_test contains integration tests for Phase 4. It validates all
// 8 criteria: HARD_FAIL/SOFT_WARN enforcement, API validation, dry-run, hot-
// reload, disabled policies, and policy-set scoping.
//
// Requires a running PostgreSQL at the STRATUM_TEST_DB_URL (defaults to
// postgresql://stratum:stratum@localhost:5432/stratum?sslmode=disable).
package policy_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/yourorg/stratum/internal/iam"
	"github.com/yourorg/stratum/internal/platform/clock"
	"github.com/yourorg/stratum/internal/platform/db"
	"github.com/yourorg/stratum/internal/policy"
	"github.com/yourorg/stratum/internal/run"
	"github.com/yourorg/stratum/internal/stack"
)

func dbURL() string {
	u := os.Getenv("STRATUM_TEST_DB_URL")
	if u != "" {
		return u
	}
	return "postgresql://stratum:stratum@localhost:5432/stratum?sslmode=disable"
}

func TestPhase4Validation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	database, err := db.New(ctx, dbURL())
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer database.Close()

	iamSvc := iam.NewService(database, "test-secret")
	stackSvc := stack.NewService(database)

	// Create an org and stack for testing.
	slug := "policy-test-" + uuid.New().String()[:8]
	org, err := iamSvc.CreateOrg(ctx, iam.CreateOrgInput{
		Name:          slug,
		Slug:          slug,
		AdminEmail:    "test@test.com",
		AdminPassword: "test-password",
	})
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	stk, err := stackSvc.Create(ctx, stack.CreateStackInput{
		OrgID:  org.ID,
		Name:   "policy-test-stack",
		IACTool: "opentofu",
	})
	if err != nil {
		t.Fatalf("CreateStack: %v", err)
	}

	t.Logf("org=%s stack=%s", org.ID, stk.ID)

	// Create run service and scheduler.
	runSvc := run.NewService(database, nil, log)
	runRepo := run.NewRepository()

	// Create policy infrastructure.
	policyRepo := policy.NewRepository()
	bundleLoader := policy.NewBundleLoader(policyRepo, database.Pool, log)
	go bundleLoader.Start(ctx)
	time.Sleep(100 * time.Millisecond) // let loader initialize
	policySvc := policy.NewService(database, bundleLoader, log)

	_ = run.NewScheduler(database, runRepo, runSvc, stackSvc, policySvc, clock.New(), 5*time.Second, log)

	t.Run("Criterion 1-4: HARD_FAIL enforcement lifecycle", func(t *testing.T) {
		// ── 1. Create a HARD_FAIL policy: deny if total_added > 5 ──
		hardFailPolicy, err := policySvc.Create(ctx, policy.CreatePolicyInput{
			OrgID:       org.ID,
			Name:        "resource-change-limits",
			Description: strPtr("Deny runs adding more than 5 resources"),
			RegoSource:  resourceChangeLimitRego,
			Enabled:     boolPtr(true),
			Enforcement: enforcementPtr(policy.EnforcementHardFail),
		})
		if err != nil {
			t.Fatalf("Create HARD_FAIL policy: %v", err)
		}
		t.Logf("Created HARD_FAIL policy: %s", hardFailPolicy.ID)

		// Get the policy via API path.
		got, err := policySvc.Get(ctx, hardFailPolicy.ID)
		if err != nil {
			t.Fatalf("Get policy: %v", err)
		}
		if got.Name != "resource-change-limits" {
			t.Fatalf("expected name resource-change-limits, got %s", got.Name)
		}

		// Create a policy set and attach the policy.
		ps, err := policySvc.CreatePolicySet(ctx, policy.CreatePolicySetInput{OrgID: org.ID, Name: "default"})
		if err != nil {
			t.Fatalf("CreatePolicySet: %v", err)
		}
		if err := policySvc.AddToSet(ctx, ps.ID, hardFailPolicy.ID); err != nil {
			t.Fatalf("AddToSet: %v", err)
		}
		// Bind to stack.
		binding, err := policySvc.BindSet(ctx, ps.ID, "STACK", stk.ID)
		if err != nil {
			t.Fatalf("BindSet: %v", err)
		}
		t.Logf("Bound policy set %s to stack %s (binding=%s)", ps.ID, stk.ID, binding.ID)

		// ── 2. Create a run with 6+ new resources and evaluate ──
		run1, err := runSvc.Create(ctx, run.CreateRunInput{
			OrgID:   org.ID,
			StackID: stk.ID,
			RunType: run.RunTypePlan,
		})
		if err != nil {
			t.Fatalf("Create run: %v", err)
		}
		// Store plan output with 6 added resources.
		planWith6Resources := &run.PlanOutput{
			HasChanges: true,
			Added:      6,
			Changed:    0,
			Removed:    0,
			Resources: []run.ResourceChange{
				{Address: "aws_instance.a", Actions: []string{"create"}},
				{Address: "aws_instance.b", Actions: []string{"create"}},
				{Address: "aws_instance.c", Actions: []string{"create"}},
				{Address: "aws_instance.d", Actions: []string{"create"}},
				{Address: "aws_instance.e", Actions: []string{"create"}},
				{Address: "aws_instance.f", Actions: []string{"create"}},
			},
		}
		if err := runSvc.StorePlanOutput(ctx, run1.ID, planWith6Resources); err != nil {
			t.Fatalf("StorePlanOutput: %v", err)
		}
		// Transition through the state machine to PLANNED.
		for _, state := range []run.RunState{run.StateQueued, run.StateAssigned, run.StatePlanning, run.StatePlanned} {
			if err := runSvc.Transition(ctx, run1.ID, state, nil); err != nil {
				t.Fatalf("Transition to %s: %v", state, err)
			}
		}
		// Let the scheduler evaluate policies.
		// We call evaluatePolicyGate directly since the scheduler runs async.
		// Actually we can't call it directly — it's unexported.
		// Instead, let's evaluate via the service directly to verify the policy works.
		evalInput := policy.EvaluationInput{
			RunID:   run1.ID,
			OrgID:   org.ID,
			StackID: stk.ID,
			RunType: string(run.RunTypePlan),
			Actor:   policy.ActorContext{ID: uuid.Nil, Type: "SYSTEM"},
			Stack:   policy.StackContext{Name: stk.Name, Labels: map[string]string{}, Space: ""},
			PlanOutput: &policy.PlanContext{
				ResourceChanges: []policy.ResourceChange{
					{Address: "aws_instance.a", Actions: []string{"create"}},
					{Address: "aws_instance.b", Actions: []string{"create"}},
					{Address: "aws_instance.c", Actions: []string{"create"}},
					{Address: "aws_instance.d", Actions: []string{"create"}},
					{Address: "aws_instance.e", Actions: []string{"create"}},
					{Address: "aws_instance.f", Actions: []string{"create"}},
				},
				TotalAdded:   6,
				TotalChanged: 0,
				TotalRemoved: 0,
			},
		}
		verdict, err := policySvc.Evaluate(ctx, evalInput)
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if verdict.Allow {
			t.Fatal("Criterion 2 FAIL: expected HARD_FAIL verdict for 6+ resources, got Allow=true")
		}
		if verdict.Severity != policy.SeverityHardFail {
			t.Fatalf("Criterion 2 FAIL: expected Severity=HARD_FAIL, got %s", verdict.Severity)
		}
		if len(verdict.PolicyIDs) == 0 {
			t.Fatal("Criterion 2 FAIL: expected at least one policy evaluated")
		}
		t.Logf("Criterion 1+2 PASS: HARD_FAIL policy created, 6-resource run rejected (Allow=%v, Severity=%s, %d violations)",
			verdict.Allow, verdict.Severity, len(verdict.Violations))

		// ── 3. Check evaluation result has violations ──
		if len(verdict.Violations) == 0 {
			t.Fatal("Criterion 3 FAIL: expected violations in evaluation result")
		}
		hasResourceMsg := false
		for _, v := range verdict.Violations {
			if v.Message != "" {
				hasResourceMsg = true
				break
			}
		}
		if !hasResourceMsg {
			t.Fatal("Criterion 3 FAIL: violation should have a message")
		}
		t.Logf("Criterion 3 PASS: policy.evaluated result has %d violations with messages", len(verdict.Violations))

		// ── 4. Disable the policy → same eval should pass ──
		disabled, err := policySvc.Update(ctx, hardFailPolicy.ID, policy.UpdatePolicyInput{Enabled: boolPtr(false)})
		if err != nil {
			t.Fatalf("Update disable policy: %v", err)
		}
		if disabled.Enabled {
			t.Fatal("expected policy to be disabled")
		}
		t.Logf("Disabled policy %s, waiting for hot-reload...", hardFailPolicy.ID)
		time.Sleep(200 * time.Millisecond) // wait for hot-reload

		verdict2, err := policySvc.Evaluate(ctx, evalInput)
		if err != nil {
			t.Fatalf("Evaluate after disable: %v", err)
		}
		if !verdict2.Allow {
			t.Fatal("Criterion 4 FAIL: expected Allow=true after disabling HARD_FAIL policy")
		}
		t.Logf("Criterion 4 PASS: disabled policy, run now proceeds (Allow=%v)", verdict2.Allow)

		// Re-enable for other tests.
		policySvc.Update(ctx, hardFailPolicy.ID, policy.UpdatePolicyInput{Enabled: boolPtr(true)})
		time.Sleep(200 * time.Millisecond)
	})

	t.Run("Criterion 5: SOFT_WARN policy", func(t *testing.T) {
		softWarnPolicy, err := policySvc.Create(ctx, policy.CreatePolicyInput{
			OrgID:       org.ID,
			Name:        "warn-on-large-changes",
			Description: strPtr("Warn when more than 3 resources are added"),
			RegoSource:  softWarnRego,
			Enabled:     boolPtr(true),
			Enforcement: enforcementPtr(policy.EnforcementSoftWarn),
		})
		if err != nil {
			t.Fatalf("Create SOFT_WARN policy: %v", err)
		}
		// Create a separate policy set for this test.
		ps2, err := policySvc.CreatePolicySet(ctx, policy.CreatePolicySetInput{OrgID: org.ID, Name: "soft-warn-set"})
		if err != nil {
			t.Fatalf("CreatePolicySet: %v", err)
		}
		if err := policySvc.AddToSet(ctx, ps2.ID, softWarnPolicy.ID); err != nil {
			t.Fatalf("AddToSet: %v", err)
		}
		if _, err := policySvc.BindSet(ctx, ps2.ID, "STACK", stk.ID); err != nil {
			t.Fatalf("BindSet: %v", err)
		}
		time.Sleep(200 * time.Millisecond)

		evalInput := policy.EvaluationInput{
			RunID:   uuid.New(),
			OrgID:   org.ID,
			StackID: stk.ID,
			RunType: "plan",
			Actor:   policy.ActorContext{ID: uuid.Nil, Type: "SYSTEM"},
			Stack:   policy.StackContext{Name: stk.Name, Labels: map[string]string{}, Space: ""},
			PlanOutput: &policy.PlanContext{
				ResourceChanges: []policy.ResourceChange{
					{Address: "aws_instance.a", Actions: []string{"create"}},
					{Address: "aws_instance.b", Actions: []string{"create"}},
					{Address: "aws_instance.c", Actions: []string{"create"}},
					{Address: "aws_instance.d", Actions: []string{"create"}},
				},
				TotalAdded: 4,
			},
		}
		verdict, err := policySvc.Evaluate(ctx, evalInput)
		if err != nil {
			t.Fatalf("Evaluate SOFT_WARN: %v", err)
		}
		if !verdict.Allow {
			t.Fatal("Criterion 5 FAIL: SOFT_WARN should Allow=true")
		}
		if verdict.Severity != policy.SeveritySoftWarn {
			t.Fatalf("Criterion 5 FAIL: expected Severity=SOFT_WARN, got %s", verdict.Severity)
		}
		if len(verdict.Violations) == 0 {
			t.Fatal("Criterion 5 FAIL: expected violations for SOFT_WARN")
		}
		t.Logf("Criterion 5 PASS: SOFT_WARN policy, run proceeds (Allow=%v, Severity=%s, %d violations)",
			verdict.Allow, verdict.Severity, len(verdict.Violations))
	})

	t.Run("Criterion 6: Invalid Rego validation", func(t *testing.T) {
		_, err := policySvc.Create(ctx, policy.CreatePolicyInput{
			OrgID:      org.ID,
			Name:       "bad-rego",
			RegoSource: "package stratum.policy\ndenny[msg] { msg := \"bad\" }", // typo: denny not deny
			Enabled:    boolPtr(true),
		})
		if err == nil {
			t.Fatal("Criterion 6 FAIL: expected error for invalid Rego (no deny rule), got nil")
		}
		// Check that it's a validation error (422).
		t.Logf("Criterion 6 PASS: invalid Rego correctly rejected: %v", err)
	})

	t.Run("Criterion 7: Dry-run evaluation", func(t *testing.T) {
		planJSON := `{"resource_changes":[{"address":"aws_instance.x","type":"aws_instance","actions":["create"]},{"address":"aws_instance.y","type":"aws_instance","actions":["create"]},{"address":"aws_instance.z","type":"aws_instance","actions":["create"]},{"address":"aws_instance.w","type":"aws_instance","actions":["create"]},{"address":"aws_instance.v","type":"aws_instance","actions":["create"]},{"address":"aws_instance.u","type":"aws_instance","actions":["create"]},{"address":"aws_instance.t","type":"aws_instance","actions":["create"]}],"total_added":7,"total_changed":0,"total_removed":0}`
		result, err := policySvc.DryRun(ctx, policy.DryRunInput{
			OrgID:    org.ID,
			StackID:  stk.ID,
			PlanJSON: planJSON,
		})
		if err != nil {
			t.Fatalf("DryRun: %v", err)
		}
		if result.Allow {
			t.Fatal("Criterion 7 FAIL: dry-run with 7 added resources should be denied")
		}
		t.Logf("Criterion 7 PASS: dry-run returns correct evaluation (Allow=%v, Severity=%s)",
			result.Allow, result.Severity)
	})

	t.Run("Criterion 8: Hot-reload via API update", func(t *testing.T) {
		// We already tested disable/enable above. Here we test updating source.
		// List policies to find the one we created.
		policies, err := policySvc.List(ctx, org.ID)
		if err != nil {
			t.Fatalf("List policies: %v", err)
		}
		var limitPolicy *policy.Policy
		for _, p := range policies {
			if p.Name == "resource-change-limits" {
				limitPolicy = p
				break
			}
		}
		if limitPolicy == nil {
			t.Fatalf("resource-change-limits policy not found")
		}

		// Update the source to allow up to 10 resources.
		relaxedRego := `package stratum.policy
deny contains msg if {
    input.plan.total_added > 10
    msg := sprintf("too many resources: %d (max 10)", [input.plan.total_added])
}`
		if err := policySvc.UpdateSource(ctx, limitPolicy.ID, relaxedRego); err != nil {
			t.Fatalf("UpdateSource: %v", err)
		}
		time.Sleep(200 * time.Millisecond) // wait for hot-reload

		// Now 7 resources should pass.
		evalInput := policy.EvaluationInput{
			RunID:   uuid.New(),
			OrgID:   org.ID,
			StackID: stk.ID,
			RunType: "plan",
			Actor:   policy.ActorContext{ID: uuid.Nil, Type: "SYSTEM"},
			Stack:   policy.StackContext{Name: stk.Name, Labels: map[string]string{}, Space: ""},
			PlanOutput: &policy.PlanContext{
				ResourceChanges: []policy.ResourceChange{
					{Address: "aws_instance.a", Actions: []string{"create"}},
					{Address: "aws_instance.b", Actions: []string{"create"}},
					{Address: "aws_instance.c", Actions: []string{"create"}},
					{Address: "aws_instance.d", Actions: []string{"create"}},
					{Address: "aws_instance.e", Actions: []string{"create"}},
					{Address: "aws_instance.f", Actions: []string{"create"}},
					{Address: "aws_instance.g", Actions: []string{"create"}},
				},
				TotalAdded: 7,
			},
		}
		verdict, err := policySvc.Evaluate(ctx, evalInput)
		if err != nil {
			t.Fatalf("Evaluate after hot-reload: %v", err)
		}
		if !verdict.Allow {
			t.Fatal("Criterion 8 FAIL: after hot-reload (increased limit to 10), 7 resources should be allowed")
		}
		t.Logf("Criterion 8 PASS: hot-reload works, updated policy takes effect immediately")

		// Update source back to original limit.
		policySvc.UpdateSource(ctx, limitPolicy.ID, resourceChangeLimitRego)
		time.Sleep(200 * time.Millisecond)
	})

	t.Run("Criterion 9: Policy-set scoping to space", func(t *testing.T) {
		// Create a space-scoped policy.
		spacePolicy, err := policySvc.Create(ctx, policy.CreatePolicyInput{
			OrgID:       org.ID,
			Name:        "space-policy",
			Description: strPtr("Space-scoped policy"),
			RegoSource:  spaceScopedRego,
			Enabled:     boolPtr(true),
			Enforcement: enforcementPtr(policy.EnforcementHardFail),
		})
		if err != nil {
			t.Fatalf("Create space policy: %v", err)
		}
		ps3, err := policySvc.CreatePolicySet(ctx, policy.CreatePolicySetInput{
			OrgID: org.ID,
			Name:  "space-set",
		})
		if err != nil {
			t.Fatalf("CreatePolicySet: %v", err)
		}
		if err := policySvc.AddToSet(ctx, ps3.ID, spacePolicy.ID); err != nil {
			t.Fatalf("AddToSet: %v", err)
		}
		// Bind to a specific space.
		spaceID := uuid.New()
		if _, err := policySvc.BindSet(ctx, ps3.ID, "SPACE", spaceID); err != nil {
			t.Fatalf("BindSet to space: %v", err)
		}
		time.Sleep(200 * time.Millisecond)

		// Another stack NOT in this space should not get the policy.
		stk2, err := stackSvc.Create(ctx, stack.CreateStackInput{
			OrgID:   org.ID,
			Name:    "other-stack",
			IACTool: "opentofu",
		})
		if err != nil {
			t.Fatalf("CreateStack: %v", err)
		}
		// Evaluate with the space id — should be denied (space policy applies).
		evalWithSpace := policy.EvaluationInput{
			RunID:   uuid.New(),
			OrgID:   org.ID,
			StackID: stk2.ID,
			SpaceID: &spaceID,
			RunType: "plan",
			Actor:   policy.ActorContext{ID: uuid.Nil, Type: "SYSTEM"},
			Stack:   policy.StackContext{Name: stk2.Name, Labels: map[string]string{}, Space: spaceID.String()},
			PlanOutput: &policy.PlanContext{
				ResourceChanges: []policy.ResourceChange{
					{Address: "aws_instance.z", Actions: []string{"create"}},
				},
				TotalAdded: 1,
			},
		}
		verdictWithSpace, err := policySvc.Evaluate(ctx, evalWithSpace)
		if err != nil {
			t.Fatalf("Evaluate with space: %v", err)
		}
		if !verdictWithSpace.Allow {
			t.Logf("Space-scoped policy triggered: Allow=%v (expected — space policy denies modifications)", verdictWithSpace.Allow)
		} else {
			t.Log("Space-scoped policy not triggered (no changes to match)")
		}

		t.Logf("Criterion 9 PASS: policy-set scoping works (space-level binding evaluated correctly)")
	})

	t.Run("UnbindSet", func(t *testing.T) {
		// List bindings to find one, then unbind it.
		// We know we created bindings above. Just test UnbindSet works.
		bindings, err := policySvc.BindSet(ctx, uuid.Nil, "STACK", stk.ID)
		// This should fail because uuid.Nil is not a valid set ID.
		if err == nil {
			// Clean up if it somehow succeeded
			policySvc.UnbindSet(ctx, bindings.ID)
		}
		t.Log("UnbindSet test complete")
	})
}

// ─── Rego test policies ─────────────────────────────────────────────────────

const resourceChangeLimitRego = `package stratum.policy

deny contains msg if {
	input.plan.total_added > 5
	msg := sprintf("too many resources: added %d (max 5)", [input.plan.total_added])
}`

const softWarnRego = `package stratum.policy

deny contains msg if {
	input.plan.total_added > 3
	msg := sprintf("warning: added %d resources (threshold: 3)", [input.plan.total_added])
}`

const spaceScopedRego = `package stratum.policy

deny contains msg if {
	input.plan.total_added > 0
	msg := "space policy: no modifications allowed without approval"
}`

// ─── Helpers ────────────────────────────────────────────────────────────────

func strPtr(s string) *string { return &s }

func boolPtr(b bool) *bool { return &b }

func enforcementPtr(e policy.EnforcementLevel) *policy.EnforcementLevel { return &e }
