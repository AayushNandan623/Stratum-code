# Built-in Policy: No Public Storage
# Enforces that S3 buckets and GCS buckets are not publicly accessible.
# Severity: HARD_FAIL (apply is blocked if violated)
#
# Usage: Attach to any org or space that manages storage resources.

package stratum.policy

import future.keywords.if
import future.keywords.in

# ─── AWS S3 ───────────────────────────────────────────────────────────────────

deny[msg] if {
    change := input.plan.resource_changes[_]
    change.type == "aws_s3_bucket"
    change.actions[_] in ["create", "update"]
    change.after.acl in ["public-read", "public-read-write", "authenticated-read"]
    msg := sprintf(
        "S3 bucket '%s' has insecure ACL '%s'. Use private ACL and bucket policies instead.",
        [change.address, change.after.acl]
    )
}

deny[msg] if {
    change := input.plan.resource_changes[_]
    change.type == "aws_s3_bucket_public_access_block"
    change.actions[_] in ["create", "update"]
    not change.after.block_public_acls
    msg := sprintf(
        "S3 bucket public access block '%s' does not block public ACLs.",
        [change.address]
    )
}

deny[msg] if {
    change := input.plan.resource_changes[_]
    change.type == "aws_s3_bucket_public_access_block"
    change.actions[_] in ["create", "update"]
    not change.after.block_public_policy
    msg := sprintf(
        "S3 bucket public access block '%s' does not block public bucket policies.",
        [change.address]
    )
}

# ─── GCS (Google Cloud Storage) ───────────────────────────────────────────────

deny[msg] if {
    change := input.plan.resource_changes[_]
    change.type == "google_storage_bucket"
    change.actions[_] in ["create", "update"]
    change.after.uniform_bucket_level_access == false
    msg := sprintf(
        "GCS bucket '%s' must have uniform_bucket_level_access enabled.",
        [change.address]
    )
}

deny[msg] if {
    change := input.plan.resource_changes[_]
    change.type == "google_storage_bucket_iam_member"
    change.actions[_] in ["create", "update"]
    change.after.member in ["allUsers", "allAuthenticatedUsers"]
    msg := sprintf(
        "GCS bucket IAM binding '%s' grants access to all users. Remove public access.",
        [change.address]
    )
}
