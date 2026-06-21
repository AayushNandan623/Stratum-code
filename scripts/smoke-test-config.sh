# scripts/smoke-test-config.sh — Configuration for smoke-test.sh
# Source this file from your smoke test script.
#
# Override any variable by exporting it before sourcing, or by editing here.

# ─── Control plane ───────────────────────────────────────────────────────────
STRATUM_BASE_URL="${STRATUM_BASE_URL:-http://localhost:8080}"
STRATUM_NATS_MONITOR_URL="${STRATUM_NATS_MONITOR_URL:-http://localhost:8222}"

# ─── Timeouts (seconds) ──────────────────────────────────────────────────────
POLL_INTERVAL="${POLL_INTERVAL:-2}"              # seconds between state polls
RUN_TIMEOUT="${RUN_TIMEOUT:-90}"                 # max wait for a run to reach terminal
WORKER_DOWN_TIMEOUT="${WORKER_DOWN_TIMEOUT:-60}" # max wait for re-queue after kill
RECONCILE_WAIT="${RECONCILE_WAIT:-35}"           # wait for auto drift-detect run

# ─── Test identity ────────────────────────────────────────────────────────────
TEST_ORG_NAME="${TEST_ORG_NAME:-smoke-test-org}"
TEST_ORG_SLUG="${TEST_ORG_SLUG:-smoke-test}"
TEST_ADMIN_EMAIL="${TEST_ADMIN_EMAIL:-admin@smoke-test.local}"
TEST_ADMIN_PASSWORD="${TEST_ADMIN_PASSWORD:-smoke-test-password}"
TEST_API_KEY_NAME="${TEST_API_KEY_NAME:-smoke-test-key}"

# ─── Stacks ───────────────────────────────────────────────────────────────────
TEST_STACK_A_NAME="${TEST_STACK_A_NAME:-smoke-stack-a}"
TEST_STACK_B_NAME="${TEST_STACK_B_NAME:-smoke-stack-b}"

# ─── Worker pool ──────────────────────────────────────────────────────────────
TEST_POOL_NAME="${TEST_POOL_NAME:-smoke-test-pool}"
TEST_POOL_TYPE="${TEST_POOL_TYPE:-PRIVATE}"

# ─── Output ───────────────────────────────────────────────────────────────────
SUMMARY_FILE="${SUMMARY_FILE:-/tmp/stratum-smoke-summary.json}"
