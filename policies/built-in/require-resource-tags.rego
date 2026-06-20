# Built-in Policy: Require Resource Tags
# Enforces that all taggable resources have required tags applied.
# Severity: SOFT_WARN by default — can be changed to HARD_FAIL per org.
#
# Required tags are configured by setting stack variables:
#   STRATUM_REQUIRED_TAGS = "env,team,cost-center"
#   (parsed from input.stack.labels["required_tags"] or defaults)

package stratum.policy

import future.keywords.if
import future.keywords.in

# Resources that support tagging (extend this list as needed)
taggable_resource_types := {
    "aws_instance",
    "aws_s3_bucket",
    "aws_rds_cluster",
    "aws_rds_instance",
    "aws_eks_cluster",
    "aws_vpc",
    "aws_subnet",
    "aws_security_group",
    "aws_lambda_function",
    "aws_elasticache_cluster",
    "google_compute_instance",
    "google_container_cluster",
    "google_sql_database_instance",
    "azurerm_virtual_machine",
    "azurerm_resource_group",
}

# Default required tags — override via stack label "required_tags" (comma-separated)
default_required_tags := ["env", "team"]

required_tags := tags if {
    label_val := input.stack.labels["required_tags"]
    tags := split(label_val, ",")
} else := default_required_tags

# Warn when a taggable resource is created/updated without required tags
warn[msg] if {
    change := input.plan.resource_changes[_]
    change.type in taggable_resource_types
    change.actions[_] in ["create", "update"]
    tag := required_tags[_]
    not change.after.tags[tag]
    msg := sprintf(
        "Resource '%s' (%s) is missing required tag '%s'.",
        [change.address, change.type, tag]
    )
}

# Also deny (HARD_FAIL) if stack is tagged as production and missing env tag
deny[msg] if {
    input.stack.labels["env"] == "production"
    change := input.plan.resource_changes[_]
    change.type in taggable_resource_types
    change.actions[_] in ["create"]
    not change.after.tags["cost-center"]
    msg := sprintf(
        "Production resource '%s' must have a 'cost-center' tag.",
        [change.address]
    )
}
