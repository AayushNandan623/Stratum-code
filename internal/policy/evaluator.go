package policy

import (
	"context"
	"fmt"
	"time"

	"github.com/open-policy-agent/opa/v1/ast"
	"github.com/open-policy-agent/opa/v1/rego"

	"github.com/google/uuid"
)

// OPAEvaluator evaluates Rego policies against an EvaluationInput using OPA's
// embedded, in-process engine.
type OPAEvaluator struct {
	loader *BundleLoader
}

func NewOPAEvaluator(loader *BundleLoader) *OPAEvaluator {
	return &OPAEvaluator{loader: loader}
}

// Evaluate retrieves the policies for the given scope, compiles each one, and
// evaluates them. It returns an aggregated EvaluationResult.
func (e *OPAEvaluator) Evaluate(ctx context.Context, input EvaluationInput) (*EvaluationResult, error) {
	start := time.Now()

	policies := e.loader.GetPoliciesForScope(input.OrgID, input.SpaceID, input.StackID)
	if len(policies) == 0 {
		return &EvaluationResult{Allow: true, Severity: SeverityPass}, nil
	}

	opaInput := BuildInputDocument(input)
	var violations []PolicyViolation
	hardFail := false
	var evaluatedIDs []uuid.UUID

	for _, pol := range policies {
		if !pol.Enabled {
			continue
		}
		evaluatedIDs = append(evaluatedIDs, pol.ID)

		denials, err := e.evaluateSingle(ctx, pol, opaInput)
		if err != nil {
			return nil, fmt.Errorf("policy %q eval error: %w", pol.Name, err)
		}
		for _, msg := range denials {
			violations = append(violations, PolicyViolation{
				PolicyID:   pol.ID,
				PolicyName: pol.Name,
				Message:    msg,
			})
			if pol.Enforcement == EnforcementHardFail {
				hardFail = true
			}
		}
	}

	result := &EvaluationResult{
		Violations: violations,
		PolicyIDs:  evaluatedIDs,
		DurationMs: time.Since(start).Milliseconds(),
	}
	if hardFail {
		result.Allow = false
		result.Severity = SeverityHardFail
	} else if len(violations) > 0 {
		result.Allow = true   // SOFT_WARN allows the run
		result.Severity = SeveritySoftWarn
	} else {
		result.Allow = true
		result.Severity = SeverityPass
	}
	return result, nil
}

// evaluateSingle evaluates a single policy against the input and returns any
// denial messages.
func (e *OPAEvaluator) evaluateSingle(ctx context.Context, pol *Policy, opaInput any) ([]string, error) {
	compiler := ast.MustCompileModules(map[string]string{
		pol.Name + ".rego": pol.RegoSource,
	})

	r := rego.New(
		rego.Query("data.stratum.policy"),
		rego.Compiler(compiler),
		rego.Input(opaInput),
	)
	rs, err := r.Eval(ctx)
	if err != nil {
		return nil, err
	}

	return extractDenials(rs), nil
}

// extractDenials extracts denial messages from the OPA result set.
// It looks for the "deny" key in the result value, which should be an array
// of strings (or objects with a "message" field).
func extractDenials(rs rego.ResultSet) []string {
	if len(rs) == 0 {
		return nil
	}

	var msgs []string
	for _, r := range rs {
		for _, expr := range r.Expressions {
			if expr.Value == nil {
				continue
			}
			// The value for data.stratum.policy is a map[string]any
			val, ok := expr.Value.(map[string]any)
			if !ok {
				continue
			}
			// Check for deny rules
			denyVal, hasDeny := val["deny"]
			if !hasDeny {
				continue
			}
			msgs = append(msgs, extractStrings(denyVal)...)
		}
	}
	return msgs
}

// extractStrings extracts string messages from an OPA value that may be a
// []any of strings, a []any of maps with a "message" key, or similar.
func extractStrings(v any) []string {
	switch val := v.(type) {
	case []any:
		var out []string
		for _, item := range val {
			switch it := item.(type) {
			case string:
				out = append(out, it)
			case map[string]any:
				if msg, ok := it["message"].(string); ok {
					out = append(out, msg)
				}
			}
		}
		return out
	case []string:
		return val
	default:
		return nil
	}
}

// BuildInputDocument constructs the OPA input document from an EvaluationInput.
func BuildInputDocument(input EvaluationInput) map[string]any {
	doc := map[string]any{
		"run": map[string]any{
			"id":   input.RunID.String(),
			"type": input.RunType,
		},
		"stack": map[string]any{
			"name":   input.Stack.Name,
			"labels": input.Stack.Labels,
			"space":  input.Stack.Space,
		},
		"actor": map[string]any{
			"id":    input.Actor.ID.String(),
			"type":  input.Actor.Type,
			"roles": input.Actor.Roles,
		},
		"organization": map[string]any{
			"id": input.OrgID.String(),
		},
	}

	if input.PlanOutput != nil {
		doc["plan"] = buildPlanDocument(input.PlanOutput)
	}

	return doc
}

func buildPlanDocument(plan *PlanContext) map[string]any {
	changes := make([]map[string]any, len(plan.ResourceChanges))
	for i, rc := range plan.ResourceChanges {
		c := map[string]any{
			"address": rc.Address,
			"type":    rc.Type,
			"actions": rc.Actions,
		}
		if rc.After != nil {
			c["after"] = rc.After
		}
		if rc.Before != nil {
			c["before"] = rc.Before
		}
		changes[i] = c
	}
	return map[string]any{
		"resource_changes": changes,
		"total_added":      plan.TotalAdded,
		"total_changed":    plan.TotalChanged,
		"total_removed":    plan.TotalRemoved,
	}
}
