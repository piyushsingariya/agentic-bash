package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
)

// bootstrapFS creates a Linux-like directory skeleton and synthetic config
// files inside root (the sandbox's real temp directory).
func bootstrapFS(root string, cfg BootstrapConfig) error {
	type dirSpec struct {
		path string
		mode os.FileMode
	}
	dirs := []dirSpec{
		{filepath.Join(root, "bin"), 0o755},
		{filepath.Join(root, "etc"), 0o755},
		{filepath.Join(root, "home", cfg.UserName), 0o755},
		{filepath.Join(root, "lib"), 0o755},
		{filepath.Join(root, "lib64"), 0o755},
		{filepath.Join(root, "tmp"), 0o1777},
		{filepath.Join(root, "usr"), 0o755},
		{filepath.Join(root, "usr", "bin"), 0o755},
		{filepath.Join(root, "usr", "lib"), 0o755},
		{filepath.Join(root, "usr", "local"), 0o755},
		{filepath.Join(root, "usr", "local", "bin"), 0o755},
		{filepath.Join(root, "usr", "local", "lib"), 0o755},
		{filepath.Join(root, "usr", "sbin"), 0o755},
		{filepath.Join(root, "var"), 0o755},
		{filepath.Join(root, "var", "log"), 0o755},
		{filepath.Join(root, "var", "tmp"), 0o755},
		{filepath.Join(root, "workspace"), 0o755},
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			return fmt.Errorf("bootstrap: mkdir %s: %w", d.path, err)
		}
		// Reapply mode because MkdirAll skips chmod when the dir already exists.
		_ = os.Chmod(d.path, d.mode)
	}

	files := []struct {
		path    string
		content string
	}{
		{
			filepath.Join(root, "etc", "hostname"),
			cfg.Hostname + "\n",
		},
		{
			filepath.Join(root, "etc", "os-release"),
			"PRETTY_NAME=\"agentic-bash 1.0 (virtual)\"\n" +
				"NAME=\"agentic-bash\"\n" +
				"ID=agentic-bash\n" +
				"VERSION_ID=\"1.0\"\n" +
				"HOME_URL=\"https://github.com/piyushsingariya/agentic-bash\"\n",
		},
		{
			filepath.Join(root, "etc", "passwd"),
			fmt.Sprintf("%s:x:%d:%d:%s:/home/%s:/bin/bash\nroot:x:0:0:root:/root:/bin/bash\n",
				cfg.UserName, cfg.UID, cfg.GID, cfg.UserName, cfg.UserName),
		},
		{
			filepath.Join(root, "etc", "group"),
			fmt.Sprintf("%s:x:%d:\nroot:x:0:\n", cfg.UserName, cfg.GID),
		},
		{
			filepath.Join(root, "etc", "shells"),
			"/bin/sh\n/bin/bash\n",
		},
		{
			filepath.Join(root, "etc", "resolv.conf"),
			"nameserver 8.8.8.8\nnameserver 8.8.4.4\n",
		},
		{
			filepath.Join(root, "home", cfg.UserName, ".bashrc"),
			"export PS1='\\u@\\h:\\w\\$ '\nalias ll='ls -la'\nalias l='ls -CF'\n",
		},
		{
			filepath.Join(root, "home", cfg.UserName, ".profile"),
			"[ -f ~/.bashrc ] && . ~/.bashrc\n",
		},
	}

	for _, f := range files {
		if err := os.WriteFile(f.path, []byte(f.content), 0o644); err != nil {
			return fmt.Errorf("bootstrap: write %s: %w", f.path, err)
		}
	}

	return nil
}
