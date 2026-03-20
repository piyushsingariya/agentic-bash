package isolation_test

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/piyushsingariya/agentic-bash/isolation"
	"github.com/piyushsingariya/agentic-bash/sandbox"
)

// ─── NoopStrategy ────────────────────────────────────────────────────────────

func TestNoop_Available(t *testing.T) {
	s := isolation.NewNoop()
	if !s.Available() {
		t.Error("NoopStrategy.Available() must always return true")
	}
}

func TestNoop_Name(t *testing.T) {
	if got := isolation.NewNoop().Name(); got != "noop" {
		t.Errorf("want 'noop', got %q", got)
	}
}

func TestNoop_Wrap_NoOp(t *testing.T) {
	cmd := exec.Command("true")
	before := cmd.SysProcAttr
	if err := isolation.NewNoop().Wrap(cmd); err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if cmd.SysProcAttr != before {
		t.Error("NoopStrategy.Wrap must not modify SysProcAttr")
	}
}

func TestNoop_Apply_NoOp(t *testing.T) {
	if err := isolation.NewNoop().Apply(); err != nil {
		t.Errorf("Apply: %v", err)
	}
}

// ─── NamespaceStrategy ───────────────────────────────────────────────────────

func TestNamespace_Name(t *testing.T) {
	s := isolation.NewNamespaceForTest()
	if got := s.Name(); got != "namespace" {
		t.Errorf("want 'namespace', got %q", got)
	}
}

func TestNamespace_AvailableOnLinuxOnly(t *testing.T) {
	s := isolation.NewNamespaceForTest()
	if runtime.GOOS != "linux" {
		if s.Available() {
			t.Error("NamespaceStrategy.Available() must return false on non-Linux")
		}
		return
	}
	// On Linux we merely check that Available() does not panic.
	_ = s.Available()
}

// ─── LandlockStrategy ────────────────────────────────────────────────────────

func TestLandlock_Name(t *testing.T) {
	s := isolation.NewLandlockStrategy()
	if got := s.Name(); got != "landlock" {
		t.Errorf("want 'landlock', got %q", got)
	}
}

func TestLandlock_AvailableOnLinuxOnly(t *testing.T) {
	s := isolation.NewLandlockStrategy()
	if runtime.GOOS != "linux" {
		if s.Available() {
			t.Error("LandlockStrategy.Available() must return false on non-Linux")
		}
		return
	}
	// On Linux probe should not panic.
	_ = s.Available()
}

// ─── BestAvailable ───────────────────────────────────────────────────────────

func TestBestAvailable_ReturnsNonNil(t *testing.T) {
	s := isolation.BestAvailable()
	if s == nil {
		t.Fatal("BestAvailable() returned nil")
	}
}

func TestBestAvailable_AlwaysAvailable(t *testing.T) {
	s := isolation.BestAvailable()
	if !s.Available() {
		t.Errorf("BestAvailable() returned a strategy that is not available: %s", s.Name())
	}
}

func TestBestAvailable_FallsBackToNoopOnNonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("test only relevant on non-Linux")
	}
	s := isolation.BestAvailable()
	if s.Name() != "noop" {
		t.Errorf("expected 'noop' on non-Linux, got %q", s.Name())
	}
}

// ─── SelectStrategy ──────────────────────────────────────────────────────────

func TestSelectStrategy_None(t *testing.T) {
	// IsolationNone = 0
	s := isolation.SelectStrategy(0)
	if s.Name() != "noop" {
		t.Errorf("want 'noop' for level 0, got %q", s.Name())
	}
}

func TestSelectStrategy_Unknown(t *testing.T) {
	s := isolation.SelectStrategy(99)
	if s.Name() != "noop" {
		t.Errorf("want 'noop' for unknown level, got %q", s.Name())
	}
}

// ─── Sandbox integration ─────────────────────────────────────────────────────

func newSandbox(t *testing.T, opts sandbox.Options) *sandbox.Sandbox {
	t.Helper()
	s, err := sandbox.New(opts)
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSandbox_WithNoopIsolation(t *testing.T) {
	s := newSandbox(t, sandbox.Options{
		Isolation: sandbox.IsolationNone,
	})
	r := s.Run("echo hello")
	if r.ExitCode != 0 {
		t.Fatalf("exit %d: %s", r.ExitCode, r.Stderr)
	}
	if got := strings.TrimSpace(r.Stdout); got != "hello" {
		t.Errorf("want 'hello', got %q", got)
	}
}

func TestSandbox_WithAutoIsolation(t *testing.T) {
	s := newSandbox(t, sandbox.Options{
		Isolation: sandbox.IsolationAuto,
	})
	if !s.Isolation().Available() {
		t.Errorf("auto-selected strategy %q reports Available()=false", s.Isolation().Name())
	}
	r := s.Run("echo isolated")
	if r.ExitCode != 0 {
		t.Fatalf("exit %d: %s", r.ExitCode, r.Stderr)
	}
	if got := strings.TrimSpace(r.Stdout); got != "isolated" {
		t.Errorf("want 'isolated', got %q", got)
	}
}

func TestSandbox_ExternalCommandThroughExecHandler(t *testing.T) {
	// Verify that external commands still work correctly when the ExecHandler
	// is replaced by the IsolatedExecHandler (Noop strategy, all platforms).
	s := newSandbox(t, sandbox.Options{Isolation: sandbox.IsolationNone})

	r := s.Run("echo hello | tr a-z A-Z")
	if r.ExitCode != 0 {
		t.Fatalf("exit %d: %s", r.ExitCode, r.Stderr)
	}
	if got := strings.TrimSpace(r.Stdout); got != "HELLO" {
		t.Errorf("want 'HELLO', got %q", got)
	}
}

func TestSandbox_EnvPassedToExternalCommand(t *testing.T) {
	s := newSandbox(t, sandbox.Options{Isolation: sandbox.IsolationNone})

	r := s.Run("export MYVAR=world && env | grep MYVAR")
	if r.ExitCode != 0 {
		t.Fatalf("exit %d: %s", r.ExitCode, r.Stderr)
	}
	if !strings.Contains(r.Stdout, "MYVAR=world") {
		t.Errorf("expected MYVAR=world in output, got %q", r.Stdout)
	}
}

func TestSandbox_ExitCodeThroughExecHandler(t *testing.T) {
	s := newSandbox(t, sandbox.Options{Isolation: sandbox.IsolationNone})

	r := s.Run("false")
	if r.ExitCode == 0 {
		t.Error("expected non-zero exit code for 'false'")
	}
}

func TestSandbox_IsolationName_ReachableViaAccessor(t *testing.T) {
	for _, level := range []sandbox.IsolationLevel{
		sandbox.IsolationNone,
		sandbox.IsolationAuto,
	} {
		s := newSandbox(t, sandbox.Options{Isolation: level})
		name := s.Isolation().Name()
		if name == "" {
			t.Errorf("isolation level %d: Name() returned empty string", level)
		}
	}
}
