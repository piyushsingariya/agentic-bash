package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/piyushsingariya/agentic-bash/sandbox"
	"github.com/piyushsingariya/agentic-bash/tui"
)

func main() {
	vertical := flag.Bool("vertical", false, "stack panels top/bottom instead of left/right (default: left/right)")
	flag.Parse()

	// Buffered channel so sandbox hook callbacks never block the execution goroutine.
	logCh := make(chan tui.LogEntry, 256)

	sb, err := sandbox.New(sandbox.Options{
		OnCommand: func(cmd string) {
			nonBlocking(logCh, tui.LogEntry{
				At:      time.Now(),
				Level:   tui.LogLevelCMD,
				Message: cmd,
			})
		},
		OnResult: func(r sandbox.ExecutionResult) {
			if r.Error != nil {
				nonBlocking(logCh, tui.LogEntry{
					At:      time.Now(),
					Level:   tui.LogLevelError,
					Message: r.Error.Error(),
				})
				return
			}

			icon := "✓"
			if r.ExitCode != 0 {
				icon = "✗"
			}
			nonBlocking(logCh, tui.LogEntry{
				At:      time.Now(),
				Level:   tui.LogLevelResult,
				Message: fmt.Sprintf("%s exit=%d duration=%s", icon, r.ExitCode, r.Duration.Round(time.Millisecond)),
			})

			for _, f := range r.FilesCreated {
				nonBlocking(logCh, tui.LogEntry{At: time.Now(), Level: tui.LogLevelFile, Message: "+ created: " + f})
			}
			for _, f := range r.FilesModified {
				nonBlocking(logCh, tui.LogEntry{At: time.Now(), Level: tui.LogLevelFile, Message: "~ modified: " + f})
			}
			for _, f := range r.FilesDeleted {
				nonBlocking(logCh, tui.LogEntry{At: time.Now(), Level: tui.LogLevelFile, Message: "- deleted: " + f})
			}

			if r.CPUTime > 0 || r.MemoryPeakMB > 0 {
				nonBlocking(logCh, tui.LogEntry{
					At:      time.Now(),
					Level:   tui.LogLevelMetric,
					Message: fmt.Sprintf("cpu=%s mem=%dMB", r.CPUTime.Round(time.Millisecond), r.MemoryPeakMB),
				})
			}
		},
		OnViolation: func(v sandbox.PolicyViolation) {
			word := "logged"
			if v.Blocked {
				word = "blocked"
			}
			nonBlocking(logCh, tui.LogEntry{
				At:      time.Now(),
				Level:   tui.LogLevelViolation,
				Message: fmt.Sprintf("policy %s: %s — %s", word, v.Type, v.Detail),
			})
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer sb.Close()

	// Emit a startup event into the log panel.
	logCh <- tui.LogEntry{
		At:      time.Now(),
		Level:   tui.LogLevelSandbox,
		Message: fmt.Sprintf("sandbox initialized, isolation=%s, workdir=%s", sb.Isolation().Name(), sb.State().Cwd),
	}

	split := tui.SplitHorizontal
	if *vertical {
		split = tui.SplitVertical
	}

	model := tui.NewModel(tui.Config{
		Sandbox:       sb,
		LogCh:         logCh,
		SplitMode:     split,
		IsolationName: sb.Isolation().Name(),
	})

	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),       // full-screen canvas, restored on exit
		tea.WithMouseCellMotion(), // enables mouse wheel scrolling
	)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tui error: %v\n", err)
		os.Exit(1)
	}
}

// nonBlocking sends to a buffered channel without ever blocking.
// Dropping entries is safe — the sandbox output is already shown in the terminal
// panel; the log channel is a supplementary structured view.
func nonBlocking(ch chan<- tui.LogEntry, e tui.LogEntry) {
	select {
	case ch <- e:
	default:
	}
}
