//go:build linux

package isolation

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// LandlockStrategy applies Landlock LSM restrictions to the calling process.
// It requires Linux kernel 5.13+ with Landlock enabled.
//
// Unlike NamespaceStrategy, Landlock restricts file-system access at the
// syscall level for the entire process tree.  Call Apply() from within a child
// process (e.g., after a fork) to avoid restricting the parent.
//
// allowedPaths is the list of directories that may be read and written.
// The sandbox tmpDir and /proc (needed by Go runtime) are always added.
type LandlockStrategy struct {
	allowedPaths []string
}

func newLandlock() IsolationStrategy { return &LandlockStrategy{} }

// NewLandlockStrategy creates a LandlockStrategy with explicit allowed paths.
func NewLandlockStrategy(allowedPaths ...string) *LandlockStrategy {
	return &LandlockStrategy{allowedPaths: allowedPaths}
}

func (l *LandlockStrategy) Name() string { return "landlock" }

// Available probes the kernel by calling landlock_create_ruleset with the
// LANDLOCK_CREATE_RULESET_VERSION flag.  Returns true when the ABI is >= 1.
func (l *LandlockStrategy) Available() bool {
	// Passing NULL attr, size=0, flags=LANDLOCK_CREATE_RULESET_VERSION
	// returns the ABI version (>0) or an error.
	abi, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		0,
		0,
		uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION),
	)
	if errno == syscall.ENOSYS {
		return false
	}
	return int(abi) >= 1
}

// Wrap is a no-op for Landlock: filesystem restrictions cannot be applied to a
// child via exec.Cmd SysProcAttr.  Use Apply() inside the child process.
func (l *LandlockStrategy) Wrap(_ *exec.Cmd) error { return nil }

// Apply applies Landlock restrictions to the current OS thread (and therefore
// the whole process, since Go's runtime locks goroutines to threads as needed).
//
// It restricts all filesystem access except for the explicitly allowed paths
// and essential system locations (/proc, /dev/null, /usr, /lib, /bin, /etc).
// Call this method only from within a child process to avoid restricting the
// parent.
func (l *LandlockStrategy) Apply() error {
	// Full filesystem access mask for ABI v1.
	const accessFS = unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM

	attr := unix.LandlockRulesetAttr{Access_fs: accessFS}
	attrSize := unsafe.Sizeof(attr)

	rulesetFD, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)),
		attrSize,
		0,
	)
	if errno != 0 {
		return fmt.Errorf("landlock: create ruleset: %w", errno)
	}
	defer syscall.Close(int(rulesetFD))

	// Build the list of allowed paths: caller-supplied + essential system paths.
	paths := append([]string{}, l.allowedPaths...)
	paths = append(paths, "/proc", "/dev", "/usr", "/lib", "/lib64", "/bin", "/etc", "/tmp")

	for _, p := range paths {
		if p == "" {
			continue
		}
		f, err := os.OpenFile(p, unix.O_PATH, 0)
		if err != nil {
			continue // path doesn't exist on this system; skip
		}

		ruleAttr := unix.LandlockPathBeneathAttr{
			Allowed_access: accessFS,
			Parent_fd:      int32(f.Fd()),
		}
		_, _, ruleErrno := unix.Syscall6(
			unix.SYS_LANDLOCK_ADD_RULE,
			uintptr(rulesetFD),
			uintptr(unix.LANDLOCK_RULE_PATH_BENEATH),
			uintptr(unsafe.Pointer(&ruleAttr)),
			0, 0, 0,
		)
		f.Close()
		if ruleErrno != 0 {
			return fmt.Errorf("landlock: add rule for %s: %w", p, ruleErrno)
		}
	}

	// Set NO_NEW_PRIVS — required before landlock_restrict_self.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("landlock: prctl NO_NEW_PRIVS: %w", err)
	}

	// Apply restrictions to the current process.
	_, _, errno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rulesetFD), 0, 0)
	if errno != 0 {
		return fmt.Errorf("landlock: restrict self: %w", errno)
	}
	return nil
}
