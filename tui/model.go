package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/piyushsingariya/agentic-bash/sandbox"
)

// SplitMode controls whether the two panels are laid out side-by-side or stacked.
type SplitMode int

const (
	SplitHorizontal SplitMode = iota // left 60% | right 40%  (default)
	SplitVertical                    // top 50% / bottom 50%
)

// Model is the root bubbletea model. It owns both panels, routes all messages,
// performs layout calculation, and renders the status bar.
type Model struct {
	sandbox       *sandbox.Sandbox
	logCh         <-chan LogEntry
	cancelRun     context.CancelFunc // non-nil while a command is running
	terminal      TerminalPanel
	logPanel      LogPanel
	splitMode     SplitMode
	totalWidth    int
	totalHeight   int
	isolationName string
	cmdCount      int
	running       bool
	ready         bool // false until the first WindowSizeMsg
}

// Config is passed from main to NewModel.
type Config struct {
	Sandbox       *sandbox.Sandbox
	LogCh         <-chan LogEntry
	SplitMode     SplitMode
	IsolationName string
}

// NewModel constructs the root model. Panels are initialised with placeholder
// dimensions (1×1) so their internal widgets (textinput, viewport, spinner) are
// fully constructed and focused before the first tea.WindowSizeMsg arrives.
// recalcLayout() will resize them to the real terminal dimensions on that first
// message.
func NewModel(cfg Config) Model {
	return Model{
		sandbox:       cfg.Sandbox,
		logCh:         cfg.LogCh,
		splitMode:     cfg.SplitMode,
		isolationName: cfg.IsolationName,
		terminal:      NewTerminalPanel(1, 1),
		logPanel:      NewLogPanel(1, 1),
	}
}

// Init starts the goroutine-safe log channel listener.
func (m Model) Init() tea.Cmd {
	return listenLogCh(m.logCh)
}

// Update is the central event dispatcher. It runs single-threaded in
// bubbletea's main goroutine, so no locking is required.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	// ── Layout ──────────────────────────────────────────────────────────────
	case tea.WindowSizeMsg:
		m.totalWidth = msg.Width
		m.totalHeight = msg.Height
		m.recalcLayout()
		if !m.ready {
			m.ready = true
		}

	// ── Keyboard ────────────────────────────────────────────────────────────
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			if m.running && m.cancelRun != nil {
				// Cancel the context held by the running command goroutine.
				m.cancelRun()
			} else {
				// No command running — quit the TUI.
				return m, tea.Quit
			}

		case "enter":
			if m.running {
				break
			}
			cmd := strings.TrimSpace(m.terminal.CurrentInput())
			if cmd == "" {
				break
			}
			if cmd == "exit" || cmd == "quit" {
				return m, tea.Quit
			}

			m.terminal.PushHistory(cmd)
			m.running = true
			m.cmdCount++

			ctx, cancel := context.WithCancel(context.Background())
			m.cancelRun = cancel

			// Optimistically echo the command in the terminal panel.
			var tCmd tea.Cmd
			m.terminal, tCmd = m.terminal.Update(MsgCommandStarted{Cmd: cmd})
			cmds = append(cmds, tCmd)

			// Launch execution in a background goroutine via tea.Cmd.
			cmds = append(cmds, runCommand(m.sandbox, cmd, ctx, cancel))

		default:
			// Forward all other keys to the terminal panel (input + viewport scrolling).
			var tCmd tea.Cmd
			m.terminal, tCmd = m.terminal.Update(msg)
			cmds = append(cmds, tCmd)
		}

	// ── Log channel ─────────────────────────────────────────────────────────
	case MsgLogEntry:
		var lCmd tea.Cmd
		m.logPanel, lCmd = m.logPanel.Update(msg)
		cmds = append(cmds, lCmd)
		// Re-issue the listener so the next entry is picked up.
		cmds = append(cmds, listenLogCh(m.logCh))

	// ── Command lifecycle ────────────────────────────────────────────────────
	case MsgCommandStarted:
		// Already handled above in the enter branch; this case is a no-op here
		// but kept to avoid the message being forwarded to both panels twice.

	case MsgCommandFinished:
		m.running = false
		m.cancelRun = nil
		var tCmd tea.Cmd
		m.terminal, tCmd = m.terminal.Update(msg)
		cmds = append(cmds, tCmd)

	// ── Spinner ticks ────────────────────────────────────────────────────────
	case spinner.TickMsg:
		var tCmd tea.Cmd
		m.terminal, tCmd = m.terminal.Update(msg)
		cmds = append(cmds, tCmd)

	// ── Everything else (mouse, scroll, etc.) ────────────────────────────────
	default:
		var tCmd, lCmd tea.Cmd
		m.terminal, tCmd = m.terminal.Update(msg)
		m.logPanel, lCmd = m.logPanel.Update(msg)
		cmds = append(cmds, tCmd, lCmd)
	}

	return m, tea.Batch(cmds...)
}

// View renders the full TUI: two bordered panels + status bar.
func (m Model) View() string {
	if !m.ready {
		return "initializing..."
	}

	termView := StyleTerminalBorder.
		Width(m.termInnerW()).
		Height(m.termInnerH()).
		Render(m.terminal.View())

	logView := StyleLogBorder.
		Width(m.logInnerW()).
		Height(m.logInnerH()).
		Render(m.logPanel.View())

	var panels string
	if m.splitMode == SplitHorizontal {
		panels = lipgloss.JoinHorizontal(lipgloss.Top, termView, logView)
	} else {
		panels = lipgloss.JoinVertical(lipgloss.Left, termView, logView)
	}

	return lipgloss.JoinVertical(lipgloss.Left, panels, m.statusBar())
}

// statusBar renders the single-row status bar pinned to the bottom.
func (m Model) statusBar() string {
	cwd := m.sandbox.State().Cwd
	info := fmt.Sprintf("isolation=%s  cmds:%d", m.isolationName, m.cmdCount)

	left := StyleStatusCwd.Render(cwd)
	mid := StyleStatusBar.Render(info)

	var right string
	if m.running {
		right = StyleStatusRunning.Render("● RUNNING")
	}

	used := lipgloss.Width(left) + lipgloss.Width(mid) + lipgloss.Width(right)
	gap := m.totalWidth - used
	if gap < 0 {
		gap = 0
	}
	fill := StyleStatusBar.Render(strings.Repeat(" ", gap))

	return lipgloss.JoinHorizontal(lipgloss.Bottom, left, mid, fill, right)
}

// recalcLayout recomputes all panel dimensions and notifies sub-models.
// Called on every tea.WindowSizeMsg.
func (m *Model) recalcLayout() {
	contentH := m.totalHeight - StatusBarHeight

	if m.splitMode == SplitHorizontal {
		termOuter := int(float64(m.totalWidth) * TermWidthRatio)
		logOuter := m.totalWidth - termOuter // handles odd widths cleanly

		termInW := clamp(termOuter-BorderWidth, 1)
		logInW := clamp(logOuter-BorderWidth, 1)
		inH := clamp(contentH-BorderHeight, 1)

		m.terminal.SetSize(termInW, inH)
		m.logPanel.SetSize(logInW, inH)
	} else {
		topH := contentH / 2
		botH := contentH - topH
		inW := clamp(m.totalWidth-BorderWidth, 1)

		m.terminal.SetSize(inW, clamp(topH-BorderHeight, 1))
		m.logPanel.SetSize(inW, clamp(botH-BorderHeight, 1))
	}
}

// Dimension helpers — used by View() to pass the correct Width/Height to lipgloss borders.

func (m Model) termInnerW() int {
	if m.splitMode == SplitHorizontal {
		return clamp(int(float64(m.totalWidth)*TermWidthRatio)-BorderWidth, 1)
	}
	return clamp(m.totalWidth-BorderWidth, 1)
}

func (m Model) termInnerH() int {
	contentH := m.totalHeight - StatusBarHeight
	if m.splitMode == SplitHorizontal {
		return clamp(contentH-BorderHeight, 1)
	}
	return clamp(contentH/2-BorderHeight, 1)
}

func (m Model) logInnerW() int {
	if m.splitMode == SplitHorizontal {
		termOuter := int(float64(m.totalWidth) * TermWidthRatio)
		return clamp(m.totalWidth-termOuter-BorderWidth, 1)
	}
	return clamp(m.totalWidth-BorderWidth, 1)
}

func (m Model) logInnerH() int {
	contentH := m.totalHeight - StatusBarHeight
	if m.splitMode == SplitHorizontal {
		return clamp(contentH-BorderHeight, 1)
	}
	topH := contentH / 2
	return clamp((contentH-topH)-BorderHeight, 1)
}

// listenLogCh returns a tea.Cmd that blocks waiting for exactly one LogEntry,
// then returns it as a MsgLogEntry. The Update function re-issues this after
// every receipt to keep the channel continuously drained.
func listenLogCh(ch <-chan LogEntry) tea.Cmd {
	return func() tea.Msg {
		return MsgLogEntry{Entry: <-ch}
	}
}

// runCommand executes cmd via RunContext in a bubbletea background goroutine.
// The caller-supplied ctx is wired to Ctrl+C so the user can interrupt execution.
// cancel is always deferred so the context is released when done.
func runCommand(sb *sandbox.Sandbox, cmd string, ctx context.Context, cancel context.CancelFunc) tea.Cmd {
	return func() tea.Msg {
		defer cancel()
		result := sb.RunContext(ctx, cmd)
		state := *sb.State() // value copy — safe after RunContext returns
		return MsgCommandFinished{Result: result, State: state}
	}
}
