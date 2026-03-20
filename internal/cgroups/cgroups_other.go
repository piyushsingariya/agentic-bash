//go:build !linux

package cgroups

type noopManager struct{}

func newManager() Manager { return &noopManager{} }

func (n *noopManager) Available() bool                        { return false }
func (n *noopManager) New(_ string, _ Opts) (Cgroup, error)   { return &noopCgroup{}, nil }

type noopCgroup struct{}

func (c *noopCgroup) AddPID(_ int) error                        { return nil }
func (c *noopCgroup) Stop() (uint64, uint64, error)             { return 0, 0, nil }
