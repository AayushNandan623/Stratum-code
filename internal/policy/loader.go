package policy

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BundleLoader caches policies in memory and provides hot-reload via a channel
// notification. It periodically does a full refresh as a safety net.
type BundleLoader struct {
	mu       sync.RWMutex
	policies map[uuid.UUID]*Policy   // keyed by policy ID
	byScope  map[string][]*Policy    // "org:{id}" | "space:{id}" | "stack:{id}"
	repo     *Repository
	pool     *pgxpool.Pool
	updateCh chan uuid.UUID
	logger   *slog.Logger
}

// NewBundleLoader creates a BundleLoader.
func NewBundleLoader(repo *Repository, pool *pgxpool.Pool, logger *slog.Logger) *BundleLoader {
	return &BundleLoader{
		policies:  make(map[uuid.UUID]*Policy),
		byScope:   make(map[string][]*Policy),
		repo:      repo,
		pool:      pool,
		updateCh:  make(chan uuid.UUID, 100),
		logger:    logger,
	}
}

// Start begins the hot-reload loop. It does an initial full load, then listens
// for update notifications and performs a safety-net full refresh every 5 min.
func (l *BundleLoader) Start(ctx context.Context) {
	if err := l.reloadAll(ctx); err != nil {
		l.logger.Error("bundle loader initial reload failed", "error", err)
	}

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case policyID := <-l.updateCh:
			l.logger.Info("bundle loader hot-reload triggered", "policy_id", policyID)
			if err := l.reloadAll(ctx); err != nil {
				l.logger.Error("bundle loader hot-reload failed", "policy_id", policyID, "error", err)
			}
		case <-ticker.C:
			l.logger.Debug("bundle loader safety-net full refresh")
			if err := l.reloadAll(ctx); err != nil {
				l.logger.Error("bundle loader safety-net refresh failed", "error", err)
			}
		case <-ctx.Done():
			l.logger.Info("bundle loader stopped")
			return
		}
	}
}

// NotifyUpdate is called by the service after a policy is created, updated, or
// deleted so the loader can hot-reload it.
func (l *BundleLoader) NotifyUpdate(policyID uuid.UUID) {
	select {
	case l.updateCh <- policyID:
	default:
		l.logger.Warn("bundle loader update channel full, dropping notification", "policy_id", policyID)
	}
}

// GetPoliciesForScope returns all enabled policies that apply to the given
// scope: org-level, space-level (if spaceID non-nil), and stack-level.
// Duplicates are deduplicated by policy ID.
func (l *BundleLoader) GetPoliciesForScope(orgID uuid.UUID, spaceID *uuid.UUID, stackID uuid.UUID) []*Policy {
	l.mu.RLock()
	defer l.mu.RUnlock()

	seen := make(map[uuid.UUID]bool)
	var result []*Policy

	for _, p := range l.byScope[fmt.Sprintf("org:%s", orgID)] {
		if !seen[p.ID] {
			result = append(result, p)
			seen[p.ID] = true
		}
	}

	if spaceID != nil {
		for _, p := range l.byScope[fmt.Sprintf("space:%s", *spaceID)] {
			if !seen[p.ID] {
				result = append(result, p)
				seen[p.ID] = true
			}
		}
	}

	for _, p := range l.byScope[fmt.Sprintf("stack:%s", stackID)] {
		if !seen[p.ID] {
			result = append(result, p)
			seen[p.ID] = true
		}
	}

	return result
}

// reloadAll performs a full refresh of all policies and their bindings from the DB.
func (l *BundleLoader) reloadAll(ctx context.Context) error {
	// Load all non-deleted policies.
	policies, err := l.loadAllPolicies(ctx)
	if err != nil {
		return fmt.Errorf("load all policies: %w", err)
	}

	// Load all bindings (across all orgs).
	bindings, err := l.loadAllBindings(ctx)
	if err != nil {
		return fmt.Errorf("load all bindings: %w", err)
	}

	// Build a set ID → policy mapping from policies.
	setToPolicies := make(map[uuid.UUID][]*Policy)
	policyByID := make(map[uuid.UUID]*Policy, len(policies))
	for _, p := range policies {
		policyByID[p.ID] = p
	}

	// For each binding, resolve the set members and collect policies.
	// We need set members. Load them all.
	setMembers, err := l.loadAllSetMembers(ctx)
	if err != nil {
		return fmt.Errorf("load all set members: %w", err)
	}
	for _, sm := range setMembers {
		if p, ok := policyByID[sm.PolicyID]; ok && p.Enabled {
			setToPolicies[sm.PolicySetID] = append(setToPolicies[sm.PolicySetID], p)
		}
	}

	// Build byScope index.
	byScope := make(map[string][]*Policy)
	seen := make(map[string]map[uuid.UUID]bool) // scope key → set of policy IDs
	for _, b := range bindings {
		key := fmt.Sprintf("%s:%s", strings.ToLower(b.ResourceType), b.ResourceID.String())
		if seen[key] == nil {
			seen[key] = make(map[uuid.UUID]bool)
		}
		for _, p := range setToPolicies[b.PolicySetID] {
			if !seen[key][p.ID] {
				byScope[key] = append(byScope[key], p)
				seen[key][p.ID] = true
			}
		}
	}

	l.mu.Lock()
	l.policies = policyByID
	l.byScope = byScope
	l.mu.Unlock()

	l.logger.Info("bundle loader full refresh complete",
		"policies", len(policies),
		"bindings", len(bindings),
		"scope_keys", len(byScope))
	return nil
}

func (l *BundleLoader) loadAllPolicies(ctx context.Context) ([]*Policy, error) {
	const sql = `SELECT ` + policyColumnsWithEnforcement + ` FROM policies WHERE deleted_at IS NULL`
	rows, err := l.pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPolicies(rows)
}

func (l *BundleLoader) loadAllBindings(ctx context.Context) ([]*PolicySetBinding, error) {
	const sql = `SELECT ` + policySetBindingColumns + ` FROM policy_set_bindings`
	rows, err := l.pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPolicySetBindings(rows)
}

func (l *BundleLoader) loadAllSetMembers(ctx context.Context) ([]PolicySetMember, error) {
	const sql = `SELECT policy_set_id, policy_id FROM policy_set_members`
	rows, err := l.pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PolicySetMember
	for rows.Next() {
		m := PolicySetMember{}
		if err := rows.Scan(&m.PolicySetID, &m.PolicyID); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
