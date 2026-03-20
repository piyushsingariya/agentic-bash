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

// TerminalPanel is the left/top interactive shell panel.
//
// Rendering model — the panel behaves like a real terminal:
//   - The viewport occupies the full panel height.
//   - The live prompt (textinput or spinner) is embedded as the last line of the
//     viewport content, so every command and its output form one continuous
//     scrollable buffer.
//   - New output auto-scrolls to the bottom; the user can scroll up freely
//     without the position being reset by keypresses.
type TerminalPanel struct {
	viewport   viewport.Model
	input      textinput.Model
	spinner    spinner.Model
	running    bool
	history    []string // submitted command strings, oldest first
	historyIdx int      // -1 = not navigating; 0 = most recent
	content    []string // accumulated rendered lines (history only; live prompt appended in refreshViewport)
	width      int      // inner width (post-border)
	height     int      // inner height (post-border)
	cwd        string   // updated from MsgCommandFinished.State.Cwd
	atBottom   bool     // true when viewport was at bottom before last update
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

	vp := viewport.New(w, h)

	p := TerminalPanel{
		viewport:   vp,
		input:      ti,
		spinner:    sp,
		historyIdx: -1,
		width:      w,
		height:     h,
		cwd:        os.Getenv("HOME"),
		atBottom:   true,
	}
	p.input.Width = p.inputWidth()
	p.refreshViewport(true)
	return p
}

// SetSize recalculates inner dimensions after a window resize.
func (p *TerminalPanel) SetSize(width, height int) {
	p.width = clamp(width, 1)
	p.height = clamp(height, 1)
	p.viewport.Width = p.width
	p.viewport.Height = p.height
	p.input.Width = p.inputWidth()
	p.refreshViewport(true)
}

// Update handles all messages relevant to the terminal panel.
func (p TerminalPanel) Update(msg tea.Msg) (TerminalPanel, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case MsgCommandStarted:
		p.running = true
		// Freeze the current prompt + command text into the history buffer.
		p.content = append(p.content,
			StylePrompt.Render(p.promptPrefix())+StyleStdout.Render(msg.Cmd),
		)
		p.refreshViewport(true)
		cmds = append(cmds, p.spinner.Tick)

	case MsgCommandFinished:
		p.running = false
		r := msg.Result
		p.cwd = msg.State.Cwd

		if r.Stdout != "" {
			for _, line := range splitLines(r.Stdout) {
				p.content = append(p.content, StyleStdout.Render(line))
			}
		}
		if r.Stderr != "" {
			for _, line := range splitLines(r.Stderr) {
				p.content = append(p.content, StyleStderr.Render(line))
			}
		}
		if r.Error != nil {
			p.content = append(p.content, StyleStderr.Render("error: "+r.Error.Error()))
		}
		if r.ExitCode != 0 {
			p.content = append(p.content, StyleExitCode.Render(fmt.Sprintf("[exit %d]", r.ExitCode)))
		}
		p.input.Reset()
		p.refreshViewport(true)

	case spinner.TickMsg:
		if p.running {
			p.spinner, cmd = p.spinner.Update(msg)
			cmds = append(cmds, cmd)
			// Refresh so the spinner animation updates in the viewport.
			p.refreshViewport(false)
		}

	case tea.KeyMsg:
		if p.running {
			break
		}
		switch msg.Type {
		case tea.KeyUp:
			p.navigateHistory(-1)
			p.refreshViewport(false)
		case tea.KeyDown:
			p.navigateHistory(+1)
			p.refreshViewport(false)
		default:
			p.historyIdx = -1
			p.input, cmd = p.input.Update(msg)
			cmds = append(cmds, cmd)
			// Redraw live prompt with updated input value (don't steal scroll position).
			p.refreshViewport(false)
		}

	default:
		// Forward scroll events so the user can scroll up through history.
		p.viewport, cmd = p.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

	return p, tea.Batch(cmds...)
}

// View renders the terminal panel — just the viewport; the live prompt is
// already embedded at the bottom of its content.
func (p TerminalPanel) View() string {
	return p.viewport.View()
}

// CurrentInput returns the current value of the text input field.
func (p TerminalPanel) CurrentInput() string { return p.input.Value() }

// PushHistory appends cmd to the history slice and resets the navigation index.
func (p *TerminalPanel) PushHistory(cmd string) {
	p.history = append(p.history, cmd)
	p.historyIdx = -1
}

// refreshViewport rebuilds the viewport content to include all frozen history
// lines plus the live prompt at the bottom.
//
// scrollToBottom forces the viewport to the last line (used when new output is
// added).  When false the current scroll position is preserved so the user can
// read history while commands run.
func (p *TerminalPanel) refreshViewport(scrollToBottom bool) {
	var livePrompt string
	if p.running {
		livePrompt = p.spinner.View() + StyleLevelMetric.Render(" running...")
	} else {
		livePrompt = StylePrompt.Render(p.promptPrefix()) + p.input.View()
	}

	var b strings.Builder
	for _, line := range p.content {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString(livePrompt)

	p.viewport.SetContent(b.String())
	if scrollToBottom {
		p.viewport.GotoBottom()
	}
}

// navigateHistory moves through submitted history.
// delta = -1 means "older" (up arrow); delta = +1 means "newer" (down arrow).
func (p *TerminalPanel) navigateHistory(delta int) {
	if len(p.history) == 0 {
		return
	}
	if p.historyIdx == -1 && delta == -1 {
		p.historyIdx = 0
	} else {
		p.historyIdx += delta
	}

	if p.historyIdx < 0 {
		p.historyIdx = -1
		p.input.SetValue("")
		return
	}
	if p.historyIdx >= len(p.history) {
		p.historyIdx = len(p.history) - 1
	}

	reversed := len(p.history) - 1 - p.historyIdx
	p.input.SetValue(p.history[reversed])
	p.input.CursorEnd()
}

// promptPrefix returns the shell-style prompt string.
func (p TerminalPanel) promptPrefix() string {
	short := p.cwd
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(short, home) {
		short = "~" + short[len(home):]
	}
	return fmt.Sprintf("sandbox [%s] $ ", short)
}

// inputWidth calculates the available width for the text input widget.
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
