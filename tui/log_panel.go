package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// MaxLogEntries is the maximum number of log entries retained in memory.
// Oldest entries are dropped when the cap is exceeded.
const MaxLogEntries = 1000

// LogPanel is the right/bottom structured log stream panel.
// It renders timestamped, color-coded log entries from the sandbox hooks
// and auto-scrolls to the bottom unless the user has manually scrolled up.
type LogPanel struct {
	viewport   viewport.Model
	entries    []LogEntry
	autoScroll bool
	width      int // inner width (post-border)
	height     int // inner height (post-border)
}

// NewLogPanel creates a LogPanel with the given inner dimensions.
func NewLogPanel(width, height int) LogPanel {
	w := clamp(width, 1)
	h := clamp(height, 1)
	vp := viewport.New(w, h)
	vp.SetContent("")
	return LogPanel{
		viewport:   vp,
		autoScroll: true,
		width:      w,
		height:     h,
	}
}

// SetSize recalculates the panel dimensions after a terminal resize.
// It also re-renders all entries so the viewport wraps at the new width.
func (p *LogPanel) SetSize(width, height int) {
	p.width = clamp(width, 1)
	p.height = clamp(height, 1)
	p.viewport.Width = p.width
	p.viewport.Height = p.height
	p.rerender()
}

// Update handles messages relevant to the log panel.
func (p LogPanel) Update(msg tea.Msg) (LogPanel, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case MsgLogEntry:
		p.entries = append(p.entries, msg.Entry)
		if len(p.entries) > MaxLogEntries {
			p.entries = p.entries[len(p.entries)-MaxLogEntries:]
		}
		p.rerender()
		if p.autoScroll {
			p.viewport.GotoBottom()
		}
		return p, nil

	default:
		prevOffset := p.viewport.YOffset
		p.viewport, cmd = p.viewport.Update(msg)
		// If the user scrolled up (YOffset decreased), stop auto-scrolling.
		if p.viewport.YOffset < prevOffset {
			p.autoScroll = false
		}
		// Re-enable auto-scroll when the user reaches the bottom again.
		if p.viewport.AtBottom() {
			p.autoScroll = true
		}
	}

	return p, cmd
}

// View renders the log panel interior (border is applied by the root model).
func (p LogPanel) View() string {
	return p.viewport.View()
}

// rerender rebuilds the full viewport content string from the entries slice.
// Must be called after any append and after SetSize to keep wrapping correct.
func (p *LogPanel) rerender() {
	lines := make([]string, 0, len(p.entries))
	for _, e := range p.entries {
		lines = append(lines, p.renderEntry(e))
	}
	p.viewport.SetContent(strings.Join(lines, "\n"))
}

// renderEntry formats a single LogEntry into a styled terminal string.
// The timestamp prefix is 12 chars; the body is truncated to avoid lipgloss
// layout breakage on lines wider than the panel.
func (p *LogPanel) renderEntry(e LogEntry) string {
	ts := StyleTimestamp.Render(e.At.Format("15:04:05.000"))

	var tag, body string
	switch e.Level {
	case LogLevelCMD:
		tag = "CMD "
		body = StyleLevelCMD.Render(fmt.Sprintf("→ %s", e.Message))
	case LogLevelResult:
		tag = "RES "
		if strings.HasPrefix(e.Message, "✓") {
			body = StyleLevelResult.Render(e.Message)
		} else {
			body = StyleLevelResultErr.Render(e.Message)
		}
	case LogLevelFile:
		tag = "FILE"
		body = StyleLevelFile.Render(e.Message)
	case LogLevelMetric:
		tag = "METR"
		body = StyleLevelMetric.Render(e.Message)
	case LogLevelViolation:
		tag = "VIOL"
		body = StyleLevelViolation.Render("⚠ " + e.Message)
	case LogLevelSandbox:
		tag = "SBOX"
		body = StyleLevelSandbox.Render(e.Message)
	case LogLevelError:
		tag = "ERR "
		body = StyleLevelError.Render(e.Message)
	default:
		tag = "    "
		body = e.Message
	}

	// Truncate raw message text to prevent the line from exceeding panel width.
	// 13 = 12 (timestamp) + 1 (space). 6 = 4 (tag) + 1 (space) + 1 (margin).
	available := p.width - 13 - 6
	if available > 0 && len(e.Message) > available {
		body = body[:available]
	}

	return fmt.Sprintf("%s %s %s", ts, StyleLevelMetric.Render(tag), body)
}
