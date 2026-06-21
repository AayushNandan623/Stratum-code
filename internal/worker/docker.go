package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// DockerExecutor runs IaC operations inside Docker containers using the
// OpenTofu/Terraform runner image.
type DockerExecutor struct {
	client     *client.Client
	imageCache map[string]bool
	pullMu     sync.Mutex
	stateDir   string // host directory for /state bind mount
}

// NewDockerExecutor creates a DockerExecutor. stateDir is the host directory
// mounted as /state for plan files and other artifacts. Returns the Executor
// interface so callers can use any executor type interchangeably.
func NewDockerExecutor(stateDir string) (Executor, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &DockerExecutor{
		client:     cli,
		imageCache: make(map[string]bool),
		stateDir:   stateDir,
	}, nil
}

// EnsureImage is exported for testing — pulls the image if not cached.
func (e *DockerExecutor) ensureImage(ctx context.Context, imageRef string) error {
	e.pullMu.Lock()
	if e.imageCache[imageRef] {
		e.pullMu.Unlock()
		return nil
	}
	e.pullMu.Unlock()

	e.pullMu.Lock()
	defer e.pullMu.Unlock()

	if e.imageCache[imageRef] {
		return nil
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		reader, err := e.client.ImagePull(ctx, imageRef, dockertypes.ImagePullOptions{})
		if err != nil {
			lastErr = err
			continue
		}
		io.Copy(io.Discard, reader)
		reader.Close()
		lastErr = nil
		break
	}
	if lastErr != nil {
		return fmt.Errorf("image pull failed after retries: %w", lastErr)
	}
	e.imageCache[imageRef] = true
	return nil
}

// Execute runs the IaC task inside a Docker container. Implements Executor.
func (e *DockerExecutor) Execute(ctx context.Context, task *ExecutionTask) (*ExecutionResult, error) {
	imageRef := fmt.Sprintf("ghcr.io/opentofu/opentofu:%s", task.IACVersion)

	if err := e.ensureImage(ctx, imageRef); err != nil {
		return nil, fmt.Errorf("image setup: %w", err)
	}

	runStateDir := filepath.Join(e.stateDir, task.RunID.String())
	if err := os.MkdirAll(runStateDir, 0755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	defer os.RemoveAll(runStateDir)

	containerCfg := &container.Config{
		Image:      imageRef,
		Cmd:        buildCommand(task),
		Env:        e.buildEnv(task),
		User:       "10000:10000",
		WorkingDir: "/workspace",
	}

	hostCfg := &container.HostConfig{
		NetworkMode: container.NetworkMode("none"),
		Binds: []string{
			fmt.Sprintf("%s:/workspace:ro", task.WorkDir),
			fmt.Sprintf("%s:/state:rw", runStateDir),
		},
		Resources: container.Resources{
			Memory:   512 * 1024 * 1024,
			NanoCPUs: 1_000_000_000,
		},
		ReadonlyRootfs: true,
	}

	resp, err := e.client.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("container create: %w", err)
	}

	containerID := resp.ID
	defer e.client.ContainerRemove(context.Background(), containerID, dockertypes.ContainerRemoveOptions{Force: true})

	if err := e.client.ContainerStart(ctx, containerID, dockertypes.ContainerStartOptions{}); err != nil {
		return nil, fmt.Errorf("container start: %w", err)
	}

	logReader, err := e.client.ContainerLogs(ctx, containerID, dockertypes.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: true, Timestamps: false,
	})
	if err != nil {
		return nil, fmt.Errorf("container logs: %w", err)
	}

	logDone := make(chan struct{})
	go func() {
		e.streamLogs(logReader, task.LogCallback)
		logReader.Close()
		close(logDone)
	}()

	statusCh, errCh := e.client.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)

	select {
	case status := <-statusCh:
		<-logDone
		result := &ExecutionResult{ExitCode: int(status.StatusCode)}
		if status.StatusCode == 0 && task.LogCallback != nil {
			task.LogCallback(LogLine{
				Line:       fmt.Sprintf("Container exited with code %d", status.StatusCode),
				Source:     "system",
				OccurredAt: time.Now(),
			})
		}
		if status.StatusCode == 0 && (task.RunType == WorkerRunTypePlan || task.RunType == WorkerRunTypeDriftDetect) {
			planOutput, err := e.parsePlanOutput(runStateDir)
			if err == nil {
				result.PlanOutput = planOutput
			}
		}
		if status.StatusCode != 0 {
			result.Error = fmt.Sprintf("exit code %d", status.StatusCode)
		}
		return result, nil

	case err := <-errCh:
		<-logDone
		return nil, fmt.Errorf("container wait: %w", err)

	case <-ctx.Done():
		stopTimeout := 30
		e.client.ContainerStop(context.Background(), containerID, container.StopOptions{Timeout: &stopTimeout})
		<-logDone
		return &ExecutionResult{ExitCode: -1, Error: "execution cancelled"}, nil
	}
}

func (e *DockerExecutor) streamLogs(reader io.Reader, callback func(line LogLine)) {
	if callback == nil {
		return
	}
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) > 8 {
			line = line[8:]
		}
		now := time.Now()
		callback(LogLine{Line: line, Source: "stdout", OccurredAt: now})
	}
}

func (e *DockerExecutor) buildEnv(task *ExecutionTask) []string {
	env := make([]string, 0, len(task.Env)+8)
	for _, ev := range task.Env {
		env = append(env, ev.Name+"="+ev.Value)
	}
	sb := task.StateBackend
	env = append(env,
		"TF_HTTP_ADDRESS="+sb.Address,
		"TF_HTTP_LOCK_ADDRESS="+sb.LockAddress,
		"TF_HTTP_UNLOCK_ADDRESS="+sb.UnlockAddress,
		"TF_HTTP_USERNAME="+sb.Username,
		"TF_HTTP_PASSWORD="+sb.Password,
	)
	return env
}

func buildCommand(task *ExecutionTask) []string {
	switch task.RunType {
	case WorkerRunTypePlan:
		return []string{"plan", "-input=false", "-json", "-out=/state/plan.tfplan"}
	case WorkerRunTypeApply:
		return []string{"apply", "-input=false", "-json", "/state/plan.tfplan"}
	case WorkerRunTypeDestroy:
		return []string{"destroy", "-input=false", "-json", "-auto-approve"}
	case WorkerRunTypeDriftDetect:
		return []string{"plan", "-input=false", "-json", "-refresh-only"}
	default:
		return []string{"plan", "-input=false", "-json", "-out=/state/plan.tfplan"}
	}
}

func (e *DockerExecutor) parsePlanOutput(stateDir string) (*PlanOutput, error) {
	planPath := filepath.Join(stateDir, "plan.tfplan")
	if _, err := os.Stat(planPath); os.IsNotExist(err) {
		return &PlanOutput{Raw: []byte("{}")}, nil
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		return nil, err
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err == nil {
		return &PlanOutput{Raw: data, HasChanges: true}, nil
	}
	return &PlanOutput{Raw: data}, nil
}
