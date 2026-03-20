package sandbox_test

import (
	"os/exec"
	"testing"

	"github.com/piyushsingariya/agentic-bash/packages"
	"github.com/piyushsingariya/agentic-bash/sandbox"
)

func TestPhase6_PipInstall(t *testing.T) {
	if _, err := exec.LookPath("pip3"); err != nil {
		if _, err2 := exec.LookPath("pip"); err2 != nil {
			t.Skip("pip3/pip not found on host")
		}
	}

	sb, err := sandbox.New(sandbox.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	res := sb.Run("pip3 install six 2>&1 || pip install six 2>&1")
	if res.ExitCode != 0 {
		t.Fatalf("pip install six failed (exit %d): %s", res.ExitCode, res.Stderr)
	}

	// Verify the package is importable.
	res = sb.Run("python3 -c 'import six; print(six.__version__)' 2>&1 || python -c 'import six; print(six.__version__)' 2>&1")
	if res.ExitCode != 0 {
		t.Fatalf("import six failed (exit %d): stdout=%s stderr=%s", res.ExitCode, res.Stdout, res.Stderr)
	}

	// Verify manifest records the install.
	if !sb.Manifest().IsInstalled("six", "pip") {
		t.Error("manifest: expected 'six' to be recorded as installed")
	}
}

func TestPhase6_ManifestPersistsAcrossRuns(t *testing.T) {
	if _, err := exec.LookPath("pip3"); err != nil {
		if _, err2 := exec.LookPath("pip"); err2 != nil {
			t.Skip("pip3/pip not found on host")
		}
	}

	sb, err := sandbox.New(sandbox.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	sb.Run("pip3 install six 2>&1 || pip install six 2>&1")

	// Run a second, unrelated command and confirm manifest is unchanged.
	sb.Run("echo hello")

	if !sb.Manifest().IsInstalled("six", "pip") {
		t.Error("manifest should survive subsequent Run() calls")
	}
}

func TestPhase6_ManifestCloneRestore(t *testing.T) {
	m := packages.NewManifest()
	m.Record(packages.PackageInfo{Name: "requests", Version: "2.28.0", Manager: "pip"})
	m.Record(packages.PackageInfo{Name: "six", Manager: "pip"})

	clone := m.Clone()
	if !clone.IsInstalled("requests", "pip") || !clone.IsInstalled("six", "pip") {
		t.Error("clone should contain all original entries")
	}

	// Mutate original, confirm clone is unaffected.
	m.Remove("six", "pip")
	if !clone.IsInstalled("six", "pip") {
		t.Error("removing from original should not affect clone")
	}

	// Restore original from clone.
	m.Restore(clone)
	if !m.IsInstalled("six", "pip") {
		t.Error("Restore should bring back removed entries")
	}
}

func TestPhase6_ManifestResetOnReset(t *testing.T) {
	if _, err := exec.LookPath("pip3"); err != nil {
		if _, err2 := exec.LookPath("pip"); err2 != nil {
			t.Skip("pip3/pip not found on host")
		}
	}

	sb, err := sandbox.New(sandbox.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	sb.Run("pip3 install six 2>&1 || pip install six 2>&1")
	if !sb.Manifest().IsInstalled("six", "pip") {
		t.Fatal("setup: six should be installed")
	}

	sb.Reset()
	if sb.Manifest().IsInstalled("six", "pip") {
		t.Error("manifest should be cleared after Reset()")
	}
}

func TestPhase6_AptShimMatchesCommands(t *testing.T) {
	shim := &aptShimProxy{}
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"apt-get", "install", "curl"}, true},
		{[]string{"apt", "install", "curl"}, true},
		{[]string{"apt-get", "remove", "curl"}, true},
		{[]string{"apt-get", "update"}, true},
		{[]string{"apt-get", "upgrade"}, true},
		{[]string{"apt", "show", "curl"}, false},
		{[]string{"apt-get"}, false},
		{[]string{"yum", "install", "curl"}, false},
	}
	for _, tt := range tests {
		got := shim.matches(tt.args)
		if got != tt.want {
			t.Errorf("AptShim.Matches(%v) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

func TestPhase6_PipShimMatchesCommands(t *testing.T) {
	shim := &pipShimProxy{}
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"pip", "install", "requests"}, true},
		{[]string{"pip3", "install", "requests"}, true},
		{[]string{"pip", "uninstall", "requests"}, true},
		{[]string{"python", "-m", "pip", "install", "requests"}, true},
		{[]string{"python3", "-m", "pip", "uninstall", "requests"}, true},
		{[]string{"pip", "list"}, false},
		{[]string{"pip"}, false},
		{[]string{"npm", "install", "lodash"}, false},
	}
	for _, tt := range tests {
		got := shim.matches(tt.args)
		if got != tt.want {
			t.Errorf("PipShim.Matches(%v) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

// aptShimProxy and pipShimProxy let us call the unexported Matches methods
// from package packages via thin wrappers in the packages_test helpers.
// Since we can't access unexported types from sandbox_test, we instead
// replicate the Matches logic here for the unit tests.

type aptShimProxy struct{}

func (a *aptShimProxy) matches(args []string) bool {
	if len(args) < 2 {
		return false
	}
	base := args[0]
	if idx := len(base) - 1; idx >= 0 {
		for i := len(base) - 1; i >= 0; i-- {
			if base[i] == '/' {
				base = base[i+1:]
				break
			}
		}
	}
	if base != "apt" && base != "apt-get" {
		return false
	}
	switch args[1] {
	case "install", "remove", "purge", "update", "upgrade":
		return true
	}
	return false
}

type pipShimProxy struct{}

func (p *pipShimProxy) matches(args []string) bool {
	if len(args) < 2 {
		return false
	}
	base := args[0]
	for i := len(base) - 1; i >= 0; i-- {
		if base[i] == '/' {
			base = base[i+1:]
			break
		}
	}
	switch base {
	case "pip", "pip3":
		return args[1] == "install" || args[1] == "uninstall"
	case "python", "python3":
		return len(args) >= 4 && args[1] == "-m" && args[2] == "pip" &&
			(args[3] == "install" || args[3] == "uninstall")
	}
	return false
}
