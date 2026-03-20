//go:build !linux

package isolation

import "os/exec"

// LandlockStrategy is not available on non-Linux platforms.
type LandlockStrategy struct{}

func newLandlock() IsolationStrategy { return &LandlockStrategy{} }

// NewLandlockStrategy returns an unavailable LandlockStrategy on non-Linux.
func NewLandlockStrategy(_ ...string) *LandlockStrategy { return &LandlockStrategy{} }

func (l *LandlockStrategy) Name() string           { return "landlock" }
func (l *LandlockStrategy) Available() bool        { return false }
func (l *LandlockStrategy) Wrap(_ *exec.Cmd) error { return nil }
func (l *LandlockStrategy) Apply() error           { return nil }
