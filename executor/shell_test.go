package executor_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/piyushsingariya/agentic-bash/executor"
)

// hostEnvSlice returns the host process environment as KEY=VALUE pairs.
func hostEnvSlice() []string { return os.Environ() }

// newShellExec is a test helper that creates a ShellExecutor in the host env.
func newShellExec(t *testing.T) *executor.ShellExecutor {
	t.Helper()
	tmpDir := t.TempDir()
	return executor.NewShellExecutor(hostEnvSlice(), tmpDir)
}

func TestShellExecutor_BasicOutput(t *testing.T) {
	e := newShellExec(t)
	r := e.Run(context.Background(), "echo hello", nil, "")
	if r.ExitCode != 0 {
		t.Fatalf("exit %d: %s", r.ExitCode, r.Stderr)
	}
	if got := strings.TrimSpace(r.Stdout); got != "hello" {
		t.Errorf("want 'hello', got %q", got)
	}
}

func TestShellExecutor_ExitCode(t *testing.T) {
	e := newShellExec(t)
	cases := []struct{ cmd string; want int }{
		{"true", 0},
		{"false", 1},
		{"exit 42", 42},
	}
	for _, tc := range cases {
		r := e.Run(context.Background(), tc.cmd, nil, "")
		if r.ExitCode != tc.want {
			t.Errorf("cmd=%q: want exit %d, got %d", tc.cmd, tc.want, r.ExitCode)
		}
	}
}

func TestShellExecutor_Timeout(t *testing.T) {
	e := newShellExec(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	r := e.Run(ctx, "sleep 60", nil, "")
	elapsed := time.Since(start)

	if r.ExitCode == 0 {
		t.Fatal("expected non-zero exit after timeout")
	}
	if r.Error == nil {
		t.Error("expected non-nil Error after timeout")
	}
	if elapsed > 2*time.Second {
		t.Errorf("process took too long: %v", elapsed)
	}
}

func TestShellExecutor_Stderr(t *testing.T) {
	e := newShellExec(t)
	r := e.Run(context.Background(), "echo out; echo err >&2", nil, "")
	if !strings.Contains(r.Stdout, "out") {
		t.Errorf("stdout missing 'out': %q", r.Stdout)
	}
	if !strings.Contains(r.Stderr, "err") {
		t.Errorf("stderr missing 'err': %q", r.Stderr)
	}
}

// TestShellExecutor_Pipeline verifies that pipelines work end-to-end.
func TestShellExecutor_Pipeline(t *testing.T) {
	e := newShellExec(t)
	r := e.Run(context.Background(), "echo 'hello world' | tr '[:lower:]' '[:upper:]'", nil, "")
	if r.ExitCode != 0 {
		t.Fatalf("exit %d: %s", r.ExitCode, r.Stderr)
	}
	if got := strings.TrimSpace(r.Stdout); got != "HELLO WORLD" {
		t.Errorf("want 'HELLO WORLD', got %q", got)
	}
}

// TestShellExecutor_EnvVarPersists verifies that exported variables survive
// across Run() calls via the StateExtractor mechanism.
func TestShellExecutor_EnvVarPersists(t *testing.T) {
	e := newShellExec(t)

	e.Run(context.Background(), "export MYVAR=hello", nil, "")

	r := e.Run(context.Background(), "echo $MYVAR", nil, "")
	if got := strings.TrimSpace(r.Stdout); got != "hello" {
		t.Errorf("want 'hello', got %q", got)
	}
}

// TestShellExecutor_LocalVarInSingleRun verifies that non-exported vars work
// within a single Run() (they are not expected to persist across calls).
func TestShellExecutor_LocalVarInSingleRun(t *testing.T) {
	e := newShellExec(t)
	r := e.Run(context.Background(), "x=42; echo $x", nil, "")
	if got := strings.TrimSpace(r.Stdout); got != "42" {
		t.Errorf("want '42', got %q", got)
	}
}

// TestShellExecutor_FunctionPersists verifies that shell functions defined in
// one Run() are callable in subsequent Run() calls.
func TestShellExecutor_FunctionPersists(t *testing.T) {
	e := newShellExec(t)

	r1 := e.Run(context.Background(), `greet() { echo "hello $1"; }`, nil, "")
	if r1.ExitCode != 0 {
		t.Fatalf("run 1 failed: %s", r1.Stderr)
	}

	r2 := e.Run(context.Background(), "greet world", nil, "")
	if r2.ExitCode != 0 {
		t.Fatalf("run 2 failed: %s", r2.Stderr)
	}
	if got := strings.TrimSpace(r2.Stdout); got != "hello world" {
		t.Errorf("want 'hello world', got %q", got)
	}
}

// TestShellExecutor_CwdPersists verifies that cd in one Run() is reflected
// in subsequent Run() calls via ExtractState().
func TestShellExecutor_CwdPersists(t *testing.T) {
	e := newShellExec(t)

	e.Run(context.Background(), "mkdir testdir && cd testdir", nil, "")

	_, cwd := e.ExtractState()
	if !strings.HasSuffix(cwd, "/testdir") {
		t.Errorf("expected cwd to end with /testdir, got %q", cwd)
	}
}

// TestShellExecutor_Arithmetic verifies shell arithmetic expansion.
func TestShellExecutor_Arithmetic(t *testing.T) {
	e := newShellExec(t)
	r := e.Run(context.Background(), "echo $((3 * 7))", nil, "")
	if got := strings.TrimSpace(r.Stdout); got != "21" {
		t.Errorf("want '21', got %q", got)
	}
}

// TestShellExecutor_HereDoc verifies here-document syntax.
func TestShellExecutor_HereDoc(t *testing.T) {
	e := newShellExec(t)
	r := e.Run(context.Background(), `cat <<EOF
heredoc line
EOF`, nil, "")
	if r.ExitCode != 0 {
		t.Fatalf("exit %d: %s", r.ExitCode, r.Stderr)
	}
	if got := strings.TrimSpace(r.Stdout); got != "heredoc line" {
		t.Errorf("want 'heredoc line', got %q", got)
	}
}

// TestShellExecutor_SetE verifies that set -e aborts on first failure.
func TestShellExecutor_SetE(t *testing.T) {
	e := newShellExec(t)
	r := e.Run(context.Background(), `set -e; false; echo "should not print"`, nil, "")
	if r.ExitCode == 0 {
		t.Error("expected non-zero exit with set -e and failing command")
	}
	if strings.Contains(r.Stdout, "should not print") {
		t.Error("script continued past failing command despite set -e")
	}
}

// TestShellExecutor_EmptyCommand verifies that an empty command returns cleanly.
func TestShellExecutor_EmptyCommand(t *testing.T) {
	e := newShellExec(t)
	r := e.Run(context.Background(), "", nil, "")
	if r.ExitCode != 0 {
		t.Errorf("expected exit 0 for empty command, got %d", r.ExitCode)
	}
	if r.Error != nil {
		t.Errorf("expected nil Error for empty command, got %v", r.Error)
	}
}

// TestShellExecutor_ParseError verifies that syntax errors are reported cleanly.
func TestShellExecutor_ParseError(t *testing.T) {
	e := newShellExec(t)
	r := e.Run(context.Background(), "if then fi", nil, "")
	if r.ExitCode == 0 {
		t.Error("expected non-zero exit for parse error")
	}
	if r.Stderr == "" {
		t.Error("expected non-empty Stderr for parse error")
	}
}

// TestShellExecutor_ResetState verifies that ResetState clears accumulated env
// and function definitions.
func TestShellExecutor_ResetState(t *testing.T) {
	e := newShellExec(t)

	e.Run(context.Background(), "export BEFORE=yes", nil, "")
	e.Run(context.Background(), `saved() { echo saved; }`, nil, "")

	if err := e.ResetState(map[string]string{}, t.TempDir()); err != nil {
		t.Fatalf("ResetState: %v", err)
	}

	// Exported var should be gone.
	r := e.Run(context.Background(), "echo ${BEFORE:-empty}", nil, "")
	if got := strings.TrimSpace(r.Stdout); got != "empty" {
		t.Errorf("expected 'empty' after reset, got %q", got)
	}

	// Function should be gone.
	r2 := e.Run(context.Background(), "saved 2>/dev/null; echo $?", nil, "")
	if exitStr := strings.TrimSpace(r2.Stdout); exitStr == "0" {
		t.Error("function 'saved' should not be available after ResetState")
	}
}

// TestShellExecutor_ExtractState verifies that ExtractState reflects the
// current exported env and working directory.
func TestShellExecutor_ExtractState(t *testing.T) {
	e := newShellExec(t)

	e.Run(context.Background(), "export EXTRACTED=42", nil, "")
	e.Run(context.Background(), "mkdir statedir && cd statedir", nil, "")

	env, cwd := e.ExtractState()
	if env["EXTRACTED"] != "42" {
		t.Errorf("ExtractState: EXTRACTED=%q, want '42'", env["EXTRACTED"])
	}
	if !strings.HasSuffix(cwd, "/statedir") {
		t.Errorf("ExtractState: cwd=%q, want suffix /statedir", cwd)
	}
}
