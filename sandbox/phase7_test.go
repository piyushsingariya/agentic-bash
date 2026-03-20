package sandbox_test

import (
	"runtime"
	"strings"
	"testing"

	"github.com/piyushsingariya/agentic-bash/network"
	"github.com/piyushsingariya/agentic-bash/sandbox"
)

// TestPhase7_AllowMode verifies that unrestricted network access works by
// default (e.g. DNS resolution and loopback both succeed).
func TestPhase7_AllowMode(t *testing.T) {
	sb, err := sandbox.New(sandbox.Options{
		Network: sandbox.NetworkPolicy{Mode: sandbox.NetworkAllow},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	// Loopback should always be reachable.
	res := sb.Run("ping -c1 -W1 127.0.0.1 2>&1 || echo 'loopback unreachable'")
	if strings.Contains(res.Stdout, "loopback unreachable") {
		t.Error("loopback should be reachable in Allow mode")
	}
}

// TestPhase7_DenyModeLinux verifies that external network access is blocked in
// Deny mode on Linux via CLONE_NEWNET.
func TestPhase7_DenyModeLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("NetworkDeny uses CLONE_NEWNET — Linux only")
	}

	sb, err := sandbox.New(sandbox.Options{
		Network: sandbox.NetworkPolicy{Mode: sandbox.NetworkDeny},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	// An external TCP connection should fail with no network interface.
	res := sb.Run("curl --connect-timeout 2 -s https://example.com 2>&1; echo exit:$?")
	if !strings.Contains(res.Stdout, "exit:") {
		t.Fatal("unexpected output:", res.Stdout)
	}
	// Exit code must be non-zero (curl network failure).
	if res.ExitCode == 0 {
		t.Error("expected curl to fail in Deny mode; got exit 0")
	}
}

// TestPhase7_DenyModeLoopback confirms that loopback (127.0.0.1) is still
// accessible inside a deny-mode sandbox on Linux (the lo interface is
// automatically present even in an isolated net namespace).
func TestPhase7_DenyModeLoopback(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("NetworkDeny uses CLONE_NEWNET — Linux only")
	}

	sb, err := sandbox.New(sandbox.Options{
		Network: sandbox.NetworkPolicy{Mode: sandbox.NetworkDeny},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	res := sb.Run("ping -c1 -W1 127.0.0.1 2>&1; echo exit:$?")
	if strings.Contains(res.Stdout, "exit:1") {
		t.Error("loopback should be reachable inside deny-mode sandbox")
	}
}

// TestPhase7_DenyModeDegradationNonLinux confirms that on non-Linux platforms
// Deny mode does not panic and does not prevent normal command execution.
func TestPhase7_DenyModeDegradationNonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("degradation test only runs on non-Linux")
	}

	sb, err := sandbox.New(sandbox.Options{
		Network: sandbox.NetworkPolicy{Mode: sandbox.NetworkDeny},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	res := sb.Run("echo hello")
	if res.ExitCode != 0 {
		t.Errorf("basic command failed after Deny degradation: %v", res.Error)
	}
	if strings.TrimSpace(res.Stdout) != "hello" {
		t.Errorf("unexpected stdout: %q", res.Stdout)
	}
}

// TestPhase7_AllowlistDegradesToDenyLinux verifies that on Linux, Allowlist
// mode currently degrades to full deny (CLONE_NEWNET).
func TestPhase7_AllowlistDegradesToDenyLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Allowlist degradation check — Linux only")
	}

	sb, err := sandbox.New(sandbox.Options{
		Network: sandbox.NetworkPolicy{
			Mode:      sandbox.NetworkAllowlist,
			Allowlist: []string{"pypi.org"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	// External traffic should still be blocked (degraded to deny).
	res := sb.Run("curl --connect-timeout 2 -s https://example.com 2>&1; echo exit:$?")
	if res.ExitCode == 0 {
		t.Error("expected curl to fail in Allowlist(→Deny) mode; got exit 0")
	}
}

// TestPhase7_FilterAvailable confirms that NewAllow() is always available and
// that NewDeny()/NewAllowlist() report correctly for the current platform.
func TestPhase7_FilterAvailable(t *testing.T) {
	if !network.NewAllow().Available() {
		t.Error("NewAllow() should always report Available() == true")
	}

	denyAvailable := network.NewDeny().Available()
	if runtime.GOOS == "linux" && !denyAvailable {
		t.Error("NewDeny() should be available on Linux")
	}
	if runtime.GOOS != "linux" && denyAvailable {
		t.Error("NewDeny() should not be available on non-Linux")
	}
}
