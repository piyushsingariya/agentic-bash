//go:build integration

// Package integration contains end-to-end tests for the full sandbox lifecycle.
// Run with: go test -tags integration ./integration/...
//
// These tests exercise real command execution, filesystem isolation, network
// policy, resource limits, snapshot/restore, and concurrent session safety.
// Some tests are Linux-only (cgroups, network namespaces) and are skipped on
// macOS automatically.
package integration

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	sbfs "github.com/piyushsingariya/agentic-bash/fs"
	"github.com/piyushsingariya/agentic-bash/sandbox"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newSandbox(t *testing.T, opts sandbox.Options) *sandbox.Sandbox {
	t.Helper()
	sb, err := sandbox.New(opts)
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })
	return sb
}

func mustRun(t *testing.T, sb *sandbox.Sandbox, cmd string) sandbox.ExecutionResult {
	t.Helper()
	r := sb.Run(cmd)
	if r.Error != nil {
		t.Fatalf("Run(%q) infrastructure error: %v", cmd, r.Error)
	}
	return r
}

// ── basic lifecycle ───────────────────────────────────────────────────────────

func TestEchoHello(t *testing.T) {
	sb := newSandbox(t, sandbox.Options{})
	r := mustRun(t, sb, `echo hello`)
	if strings.TrimSpace(r.Stdout) != "hello" {
		t.Errorf("want %q, got %q", "hello", r.Stdout)
	}
	if r.ExitCode != 0 {
		t.Errorf("want exit 0, got %d", r.ExitCode)
	}
}

func TestNonZeroExitCode(t *testing.T) {
	sb := newSandbox(t, sandbox.Options{})
	r := sb.Run("exit 42")
	if r.ExitCode != 42 {
		t.Errorf("want exit 42, got %d", r.ExitCode)
	}
	if r.Error != nil {
		t.Errorf("non-zero exit should not set Error; got %v", r.Error)
	}
}

func TestSessionPersistence(t *testing.T) {
	sb := newSandbox(t, sandbox.Options{})
	mustRun(t, sb, `export MYVAR=hello`)
	r := mustRun(t, sb, `echo $MYVAR`)
	if strings.TrimSpace(r.Stdout) != "hello" {
		t.Errorf("env var not persisted across runs; got %q", r.Stdout)
	}
}

func TestCwdPersistence(t *testing.T) {
	sb := newSandbox(t, sandbox.Options{})
	mustRun(t, sb, `mkdir -p /tmp/integration-cwd-test && cd /tmp/integration-cwd-test`)
	r := mustRun(t, sb, `pwd`)
	if !strings.Contains(r.Stdout, "integration-cwd-test") {
		t.Errorf("cwd not persisted; got %q", r.Stdout)
	}
}

func TestFunctionPersistence(t *testing.T) {
	sb := newSandbox(t, sandbox.Options{})
	mustRun(t, sb, `greet() { echo "hi $1"; }`)
	r := mustRun(t, sb, `greet world`)
	if strings.TrimSpace(r.Stdout) != "hi world" {
		t.Errorf("function not persisted; got %q", r.Stdout)
	}
}

// ── timeout ───────────────────────────────────────────────────────────────────

func TestTimeoutKillsProcess(t *testing.T) {
	sb := newSandbox(t, sandbox.Options{
		Limits: sandbox.ResourceLimits{Timeout: 300 * time.Millisecond},
	})
	start := time.Now()
	r := sb.Run(`sleep 60`)
	elapsed := time.Since(start)

	if r.Error == nil {
		t.Error("expected timeout error, got nil")
	}
	// Must be killed well before the 60s sleep completes.
	if elapsed > 5*time.Second {
		t.Errorf("timeout took too long: %s", elapsed)
	}
}

// ── output cap ────────────────────────────────────────────────────────────────

func TestOutputCapTruncates(t *testing.T) {
	sb := newSandbox(t, sandbox.Options{
		Limits: sandbox.ResourceLimits{
			Timeout:     10 * time.Second,
			MaxOutputMB: 1, // 1 MiB cap
		},
	})
	// yes produces infinite output; should be killed by the output cap.
	r := sb.Run(`yes`)
	if r.Error == nil && r.ExitCode == 0 {
		t.Error("expected output cap to kill process, but it exited cleanly")
	}
	combined := len(r.Stdout) + len(r.Stderr)
	if combined > 2*1024*1024 {
		t.Errorf("output %d bytes exceeds expected cap", combined)
	}
}

// ── filesystem isolation ──────────────────────────────────────────────────────

func TestFilesystemWritesStayInSandbox(t *testing.T) {
	sb := newSandbox(t, sandbox.Options{})
	root := sb.State().Cwd

	// Writes inside the sandbox root must succeed.
	r := mustRun(t, sb, fmt.Sprintf(`echo "sandbox content" > %s/inside.txt`, root))
	if r.ExitCode != 0 {
		t.Fatalf("write inside sandbox root failed: %v / %s", r.Error, r.Stderr)
	}

	// Writes outside the sandbox root must be rejected (OsFS path containment).
	r2 := sb.Run(`echo "escape" > /etc/sb-escape-test.txt`)
	if r2.ExitCode == 0 {
		t.Error("write outside sandbox root should have failed, but exited 0")
	}
}

func TestChangeTrackerRecordsCreates(t *testing.T) {
	sb := newSandbox(t, sandbox.Options{})
	root := sb.State().Cwd
	cmd := fmt.Sprintf("echo hello > %s/tracker-test.txt", root)
	r := mustRun(t, sb, cmd)
	if r.ExitCode != 0 {
		t.Fatalf("write failed: %v / %s", r.Error, r.Stderr)
	}
	if len(r.FilesCreated) == 0 {
		t.Error("expected FilesCreated to contain the new file")
	}
}

// ── file transfer API ─────────────────────────────────────────────────────────

func TestWriteAndReadFile(t *testing.T) {
	sb := newSandbox(t, sandbox.Options{})
	root := sb.State().Cwd
	path := root + "/hello.txt"
	content := []byte("Hello from WriteFile!")

	if err := sb.WriteFile(path, content); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := sb.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("ReadFile returned %q, want %q", got, content)
	}
}

func TestListFiles(t *testing.T) {
	sb := newSandbox(t, sandbox.Options{})
	root := sb.State().Cwd

	_ = sb.WriteFile(root+"/a.txt", []byte("a"))
	_ = sb.WriteFile(root+"/b.txt", []byte("b"))

	infos, err := sb.ListFiles(root)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	names := make(map[string]bool)
	for _, fi := range infos {
		names[fi.Name] = true
	}
	if !names["a.txt"] || !names["b.txt"] {
		t.Errorf("ListFiles missing expected entries; got %v", infos)
	}
}

func TestUploadAndDownloadTar(t *testing.T) {
	src := newSandbox(t, sandbox.Options{})
	srcRoot := src.State().Cwd

	_ = src.WriteFile(srcRoot+"/upload.txt", []byte("tar content"))
	mustRun(t, src, `mkdir -p `+srcRoot+`/subdir && echo nested > `+srcRoot+`/subdir/file.txt`)

	// Download from src.
	var buf bytes.Buffer
	if err := src.DownloadTar(&buf); err != nil {
		t.Fatalf("DownloadTar: %v", err)
	}

	// Upload into a fresh sandbox.
	dst := newSandbox(t, sandbox.Options{})
	if err := dst.UploadTar(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("UploadTar: %v", err)
	}

	dstRoot := dst.State().Cwd
	got, err := dst.ReadFile(dstRoot + "/upload.txt")
	if err != nil {
		t.Fatalf("ReadFile after upload: %v", err)
	}
	if string(got) != "tar content" {
		t.Errorf("got %q, want %q", got, "tar content")
	}
}

// ── snapshot / restore ────────────────────────────────────────────────────────

func TestSnapshotRestore(t *testing.T) {
	sb := newSandbox(t, sandbox.Options{})
	root := sb.State().Cwd

	mustRun(t, sb, fmt.Sprintf(`echo "persisted" > %s/snap.txt`, root))

	snap, err := sbfs.Snapshot(sb.FS())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Mutate after snapshot.
	mustRun(t, sb, fmt.Sprintf(`rm %s/snap.txt && echo "new" > %s/other.txt`, root, root))

	// Restore.
	if err := sbfs.Restore(sb.FS(), snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	got, err := sb.ReadFile(root + "/snap.txt")
	if err != nil {
		t.Fatalf("ReadFile after restore: %v", err)
	}
	if !strings.Contains(string(got), "persisted") {
		t.Errorf("got %q after restore, want 'persisted'", got)
	}
	// The post-snapshot file should be gone.
	if _, err := sb.ReadFile(root + "/other.txt"); err == nil {
		t.Error("other.txt should not exist after restore")
	}
}

// ── Reset ─────────────────────────────────────────────────────────────────────

func TestReset(t *testing.T) {
	sb := newSandbox(t, sandbox.Options{})
	mustRun(t, sb, `export MYVAR=set`)
	sb.Reset()
	r := mustRun(t, sb, `echo ${MYVAR:-unset}`)
	if strings.TrimSpace(r.Stdout) != "unset" {
		t.Errorf("MYVAR survived Reset; got %q", r.Stdout)
	}
}

// ── RunStream ─────────────────────────────────────────────────────────────────

func TestRunStream(t *testing.T) {
	sb := newSandbox(t, sandbox.Options{})
	var outBuf, errBuf bytes.Buffer
	exitCode, err := sb.RunStream(context.Background(), `echo streaming`, &outBuf, &errBuf)
	if err != nil {
		t.Fatalf("RunStream error: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("want exit 0, got %d", exitCode)
	}
	if !strings.Contains(outBuf.String(), "streaming") {
		t.Errorf("output %q does not contain 'streaming'", outBuf.String())
	}
}

func TestRunStreamStatePersists(t *testing.T) {
	sb := newSandbox(t, sandbox.Options{})
	var buf bytes.Buffer
	_, _ = sb.RunStream(context.Background(), `export STREAM_VAR=yes`, &buf, &buf)
	r := mustRun(t, sb, `echo $STREAM_VAR`)
	if strings.TrimSpace(r.Stdout) != "yes" {
		t.Errorf("env var set via RunStream not visible in Run; got %q", r.Stdout)
	}
}

// ── Pool ──────────────────────────────────────────────────────────────────────

func TestPoolPrewarms(t *testing.T) {
	pool := sandbox.NewPool(sandbox.PoolOptions{MinSize: 3, MaxSize: 5})
	defer pool.Close()

	if pool.Size() < 3 {
		t.Errorf("pool size %d < MinSize 3 after NewPool", pool.Size())
	}
}

func TestPoolAcquireRelease(t *testing.T) {
	pool := sandbox.NewPool(sandbox.PoolOptions{MinSize: 1, MaxSize: 3})
	defer pool.Close()

	sb, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	r := sb.Run(`echo pooled`)
	if r.ExitCode != 0 {
		t.Errorf("unexpected exit %d", r.ExitCode)
	}
	pool.Release(sb)
	if pool.Size() < 1 {
		t.Error("pool size dropped to 0 after release")
	}
}

func TestPoolConcurrent(t *testing.T) {
	const workers = 20
	pool := sandbox.NewPool(sandbox.PoolOptions{MinSize: 2, MaxSize: workers})
	defer pool.Close()

	var wg sync.WaitGroup
	errs := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sb, err := pool.Acquire(context.Background())
			if err != nil {
				errs <- fmt.Errorf("worker %d Acquire: %w", id, err)
				return
			}
			r := sb.Run(fmt.Sprintf(`echo worker-%d`, id))
			pool.Release(sb)
			if !strings.Contains(r.Stdout, fmt.Sprintf("worker-%d", id)) {
				errs <- fmt.Errorf("worker %d: unexpected output %q", id, r.Stdout)
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestPoolResetOnRelease(t *testing.T) {
	pool := sandbox.NewPool(sandbox.PoolOptions{MinSize: 1, MaxSize: 2})
	defer pool.Close()

	sb, _ := pool.Acquire(context.Background())
	sb.Run(`export DIRTY=yes`)
	pool.Release(sb)

	sb2, _ := pool.Acquire(context.Background())
	defer pool.Release(sb2)
	r := sb2.Run(`echo ${DIRTY:-clean}`)
	if strings.TrimSpace(r.Stdout) != "clean" {
		t.Errorf("sandbox not reset on Release; DIRTY=%q", r.Stdout)
	}
}

func TestPoolIdleEviction(t *testing.T) {
	ttl := 200 * time.Millisecond
	pool := sandbox.NewPool(sandbox.PoolOptions{
		MinSize: 2,
		MaxSize: 4,
		IdleTTL: ttl,
	})
	defer pool.Close()

	initialSize := pool.Size()
	// The eviction ticker fires at max(ttl/2, 1s). Wait well past 1s so the
	// loop has fired at least once and all idle sandboxes are older than ttl.
	time.Sleep(2 * time.Second)

	if pool.Size() >= initialSize {
		t.Errorf("expected idle sandboxes to be evicted; size %d → %d", initialSize, pool.Size())
	}
}

func TestPoolClosedAcquireFails(t *testing.T) {
	pool := sandbox.NewPool(sandbox.PoolOptions{MaxSize: 2})
	pool.Close()

	_, err := pool.Acquire(context.Background())
	if err == nil {
		t.Error("Acquire on closed pool should return error")
	}
}

// ── network deny (Linux only) ─────────────────────────────────────────────────

func TestNetworkDenyBlocksExternal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("network namespaces not available on " + runtime.GOOS)
	}
	sb := newSandbox(t, sandbox.Options{
		Network: sandbox.NetworkPolicy{Mode: sandbox.NetworkDeny},
		Limits:  sandbox.ResourceLimits{Timeout: 5 * time.Second},
	})
	r := sb.Run(`curl -s --max-time 2 https://example.com`)
	if r.ExitCode == 0 {
		t.Error("curl to external host should fail under NetworkDeny, but exited 0")
	}
}

// ── graceful degradation on macOS ─────────────────────────────────────────────

func TestMacOSGracefulDegradation(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS-specific test")
	}
	// IsolationAuto must not panic on macOS — it falls back to Noop.
	sb := newSandbox(t, sandbox.Options{Isolation: sandbox.IsolationAuto})
	r := mustRun(t, sb, `echo ok`)
	if strings.TrimSpace(r.Stdout) != "ok" {
		t.Errorf("got %q, want ok", r.Stdout)
	}
}
