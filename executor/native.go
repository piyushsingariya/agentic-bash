package executor

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"sync/atomic"

	"github.com/piyushsingariya/agentic-bash/internal/limitwriter"
)

// NativeExecutor runs commands using the host's /bin/sh via os/exec.
type NativeExecutor struct {
	// outputLimitBytes caps combined stdout+stderr per Run(); 0 = no cap.
	outputLimitBytes int64
}

// NewNativeExecutor creates a new NativeExecutor.
func NewNativeExecutor() *NativeExecutor {
	return &NativeExecutor{}
}

// WithOutputLimit sets a combined stdout+stderr cap (in bytes) applied on each
// Run() call.  When the cap is exceeded the process group is killed.
// Zero disables the cap.
func (e *NativeExecutor) WithOutputLimit(maxBytes int64) {
	e.outputLimitBytes = maxBytes
}

// Run executes cmd in a /bin/sh subprocess with the provided environment and
// working directory. Context cancellation and timeout are honored; the entire
// process group is killed when the context expires so no orphan children remain.
func (e *NativeExecutor) Run(ctx context.Context, cmd string, env []string, dir string) Result {
	c := exec.CommandContext(ctx, "/bin/sh", "-c", cmd)
	c.Env = env
	c.Dir = dir
	setSysProcAttr(c) // platform-specific: Setpgid (+ Pdeathsig on Linux)

	var stdout, stderr bytes.Buffer

	// Output cap: LimitWriter needs the PID to kill the group, but the PID is
	// only available after c.Start().  Store it atomically so the onLimit
	// callback (fired during c.Wait()) can safely read it.
	var pidStore atomic.Int64
	var outW, errW io.Writer = &stdout, &stderr
	if e.outputLimitBytes > 0 {
		killFn := func() {
			if pid := int(pidStore.Load()); pid != 0 {
				_ = killProcessGroup(pid)
			}
		}
		outW, errW = limitwriter.NewPair(&stdout, &stderr, e.outputLimitBytes, killFn)
	}

	c.Stdout = outW
	c.Stderr = errW

	if err := c.Start(); err != nil {
		return Result{Stderr: err.Error(), ExitCode: 1, Error: err}
	}

	pid := c.Process.Pid
	pidStore.Store(int64(pid))
	processDone := make(chan struct{})

	// Kill the entire process group when the context is cancelled or expired.
	// This ensures child processes spawned by the command are also cleaned up.
	go func() {
		select {
		case <-ctx.Done():
			_ = killProcessGroup(pid)
		case <-processDone:
			// Process finished on its own; nothing to do.
		}
	}()

	waitErr := c.Wait()
	close(processDone) // unblock the watcher goroutine

	exitCode := 0
	var runErr error

	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
			if exitCode < 0 {
				// Killed by a signal (e.g., SIGKILL from timeout).
				exitCode = 1
			}
		} else {
			exitCode = 1
			runErr = waitErr
		}
		// Prefer the context error (DeadlineExceeded / Canceled) when present.
		if ctx.Err() != nil {
			runErr = ctx.Err()
		}
	}

	return Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		Error:    runErr,
	}
}
