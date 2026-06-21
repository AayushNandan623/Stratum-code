package worker

import "context"

// Executor runs an IaC execution task (plan, apply, destroy, drift_detect) and
// returns the result. Implementations wrap Docker, Kubernetes Jobs, or local
// subprocess execution. The executor is responsible for container lifecycle,
// log streaming via LogCallback, and cleanup regardless of success or failure.
type Executor interface {
	// Execute runs the task. The ctx can be cancelled to abort execution;
	// the implementation should SIGTERM the child process or container and
	// return with a non-zero exit code in the result.
	Execute(ctx context.Context, task *ExecutionTask) (*ExecutionResult, error)
}
