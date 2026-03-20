//go:build linux

package cgroups

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const cgroupRoot = "/sys/fs/cgroup/agentic-bash"

type linuxManager struct {
	available bool
}

func newManager() Manager {
	_, err := os.Stat("/sys/fs/cgroup/cgroup.controllers")
	return &linuxManager{available: err == nil}
}

func (m *linuxManager) Available() bool { return m.available }

func (m *linuxManager) New(id string, opts Opts) (Cgroup, error) {
	dir := filepath.Join(cgroupRoot, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("cgroups: mkdir %s: %w", dir, err)
	}

	if opts.MaxMemoryBytes > 0 {
		if err := writeFile(filepath.Join(dir, "memory.max"),
			strconv.FormatInt(opts.MaxMemoryBytes, 10)); err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("cgroups: set memory.max: %w", err)
		}
	}

	if opts.CPUQuota > 0 {
		// cpu.max format: "<quota_usec> <period_usec>"
		// Standard 100ms period.
		quota := int64(opts.CPUQuota * 100_000)
		if err := writeFile(filepath.Join(dir, "cpu.max"),
			fmt.Sprintf("%d 100000", quota)); err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("cgroups: set cpu.max: %w", err)
		}
	}

	return &linuxCgroup{dir: dir}, nil
}

type linuxCgroup struct{ dir string }

func (c *linuxCgroup) AddPID(pid int) error {
	return writeFile(filepath.Join(c.dir, "cgroup.procs"), strconv.Itoa(pid))
}

func (c *linuxCgroup) Stop() (cpuUsec uint64, memPeakBytes uint64, err error) {
	cpuUsec, _ = readStatField(filepath.Join(c.dir, "cpu.stat"), "usage_usec")

	// memory.peak requires Linux 5.19+; fall back to memory.current on older kernels.
	memPeakBytes, _ = readUintFile(filepath.Join(c.dir, "memory.peak"))
	if memPeakBytes == 0 {
		memPeakBytes, _ = readUintFile(filepath.Join(c.dir, "memory.current"))
	}

	err = os.RemoveAll(c.dir)
	return
}

// writeFile writes text to path, truncating the file.
func writeFile(path, text string) error {
	return os.WriteFile(path, []byte(text), 0o644)
}

// readUintFile reads a single uint64 from a cgroup control file.
func readUintFile(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}

// readStatField parses a "key value\n" stat file and returns the named field.
func readStatField(path, field string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) == 2 && parts[0] == field {
			return strconv.ParseUint(parts[1], 10, 64)
		}
	}
	return 0, fmt.Errorf("cgroups: field %q not found in %s", field, path)
}
