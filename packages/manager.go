package packages

import "context"

// PackageManager is the common interface implemented by each manager shim.
type PackageManager interface {
	// Install installs one or more packages into the sandbox overlay.
	Install(ctx context.Context, pkgs []string) error
	// Uninstall removes one or more packages from the sandbox overlay.
	Uninstall(ctx context.Context, pkgs []string) error
	// IsInstalled reports whether pkg is recorded in the manifest.
	IsInstalled(pkg string) bool
	// Installed returns all packages recorded in the manifest for this manager.
	Installed() []PackageInfo
}
