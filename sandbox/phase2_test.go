// phase2_test.go — tests for Phase 2 ShellExecutor features.
// These tests verify behaviours that required the in-process mvdan.cc/sh
// interpreter: function persistence, accurate state extraction, bash syntax
// extensions, and the set -e / subshell semantics.

package sandbox_test

import (
	"strings"
	"testing"

	"github.com/piyushsingariya/agentic-bash/sandbox"
)

// TestPhase2_FunctionPersistsAcrossRuns verifies that a shell function defined
// in one Run() is callable in subsequent Run() calls.
func TestPhase2_FunctionPersistsAcrossRuns(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	r1 := s.Run(`greet() { echo "hello $1"; }`)
	if r1.ExitCode != 0 {
		t.Fatalf("run 1 failed (exit %d): %s", r1.ExitCode, r1.Stderr)
	}

	r2 := s.Run("greet world")
	if r2.ExitCode != 0 {
		t.Fatalf("run 2 failed (exit %d): %s", r2.ExitCode, r2.Stderr)
	}
	if got := strings.TrimSpace(r2.Stdout); got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

// TestPhase2_FunctionUpdated verifies that redefining a function replaces the
// previous definition.
func TestPhase2_FunctionUpdated(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	s.Run(`say() { echo "v1"; }`)
	s.Run(`say() { echo "v2"; }`) // override

	r := s.Run("say")
	if got := strings.TrimSpace(r.Stdout); got != "v2" {
		t.Errorf("expected 'v2', got %q", got)
	}
}

// TestPhase2_SetE verifies that set -e aborts on the first failing command.
func TestPhase2_SetE(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	r := s.Run(`set -e
false
echo "should not reach here"`)

	if r.ExitCode == 0 {
		t.Error("expected non-zero exit with set -e")
	}
	if strings.Contains(r.Stdout, "should not reach here") {
		t.Error("script continued past failing command with set -e")
	}
}

// TestPhase2_Subshell verifies that variable changes inside a subshell do not
// leak to the parent shell environment.
func TestPhase2_Subshell(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	s.Run("export OUTER=yes")
	r := s.Run(`( export INNER=yes ); echo ${INNER:-not_set}`)
	if got := strings.TrimSpace(r.Stdout); got != "not_set" {
		t.Errorf("subshell variable leaked to parent; got %q, want 'not_set'", got)
	}
}

// TestPhase2_Arithmetic verifies arithmetic expansion and compound expressions.
func TestPhase2_Arithmetic(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	r := s.Run(`a=6; b=7; echo $((a * b))`)
	if got := strings.TrimSpace(r.Stdout); got != "42" {
		t.Errorf("expected '42', got %q", got)
	}
}

// TestPhase2_HereDoc verifies here-document syntax.
func TestPhase2_HereDoc(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	r := s.Run(`cat <<EOF
line one
line two
EOF`)
	lines := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	if len(lines) != 2 || lines[0] != "line one" || lines[1] != "line two" {
		t.Errorf("unexpected here-doc output: %q", r.Stdout)
	}
}

// TestPhase2_CommandSubstitution verifies $(...) command substitution.
func TestPhase2_CommandSubstitution(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	r := s.Run(`result=$(echo hello | tr '[:lower:]' '[:upper:]'); echo $result`)
	if got := strings.TrimSpace(r.Stdout); got != "HELLO" {
		t.Errorf("expected 'HELLO', got %q", got)
	}
}

// TestPhase2_BashArray verifies bash array syntax (requires LangBash parser).
func TestPhase2_BashArray(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	r := s.Run(`arr=(alpha beta gamma); echo ${arr[1]}`)
	if r.ExitCode != 0 {
		t.Fatalf("exit %d: %s", r.ExitCode, r.Stderr)
	}
	if got := strings.TrimSpace(r.Stdout); got != "beta" {
		t.Errorf("expected 'beta', got %q", got)
	}
}

// TestPhase2_ProcessSubstitution verifies process substitution <(...).
func TestPhase2_ProcessSubstitution(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	r := s.Run(`diff <(echo a) <(echo a); echo "same: $?"`)
	if r.ExitCode != 0 {
		t.Fatalf("exit %d: %s", r.ExitCode, r.Stderr)
	}
	if got := strings.TrimSpace(r.Stdout); got != "same: 0" {
		t.Errorf("expected 'same: 0', got %q", got)
	}
}

// TestPhase2_EnvOverridesPersistAfterReset verifies that Reset() restores the
// Options.Env — not the env accumulated during prior runs.
func TestPhase2_EnvOverridesPersistAfterReset(t *testing.T) {
	s := newSandbox(t, sandbox.Options{
		Env: map[string]string{"LEVEL": "base"},
	})

	s.Run("export LEVEL=modified")
	if got := strings.TrimSpace(s.Run("echo $LEVEL").Stdout); got != "modified" {
		t.Fatalf("setup: expected 'modified', got %q", got)
	}

	s.Reset()

	// After reset, LEVEL should be back to "base".
	r := s.Run("echo $LEVEL")
	if got := strings.TrimSpace(r.Stdout); got != "base" {
		t.Errorf("after reset: expected 'base', got %q", got)
	}
}

// TestPhase2_FunctionClearedByReset verifies that Reset() removes function
// definitions accumulated in previous runs.
func TestPhase2_FunctionClearedByReset(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	s.Run(`myFunc() { echo "still here"; }`)

	// Confirm it works before reset.
	if r := s.Run("myFunc"); strings.TrimSpace(r.Stdout) != "still here" {
		t.Fatalf("setup failed: myFunc not defined")
	}

	s.Reset()

	// After reset, myFunc should not exist.
	r := s.Run("myFunc 2>/dev/null; echo exit:$?")
	if strings.Contains(r.Stdout, "still here") {
		t.Error("function survived Reset()")
	}
	if !strings.Contains(r.Stdout, "exit:127") {
		t.Errorf("expected exit:127, got %q", r.Stdout)
	}
}

// TestPhase2_MultiLineScript verifies that multi-line scripts with control
// flow work correctly as a single Run() call.
func TestPhase2_MultiLineScript(t *testing.T) {
	s := newSandbox(t, sandbox.Options{})

	r := s.Run(`
total=0
for i in $(seq 1 10); do
  total=$((total + i))
done
echo $total
`)
	if r.ExitCode != 0 {
		t.Fatalf("exit %d: %s", r.ExitCode, r.Stderr)
	}
	if got := strings.TrimSpace(r.Stdout); got != "55" {
		t.Errorf("expected '55', got %q", got)
	}
}
