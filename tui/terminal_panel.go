package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// inputAreaHeight is the number of rows consumed below the viewport:
//   row 1: separator line (─ × width)
//   row 2: prompt + text input  OR  spinner + "running..."
const inputAreaHeight = 2

// TerminalPanel is the left/top interactive shell panel.
// It renders a scrollable history of commands and their output, a text input
// for entering commands, and a spinner while a command is executing.
type TerminalPanel struct {
	viewport   viewport.Model
	input      textinput.Model
	spinner    spinner.Model
	running    bool
	history    []string // submitted command strings, oldest first
	historyIdx int      // -1 = not navigating; 0 = most recent
	content    []string // accumulated rendered lines in the viewport buffer
	width      int      // inner width (post-border)
	height     int      // inner height (post-border)
	cwd        string   // updated from MsgCommandFinished.State.Cwd
}

// NewTerminalPanel creates a TerminalPanel with the given inner dimensions.
func NewTerminalPanel(width, height int) TerminalPanel {
	w := clamp(width, 1)
	h := clamp(height, 1)

	ti := textinput.New()
	ti.Placeholder = "enter command..."
	ti.Focus()
	ti.CharLimit = 4096

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = StyleSpinner

	vp := viewport.New(w, clamp(h-inputAreaHeight, 1))

	p := TerminalPanel{
		viewport:   vp,
		input:      ti,
		spinner:    sp,
		historyIdx: -1,
		width:      w,
		height:     h,
		cwd:        os.Getenv("HOME"),
	}
	p.input.Width = p.inputWidth()
	return p
}

// SetSize recalculates inner dimensions after a window resize and re-wraps
// viewport content at the new width so there is no stale frame.
func (p *TerminalPanel) SetSize(width, height int) {
	p.width = clamp(width, 1)
	p.height = clamp(height, 1)
	p.viewport.Width = p.width
	p.viewport.Height = clamp(p.height-inputAreaHeight, 1)
	p.input.Width = p.inputWidth()
	// Re-wrap content at new width.
	p.viewport.SetContent(strings.Join(p.content, "\n"))
}

// Update handles all messages relevant to the terminal panel.
// Returns the updated panel and any commands to issue.
func (p TerminalPanel) Update(msg tea.Msg) (TerminalPanel, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case MsgCommandStarted:
		p.running = true
		p.appendLine(StylePrompt.Render(p.promptPrefix()) + StyleStdout.Render(msg.Cmd))
		// Start the spinner tick loop.
		cmds = append(cmds, p.spinner.Tick)

	case MsgCommandFinished:
		p.running = false
		r := msg.Result
		p.cwd = msg.State.Cwd

		if r.Stdout != "" {
			for _, line := range splitLines(r.Stdout) {
				p.appendLine(StyleStdout.Render(line))
			}
		}
		if r.Stderr != "" {
			for _, line := range splitLines(r.Stderr) {
				p.appendLine(StyleStderr.Render(line))
			}
		}
		if r.Error != nil {
			p.appendLine(StyleStderr.Render("error: " + r.Error.Error()))
		}
		if r.ExitCode != 0 {
			p.appendLine(StyleExitCode.Render(fmt.Sprintf("[exit %d]", r.ExitCode)))
		}
		p.viewport.GotoBottom()
		p.input.Reset()

	case spinner.TickMsg:
		if p.running {
			p.spinner, cmd = p.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}

	case tea.KeyMsg:
		if p.running {
			// All keypresses except Ctrl+C are swallowed while a command runs.
			// Ctrl+C is handled upstream in the root model.
			break
		}
		switch msg.Type {
		case tea.KeyUp:
			p.navigateHistory(-1)
		case tea.KeyDown:
			p.navigateHistory(+1)
		default:
			// Any non-navigation key resets history traversal.
			p.historyIdx = -1
			p.input, cmd = p.input.Update(msg)
			cmds = append(cmds, cmd)
		}

	default:
		// Forward viewport scroll events (mouse wheel, page up/down).
		p.viewport, cmd = p.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

	return p, tea.Batch(cmds...)
}

// View renders the terminal panel interior (border applied by root model).
// Layout: input row at top, separator, scrollable output history below.
func (p TerminalPanel) View() string {
	sep := strings.Repeat("─", p.width)

	var topRow string
	if p.running {
		topRow = p.spinner.View() + StyleLevelMetric.Render(" running...")
	} else {
		topRow = StylePrompt.Render(p.promptPrefix()) + p.input.View()
	}

	return topRow + "\n" + sep + "\n" + p.viewport.View()
}

// CurrentInput returns the current value of the text input field.
func (p TerminalPanel) CurrentInput() string { return p.input.Value() }

// PushHistory appends cmd to the history slice and resets the navigation index.
func (p *TerminalPanel) PushHistory(cmd string) {
	p.history = append(p.history, cmd)
	p.historyIdx = -1
}

// appendLine adds a rendered line to the content buffer and updates the viewport.
func (p *TerminalPanel) appendLine(line string) {
	p.content = append(p.content, line)
	p.viewport.SetContent(strings.Join(p.content, "\n"))
}

// navigateHistory moves through submitted history.
// delta = -1 means "older" (up arrow); delta = +1 means "newer" (down arrow).
// History is stored oldest-first; we navigate newest-first for shell-like UX.
func (p *TerminalPanel) navigateHistory(delta int) {
	if len(p.history) == 0 {
		return
	}
	// On first up-press, start at the most recent entry (index 0 in reversed terms).
	if p.historyIdx == -1 && delta == -1 {
		p.historyIdx = 0
	} else {
		p.historyIdx += delta
	}

	// Clamp: past newest clears the input and resets.
	if p.historyIdx < 0 {
		p.historyIdx = -1
		p.input.SetValue("")
		return
	}
	// Clamp: can't go older than the oldest entry.
	if p.historyIdx >= len(p.history) {
		p.historyIdx = len(p.history) - 1
	}

	// history[0] = oldest → reversed index for newest-first navigation.
	reversed := len(p.history) - 1 - p.historyIdx
	p.input.SetValue(p.history[reversed])
	p.input.CursorEnd()
}

// promptPrefix returns the shell-style prompt string showing the current directory.
func (p TerminalPanel) promptPrefix() string {
	short := p.cwd
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(short, home) {
		short = "~" + short[len(home):]
	}
	return fmt.Sprintf("sandbox [%s] $ ", short)
}

// inputWidth calculates the available width for the text input widget,
// accounting for the prompt prefix length and a small right margin.
func (p TerminalPanel) inputWidth() int {
	return clamp(p.width-len(p.promptPrefix())-1, 1)
}

// splitLines splits s on newlines and trims a trailing empty line.
func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
