package sandbox_test

import (
	"strings"
	"testing"
	"time"

	"github.com/piyushsingariya/agentic-bash/sandbox"
)

// newSandbox is a test helper that creates a sandbox and registers cleanup.
func newSandbox(t *testing.T, opts sandbox.Options) *sandbox.Sandbox {
	t.Helper()
	s, err := sandbox.New(opts)
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestRun_BasicOutput verifies that stdout is captured correctly.
func TestRun_BasicOutput(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	r := s.Run("echo hello")
	if r.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d (stderr: %s)", r.ExitCode, r.Stderr)
	}
	if got := strings.TrimSpace(r.Stdout); got != "hello" {
		t.Errorf("expected stdout %q, got %q", "hello", got)
	}
}

// TestRun_ExitCodes verifies that various exit codes are propagated faithfully.
func TestRun_ExitCodes(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	cases := []struct {
		cmd      string
		wantCode int
	}{
		{"true", 0},
		{"false", 1},
		{"exit 0", 0},
		{"exit 1", 1},
		{"exit 42", 42},
		{"exit 127", 127},
	}

	for _, tc := range cases {
		r := s.Run(tc.cmd)
		if r.ExitCode != tc.wantCode {
			t.Errorf("cmd=%q: want exit %d, got %d", tc.cmd, tc.wantCode, r.ExitCode)
		}
	}
}

// TestRun_Timeout verifies that a command exceeding the timeout is killed.
func TestRun_Timeout(t *testing.T) {
	s := newSandbox(t, sandbox.Options{
		Limits: sandbox.ResourceLimits{
			Timeout: 150 * time.Millisecond,
		},
	})

	start := time.Now()
	r := s.Run("sleep 60")
	elapsed := time.Since(start)

	if r.ExitCode == 0 {
		t.Fatal("expected non-zero exit code after timeout")
	}
	if r.Error == nil {
		t.Error("expected non-nil Error after timeout")
	}
	// Should be killed well within 3 seconds; 2s is a generous upper bound.
	if elapsed > 2*time.Second {
		t.Errorf("process took too long to be killed: %v (want < 2s)", elapsed)
	}
}

// TestRun_Duration verifies that Duration in the result is reasonable.
func TestRun_Duration(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	r := s.Run("true")
	if r.Duration <= 0 {
		t.Errorf("expected positive Duration, got %v", r.Duration)
	}
	if r.Duration > 5*time.Second {
		t.Errorf("Duration suspiciously large: %v", r.Duration)
	}
}

// TestRun_Stderr verifies that stderr is captured separately from stdout.
func TestRun_Stderr(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	r := s.Run("echo out; echo err >&2")
	if r.ExitCode != 0 {
		t.Fatalf("unexpected exit %d", r.ExitCode)
	}
	if !strings.Contains(r.Stdout, "out") {
		t.Errorf("stdout missing 'out': %q", r.Stdout)
	}
	if !strings.Contains(r.Stderr, "err") {
		t.Errorf("stderr missing 'err': %q", r.Stderr)
	}
	if strings.Contains(r.Stdout, "err") {
		t.Errorf("stderr content leaked into stdout: %q", r.Stdout)
	}
}

// TestRun_InitialEnv verifies that Options.Env variables are visible to commands.
func TestRun_InitialEnv(t *testing.T) {
	s := newSandbox(t, sandbox.Options{
		Env: map[string]string{"GREETING": "hello_world"},
	})

	r := s.Run("echo $GREETING")
	if r.ExitCode != 0 {
		t.Fatalf("unexpected exit %d: %s", r.ExitCode, r.Stderr)
	}
	if got := strings.TrimSpace(r.Stdout); got != "hello_world" {
		t.Errorf("expected %q, got %q", "hello_world", got)
	}
}

// TestRun_EnvPersistsAcrossRuns verifies that variables exported in one Run()
// are visible in the next Run() — the core stateful session behaviour.
func TestRun_EnvPersistsAcrossRuns(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	r1 := s.Run("export PERSIST_ME=42")
	if r1.ExitCode != 0 {
		t.Fatalf("run 1 failed (exit %d): %s", r1.ExitCode, r1.Stderr)
	}

	r2 := s.Run("echo $PERSIST_ME")
	if r2.ExitCode != 0 {
		t.Fatalf("run 2 failed (exit %d): %s", r2.ExitCode, r2.Stderr)
	}
	if got := strings.TrimSpace(r2.Stdout); got != "42" {
		t.Errorf("expected '42', got %q", got)
	}
}

// TestRun_MultipleEnvChanges verifies that multiple env assignments in a single
// Run() all persist, and that unrelated variables are unaffected.
func TestRun_MultipleEnvChanges(t *testing.T) {
	s := newSandbox(t, sandbox.Options{
		Env: map[string]string{"INITIAL": "yes"},
	})

	s.Run("export A=1; export B=2; export C=3")

	r := s.Run("echo $A $B $C $INITIAL")
	if got := strings.TrimSpace(r.Stdout); got != "1 2 3 yes" {
		t.Errorf("expected '1 2 3 yes', got %q", got)
	}
}

// TestRun_CwdPersistsAcrossRuns verifies that cd in one Run() changes the
// working directory for subsequent Run() calls.
func TestRun_CwdPersistsAcrossRuns(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	// Create a subdirectory inside the sandbox's temp dir and cd into it.
	r1 := s.Run("mkdir testsubdir && cd testsubdir")
	if r1.ExitCode != 0 {
		t.Fatalf("run 1 failed (exit %d): %s", r1.ExitCode, r1.Stderr)
	}

	// The next Run() should start in testsubdir.
	r2 := s.Run("pwd")
	if r2.ExitCode != 0 {
		t.Fatalf("run 2 failed (exit %d): %s", r2.ExitCode, r2.Stderr)
	}
	got := strings.TrimSpace(r2.Stdout)
	if !strings.HasSuffix(got, "/testsubdir") {
		t.Errorf("expected cwd to end with /testsubdir, got %q", got)
	}
}

// TestRun_Pipeline verifies that shell pipelines work correctly.
func TestRun_Pipeline(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	r := s.Run("echo 'hello world' | tr '[:lower:]' '[:upper:]'")
	if r.ExitCode != 0 {
		t.Fatalf("unexpected exit %d: %s", r.ExitCode, r.Stderr)
	}
	if got := strings.TrimSpace(r.Stdout); got != "HELLO WORLD" {
		t.Errorf("expected 'HELLO WORLD', got %q", got)
	}
}

// TestRun_MultilineCommand verifies that multi-line scripts work end-to-end.
func TestRun_MultilineCommand(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	r := s.Run(`x=0
for i in 1 2 3 4 5; do
  x=$((x + i))
done
echo $x`)
	if r.ExitCode != 0 {
		t.Fatalf("unexpected exit %d: %s", r.ExitCode, r.Stderr)
	}
	if got := strings.TrimSpace(r.Stdout); got != "15" {
		t.Errorf("expected '15', got %q", got)
	}
}

// TestRun_Redirection verifies that output redirections work.
func TestRun_Redirection(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	// Write to a file, then read it back in the next run.
	r1 := s.Run("echo sandboxed > hello.txt")
	if r1.ExitCode != 0 {
		t.Fatalf("run 1 failed (exit %d): %s", r1.ExitCode, r1.Stderr)
	}

	r2 := s.Run("cat hello.txt")
	if r2.ExitCode != 0 {
		t.Fatalf("run 2 failed (exit %d): %s", r2.ExitCode, r2.Stderr)
	}
	if got := strings.TrimSpace(r2.Stdout); got != "sandboxed" {
		t.Errorf("expected 'sandboxed', got %q", got)
	}
}

// TestRun_Reset verifies that Reset() clears env changes and resets cwd.
func TestRun_Reset(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	initialCwd := strings.TrimSpace(s.Run("pwd").Stdout)

	// Change state.
	s.Run("export RESET_ME=before")
	s.Run("mkdir resettest && cd resettest")

	// Verify state changed.
	if got := strings.TrimSpace(s.Run("echo $RESET_ME").Stdout); got != "before" {
		t.Fatalf("setup: expected 'before', got %q", got)
	}

	s.Reset()

	// Env should be cleared.
	r := s.Run("echo ${RESET_ME:-empty}")
	if got := strings.TrimSpace(r.Stdout); got != "empty" {
		t.Errorf("after Reset: expected 'empty', got %q", got)
	}

	// Cwd should be back to the initial WorkDir.
	if got := strings.TrimSpace(s.Run("pwd").Stdout); got != initialCwd {
		t.Errorf("after Reset: expected cwd %q, got %q", initialCwd, got)
	}
}

// TestRun_ClosedSandbox verifies that Run() on a closed sandbox returns an error.
func TestRun_ClosedSandbox(t *testing.T) {
	s, err := sandbox.New(sandbox.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	r := s.Run("echo hi")
	if r.ExitCode != 1 {
		t.Errorf("expected exit 1 for closed sandbox, got %d", r.ExitCode)
	}
	if r.Error == nil {
		t.Error("expected non-nil Error for closed sandbox")
	}
}

// TestRun_OnCommandHook verifies that OnCommand is called before execution.
func TestRun_OnCommandHook(t *testing.T) {
	var got []string
	s := newSandbox(t, sandbox.Options{
		OnCommand: func(cmd string) { got = append(got, cmd) },
	})

	s.Run("echo a")
	s.Run("echo b")

	if len(got) != 2 || got[0] != "echo a" || got[1] != "echo b" {
		t.Errorf("OnCommand calls: %v", got)
	}
}

// TestRun_OnResultHook verifies that OnResult is called after execution
// and receives the correct ExitCode.
func TestRun_OnResultHook(t *testing.T) {
	var codes []int
	s := newSandbox(t, sandbox.Options{
		OnResult: func(r sandbox.ExecutionResult) { codes = append(codes, r.ExitCode) },
	})

	s.Run("true")
	s.Run("false")
	s.Run("exit 5")

	if len(codes) != 3 || codes[0] != 0 || codes[1] != 1 || codes[2] != 5 {
		t.Errorf("OnResult exit codes: %v, want [0 1 5]", codes)
	}
}

// TestRun_StateAccessor verifies that State() reflects current session state.
func TestRun_StateAccessor(t *testing.T) {
	s := newSandbox(t, sandbox.Options{
		Env: map[string]string{"FOO": "bar"},
	})

	if s.State().Env["FOO"] != "bar" {
		t.Error("State().Env missing initial FOO")
	}

	s.Run("export NEW_VAR=hello")

	if s.State().Env["NEW_VAR"] != "hello" {
		t.Errorf("State().Env missing NEW_VAR after Run, got: %v", s.State().Env)
	}

	if len(s.State().History) != 1 {
		t.Errorf("expected 1 history entry, got %d", len(s.State().History))
	}
}
