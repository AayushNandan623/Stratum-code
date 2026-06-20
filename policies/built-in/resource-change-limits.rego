# Built-in Policy: Resource Change Limits
# Prevents accidentally large infrastructure changes by limiting
# the number of resources that can be created, modified, or destroyed
# in a single run.
#
# Configurable via stack labels:
#   max_resources_added    (default: 50)
#   max_resources_changed  (default: 25)
#   max_resources_removed  (default: 10)
#
# Severity: HARD_FAIL — applies to ALL runs by default.
# Override per-stack by adjusting labels or disabling this policy.

package stratum.policy

import future.keywords.if

# ─── Limits ───────────────────────────────────────────────────────────────────

max_added := limit if {
    limit := to_number(input.stack.labels["max_resources_added"])
} else := 50

max_changed := limit if {
    limit := to_number(input.stack.labels["max_resources_changed"])
} else := 25

max_removed := limit if {
    limit := to_number(input.stack.labels["max_resources_removed"])
} else := 10

# ─── Counts ───────────────────────────────────────────────────────────────────

added_count := count([r |
    r := input.plan.resource_changes[_]
    r.actions[_] == "create"
])

changed_count := count([r |
    r := input.plan.resource_changes[_]
    r.actions[_] == "update"
])

removed_count := count([r |
    r := input.plan.resource_changes[_]
    r.actions[_] == "delete"
])

# ─── Rules ────────────────────────────────────────────────────────────────────

deny[msg] if {
    added_count > max_added
    msg := sprintf(
        "This run creates %d resources, exceeding the limit of %d. Review the change scope or increase max_resources_added label.",
        [added_count, max_added]
    )
}

deny[msg] if {
    changed_count > max_changed
    msg := sprintf(
        "This run modifies %d resources, exceeding the limit of %d. Review the change scope or increase max_resources_changed label.",
        [changed_count, max_changed]
    )
}

deny[msg] if {
    removed_count > max_removed
    msg := sprintf(
        "This run destroys %d resources, exceeding the limit of %d. Review the change scope or increase max_resources_removed label.",
        [removed_count, max_removed]
    )
}

# ─── Extra: Destroy protection for production ─────────────────────────────────

deny[msg] if {
    input.stack.labels["env"] == "production"
    input.run.type == "destroy"
    not input.stack.labels["allow_destroy"] == "true"
    msg := "Destroy runs on production stacks are blocked. Set label 'allow_destroy=true' to override."
}
