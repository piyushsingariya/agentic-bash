//go:build !linux

package isolation

import "os/exec"

// NamespaceStrategy is not available on non-Linux platforms.
type NamespaceStrategy struct{}

func newNamespace() IsolationStrategy { return &NamespaceStrategy{} }

// NewNamespaceForTest returns a NamespaceStrategy; used in cross-platform tests.
func NewNamespaceForTest() IsolationStrategy { return &NamespaceStrategy{} }

func (n *NamespaceStrategy) Name() string           { return "namespace" }
func (n *NamespaceStrategy) Available() bool        { return false }
func (n *NamespaceStrategy) Wrap(_ *exec.Cmd) error { return nil }
func (n *NamespaceStrategy) Apply() error           { return nil }
