package sbfs_test

import (
	"os"
	"testing"

	sbfs "github.com/piyushsingariya/agentic-bash/fs"
)

// ---- helpers ----------------------------------------------------------------

func newLayered(t *testing.T) (*sbfs.LayeredFS, string) {
	t.Helper()
	root := t.TempDir()
	return sbfs.NewLayeredFS(root, ""), root
}

// ---- MemoryFS ---------------------------------------------------------------

func TestMemoryFS_WriteRead(t *testing.T) {
	root := t.TempDir()
	m := sbfs.NewMemoryFS(root)

	path := root + "/hello.txt"
	if err := m.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	data, err := m.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("want 'hello', got %q", data)
	}
}

func TestMemoryFS_PathEscape(t *testing.T) {
	root := t.TempDir()
	m := sbfs.NewMemoryFS(root)

	_, err := m.ReadFile(root + "/../etc/passwd")
	if err == nil {
		t.Error("expected error for path escaping sandbox root")
	}
}

func TestMemoryFS_MkdirAll(t *testing.T) {
	root := t.TempDir()
	m := sbfs.NewMemoryFS(root)

	dir := root + "/a/b/c"
	if err := m.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	info, err := m.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected a directory")
	}
}

// ---- LayeredFS --------------------------------------------------------------

func TestLayeredFS_WriteAndRead(t *testing.T) {
	lfs, root := newLayered(t)

	path := root + "/note.txt"
	if err := lfs.WriteFile(path, []byte("layered"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := lfs.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "layered" {
		t.Errorf("want 'layered', got %q", got)
	}
}

func TestLayeredFS_WriteGoesToUpperOnly(t *testing.T) {
	lfs, root := newLayered(t)

	path := root + "/upper.txt"
	if err := lfs.WriteFile(path, []byte("upper"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// The file must be accessible via the upper layer directly.
	if _, err := lfs.Upper().Stat(path); err != nil {
		t.Errorf("file should exist in upper layer: %v", err)
	}
}

func TestLayeredFS_WriteOutsideRootBlocked(t *testing.T) {
	lfs, root := newLayered(t)

	outside := root + "/../outside.txt"
	err := lfs.WriteFile(outside, []byte("bad"), 0o644)
	if err == nil {
		t.Error("expected error writing outside sandbox root")
	}
}

func TestLayeredFS_OpenFileWriteOutsideRootBlocked(t *testing.T) {
	lfs, root := newLayered(t)

	outside := root + "/../evil.txt"
	_, err := lfs.OpenFile(outside, os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		t.Error("expected error for write outside sandbox root via OpenFile")
	}
}

func TestLayeredFS_BaseDir_PrePopulated(t *testing.T) {
	// Create a real directory to serve as the base image.
	baseDir := t.TempDir()
	if err := os.WriteFile(baseDir+"/base.txt", []byte("from-base"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	root := t.TempDir()
	lfs := sbfs.NewLayeredFS(root, baseDir)

	// The base file should be visible inside the sandbox root.
	got, err := lfs.ReadFile(root + "/base.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "from-base" {
		t.Errorf("want 'from-base', got %q", got)
	}
}

func TestLayeredFS_PersistsAcrossMultipleWrites(t *testing.T) {
	lfs, root := newLayered(t)

	for i, content := range []string{"one", "two", "three"} {
		path := root + "/f.txt"
		if err := lfs.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		got, err := lfs.ReadFile(path)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if string(got) != content {
			t.Errorf("write %d: want %q, got %q", i, content, got)
		}
	}
}

// ---- ChangeTracker ----------------------------------------------------------

func TestChangeTracker_Created(t *testing.T) {
	lfs, root := newLayered(t)
	tracker := sbfs.NewChangeTracker(lfs)

	path := root + "/new.txt"
	if err := tracker.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if !containsPath(tracker.FilesCreated(), path) {
		t.Errorf("expected %s in FilesCreated, got %v", path, tracker.FilesCreated())
	}
	if len(tracker.FilesModified()) != 0 {
		t.Errorf("expected no modified files, got %v", tracker.FilesModified())
	}
}

func TestChangeTracker_Modified(t *testing.T) {
	lfs, root := newLayered(t)
	// Pre-create the file so it exists before the tracker run.
	path := root + "/existing.txt"
	if err := lfs.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatalf("setup WriteFile: %v", err)
	}

	tracker := sbfs.NewChangeTracker(lfs)
	if err := tracker.WriteFile(path, []byte("v2"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if !containsPath(tracker.FilesModified(), path) {
		t.Errorf("expected %s in FilesModified, got %v", path, tracker.FilesModified())
	}
	if len(tracker.FilesCreated()) != 0 {
		t.Errorf("expected no created files, got %v", tracker.FilesCreated())
	}
}

func TestChangeTracker_Deleted(t *testing.T) {
	lfs, root := newLayered(t)
	path := root + "/del.txt"
	if err := lfs.WriteFile(path, []byte("bye"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	tracker := sbfs.NewChangeTracker(lfs)
	if err := tracker.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if !containsPath(tracker.FilesDeleted(), path) {
		t.Errorf("expected %s in FilesDeleted, got %v", path, tracker.FilesDeleted())
	}
}

func TestChangeTracker_Reset(t *testing.T) {
	lfs, root := newLayered(t)
	tracker := sbfs.NewChangeTracker(lfs)

	if err := tracker.WriteFile(root+"/f.txt", []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	tracker.Reset()

	if len(tracker.FilesCreated()) != 0 || len(tracker.FilesModified()) != 0 || len(tracker.FilesDeleted()) != 0 {
		t.Error("expected all change sets to be empty after Reset()")
	}
}

// ---- Snapshot / Restore -----------------------------------------------------

func TestSnapshotRestore_RoundTrip(t *testing.T) {
	lfs, root := newLayered(t)

	if err := lfs.WriteFile(root+"/a.txt", []byte("alpha"), 0o644); err != nil {
		t.Fatalf("WriteFile a: %v", err)
	}
	if err := lfs.MkdirAll(root+"/sub", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := lfs.WriteFile(root+"/sub/b.txt", []byte("beta"), 0o644); err != nil {
		t.Fatalf("WriteFile b: %v", err)
	}

	snap, err := sbfs.Snapshot(lfs)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Mutate after snapshotting.
	if err := lfs.WriteFile(root+"/a.txt", []byte("mutated"), 0o644); err != nil {
		t.Fatalf("WriteFile mutate: %v", err)
	}

	// Restore should bring back the original state.
	if err := sbfs.Restore(lfs, snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	got, err := lfs.ReadFile(root + "/a.txt")
	if err != nil {
		t.Fatalf("ReadFile after restore: %v", err)
	}
	if string(got) != "alpha" {
		t.Errorf("want 'alpha' after restore, got %q", got)
	}

	got2, err := lfs.ReadFile(root + "/sub/b.txt")
	if err != nil {
		t.Fatalf("ReadFile sub after restore: %v", err)
	}
	if string(got2) != "beta" {
		t.Errorf("want 'beta' after restore, got %q", got2)
	}
}

func TestSnapshot_EmptyFS(t *testing.T) {
	lfs, _ := newLayered(t)
	snap, err := sbfs.Snapshot(lfs)
	if err != nil {
		t.Fatalf("Snapshot of empty FS: %v", err)
	}
	if snap == nil {
		t.Error("expected non-nil snapshot")
	}
}

// ---- OsFS symlink containment -----------------------------------------------

func TestOsFS_Symlink_AbsoluteEscape_Blocked(t *testing.T) {
	root := t.TempDir()
	ofs := sbfs.NewOsFS(root)

	linkPath := root + "/hostlink"
	err := ofs.Symlink("/etc/passwd", linkPath)
	if err == nil {
		t.Fatal("expected error creating symlink to /etc/passwd, got nil")
	}
}

func TestOsFS_Symlink_RelativeEscape_Blocked(t *testing.T) {
	root := t.TempDir()
	ofs := sbfs.NewOsFS(root)

	// Create deep directory so the relative escape needs several ../
	if err := os.MkdirAll(root+"/home/user", 0o755); err != nil {
		t.Fatal(err)
	}
	linkPath := root + "/home/user/link"
	// ../../../etc/passwd goes above root
	err := ofs.Symlink("../../../etc/passwd", linkPath)
	if err == nil {
		t.Fatal("expected error for relative symlink escaping sandbox root, got nil")
	}
}

func TestOsFS_Symlink_ValidAbsolute_Allowed(t *testing.T) {
	root := t.TempDir()
	ofs := sbfs.NewOsFS(root)

	// Create a real file inside the sandbox to link to.
	if err := os.WriteFile(root+"/real.txt", []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := root + "/link.txt"
	if err := ofs.Symlink(root+"/real.txt", linkPath); err != nil {
		t.Fatalf("unexpected error for valid absolute symlink: %v", err)
	}
}

func TestOsFS_Symlink_ValidRelative_Allowed(t *testing.T) {
	root := t.TempDir()
	ofs := sbfs.NewOsFS(root)

	if err := os.WriteFile(root+"/target.txt", []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := root + "/link.txt"
	if err := ofs.Symlink("target.txt", linkPath); err != nil {
		t.Fatalf("unexpected error for valid relative symlink: %v", err)
	}
}

func TestOsFS_ReadFile_ThroughSymlinkEscape_Blocked(t *testing.T) {
	root := t.TempDir()

	// Plant a symlink on disk that points outside the sandbox, bypassing
	// OsFS.Symlink (simulates a symlink created before the fix or via another path).
	linkPath := root + "/hostlink"
	if err := os.Symlink("/etc/passwd", linkPath); err != nil {
		t.Skip("cannot create raw symlink:", err)
	}

	ofs := sbfs.NewOsFS(root)
	_, err := ofs.ReadFile(linkPath)
	// The containment check in OsFS.check catches the cleaned path (the symlink
	// itself is within root), but openat2InRoot (on Linux 5.6+) blocks the
	// follow; on other platforms the path-only check passes but the read of the
	// actual /etc/passwd content should be the OS's own permission enforcement.
	// We only assert that the call does NOT silently succeed with host content.
	if err == nil {
		data, _ := ofs.ReadFile(linkPath)
		if len(data) > 0 {
			t.Error("ReadFile through escaping symlink returned content — sandbox escape active")
		}
	}
}

// ---- helpers ----------------------------------------------------------------

func containsPath(paths []string, target string) bool {
	for _, p := range paths {
		if p == target {
			return true
		}
	}
	return false
}
