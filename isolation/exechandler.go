package isolation

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"

	"github.com/piyushsingariya/agentic-bash/internal/cgroups"
	"github.com/piyushsingariya/agentic-bash/internal/limitwriter"
	"github.com/piyushsingariya/agentic-bash/network"
)

// ExecLimits carries per-invocation resource limits injected at wireHandlers time.
// Zero values disable the corresponding feature.
type ExecLimits struct {
	MaxOutputBytes int64           // combined stdout+stderr cap; 0 = no cap
	CgroupManager  cgroups.Manager // nil or unavailable = no cgroup
	MaxMemoryBytes int64           // 0 = no limit (written to memory.max)
	CPUQuota       float64         // 0 = no limit (0.5 = 50% of one CPU)
	NetworkFilter  network.Filter  // nil = no network restrictions (Phase 7)
}

// ExecMetrics accumulates resource usage across all external commands spawned
// during a single Run() interval.  The sandbox reads it after Run() returns.
type ExecMetrics struct {
	mu           sync.Mutex
	CPUUsec      uint64
	MemPeakBytes uint64
	LimitHit     bool
}

func (m *ExecMetrics) record(cpuUsec, memPeak uint64) {
	m.mu.Lock()
	m.CPUUsec += cpuUsec
	if memPeak > m.MemPeakBytes {
		m.MemPeakBytes = memPeak
	}
	m.mu.Unlock()
}

// NewIsolatedExecHandler returns an interp.ExecHandlerFunc that:
//   - applies strategy.Wrap() (namespace flags, etc.) to every external command,
//   - kills the entire process group on context cancellation,
//   - caps combined stdout+stderr output when limits.MaxOutputBytes > 0,
//   - places the subprocess into a cgroupv2 scope when limits.CgroupManager is available.
//
// metrics may be nil; when non-nil CPU and memory usage are accumulated into it.
func NewIsolatedExecHandler(strategy IsolationStrategy, limits ExecLimits, metrics *ExecMetrics) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		hc := interp.HandlerCtx(ctx)

		path, err := interp.LookPathDir(hc.Dir, hc.Env, args[0])
		if err != nil {
			if strings.Contains(err.Error(), "permission denied") {
				return interp.NewExitStatus(126)
			}
			return interp.NewExitStatus(127)
		}

		// --- output cap ---
		// We need the PID to kill the group, but it isn't available until after
		// cmd.Start().  Store it in an atomic so the onLimit callback can race
		// safely even if the writer fires before we record the PID (it won't,
		// since writes only flow during cmd.Wait()).
		var pidStore atomic.Int64
		var killOnce sync.Once
		killFn := func() {
			killOnce.Do(func() {
				if pid := int(pidStore.Load()); pid != 0 {
					_ = killProcessGroup(pid)
				}
				if metrics != nil {
					metrics.mu.Lock()
					metrics.LimitHit = true
					metrics.mu.Unlock()
				}
			})
		}

		var outW, errW = hc.Stdout, hc.Stderr
		if limits.MaxOutputBytes > 0 {
			outW, errW = limitwriter.NewPair(hc.Stdout, hc.Stderr, limits.MaxOutputBytes, killFn)
		}

		cmd := &exec.Cmd{
			Path:   path,
			Args:   args,
			Env:    environToSlice(hc.Env),
			Dir:    hc.Dir,
			Stdin:  hc.Stdin,
			Stdout: outW,
			Stderr: errW,
		}

		initCmd(cmd)

		if err := strategy.Wrap(cmd); err != nil {
			return err
		}

		// Phase 7: apply network filter (e.g. CLONE_NEWNET for deny mode).
		if limits.NetworkFilter != nil {
			if err := limits.NetworkFilter.Wrap(cmd); err != nil {
				return err
			}
		}

		if err := cmd.Start(); err != nil {
			return err
		}
		pidStore.Store(int64(cmd.Process.Pid))

		// --- cgroup ---
		var cg cgroups.Cgroup
		if limits.CgroupManager != nil && limits.CgroupManager.Available() {
			id := strconv.Itoa(cmd.Process.Pid)
			if created, cgErr := limits.CgroupManager.New(id, cgroups.Opts{
				MaxMemoryBytes: limits.MaxMemoryBytes,
				CPUQuota:       limits.CPUQuota,
			}); cgErr == nil {
				cg = created
				_ = cg.AddPID(cmd.Process.Pid)
			}
		}

		// Kill the process group when the context is cancelled (timeout / abort).
		waitDone := make(chan struct{})
		defer close(waitDone)
		go func() {
			select {
			case <-ctx.Done():
				killFn()
			case <-waitDone:
			}
		}()

		waitErr := cmd.Wait()

		// Harvest cgroup metrics before removing the cgroup directory.
		if cg != nil {
			cpuUsec, memPeak, _ := cg.Stop()
			if metrics != nil {
				metrics.record(cpuUsec, memPeak)
			}
		}

		return toExitStatus(waitErr)
	}
}

// NewIsolatedExecMiddleware returns an ExecHandlers-compatible middleware that
// applies the same isolation, resource limits, and metrics as NewIsolatedExecHandler.
// The next parameter is ignored — this middleware is always terminal: it spawns
// the real process and never delegates further. The DefaultExecHandler appended
// by interp.ExecHandlers is therefore unreachable when this middleware is present.
func NewIsolatedExecMiddleware(strategy IsolationStrategy, limits ExecLimits, metrics *ExecMetrics) func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(_ interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return NewIsolatedExecHandler(strategy, limits, metrics)
	}
}

// toExitStatus converts a cmd.Wait error into the interp exit-status type that
// mvdan.cc/sh understands.  Non-exit errors are returned verbatim.
func toExitStatus(err error) error {
	if err == nil {
		return nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		code := ee.ExitCode()
		if code < 0 {
			code = 1 // killed by signal
		}
		return interp.NewExitStatus(uint8(code))
	}
	return err
}

// environToSlice converts an expand.Environ into the []string slice format
// used by exec.Cmd.Env ("KEY=value" pairs).
// Only exported string variables are included, matching the behaviour of
// mvdan.cc/sh's internal execEnv helper.
func environToSlice(env expand.Environ) []string {
	var list []string
	env.Each(func(name string, vr expand.Variable) bool {
		if vr.Exported && vr.Kind == expand.String {
			list = append(list, name+"="+vr.Str)
		}
		return true
	})
	return list
}
