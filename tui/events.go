package tui

import (
	"time"

	"github.com/piyushsingariya/agentic-bash/sandbox"
)

// LogLevel classifies a log entry for color-coding in the log panel.
type LogLevel int

const (
	LogLevelCMD       LogLevel = iota // blue   — incoming command text
	LogLevelResult                    // green or red — completion summary
	LogLevelFile                      // yellow — filesystem change
	LogLevelMetric                    // gray   — cpu/mem numbers
	LogLevelViolation                 // red+bold — policy violation
	LogLevelSandbox                   // cyan   — lifecycle events
	LogLevelError                     // bright red — infrastructure error
)

// LogEntry is one structured row written by sandbox hooks and rendered by LogPanel.
// It is produced outside the bubbletea event loop (from hook goroutines and main)
// and consumed inside the loop via MsgLogEntry.
type LogEntry struct {
	At      time.Time
	Level   LogLevel
	Message string
}

// --- tea.Msg types ----------------------------------------------------------
// All inter-goroutine messages cross the bubbletea boundary through these types.

// MsgLogEntry arrives from listenLogCh for every entry the sandbox hooks emit.
type MsgLogEntry struct{ Entry LogEntry }

// MsgCommandStarted is sent optimistically just before sandbox.RunContext() is
// called, so the terminal panel can show the spinner and echo the prompt+command
// without waiting for execution to finish.
type MsgCommandStarted struct{ Cmd string }

// MsgCommandFinished carries the full result and a value-copy of the post-run
// ShellState. Using a value copy (not pointer) is safe across goroutines because
// the sandbox may mutate its internal state pointer on the next Run() call.
type MsgCommandFinished struct {
	Result sandbox.ExecutionResult
	State  sandbox.ShellState
}
