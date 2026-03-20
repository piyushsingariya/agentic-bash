package tui

import "github.com/charmbracelet/lipgloss"

// Panel borders
var (
	StyleTerminalBorder = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("62")) // indigo

	StyleLogBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("241")) // dark gray
)

// Terminal panel content
var (
	StylePrompt   = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))  // bright green
	StyleStdout   = lipgloss.NewStyle()                                    // default terminal color
	StyleStderr   = lipgloss.NewStyle().Foreground(lipgloss.Color("202")) // orange-red
	StyleExitCode = lipgloss.NewStyle().Foreground(lipgloss.Color("196")) // red
	StyleSpinner  = lipgloss.NewStyle().Foreground(lipgloss.Color("205")) // pink
)

// Log panel per-level styles
var (
	StyleLevelCMD       = lipgloss.NewStyle().Foreground(lipgloss.Color("33"))                    // blue
	StyleLevelResult    = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))                    // green
	StyleLevelResultErr = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))                   // red
	StyleLevelFile      = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))                   // yellow
	StyleLevelMetric    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))                   // dim gray
	StyleLevelViolation = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)        // red bold
	StyleLevelSandbox   = lipgloss.NewStyle().Foreground(lipgloss.Color("51"))                    // cyan
	StyleLevelError     = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)        // red bold
	StyleTimestamp      = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))                   // dim gray
)

// Status bar
var (
	StyleStatusBar     = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("252")).Padding(0, 1)
	StyleStatusRunning = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("214")).Bold(true).Padding(0, 1)
	StyleStatusCwd     = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("86")).Padding(0, 1)
)

// Layout constants
const (
	// BorderWidth is the combined left+right border column cost of a RoundedBorder.
	BorderWidth = 2
	// BorderHeight is the combined top+bottom border row cost of a RoundedBorder.
	BorderHeight = 2
	// StatusBarHeight is the single fixed row at the bottom of the screen.
	StatusBarHeight = 1
	// TermWidthRatio is the fraction of terminal width assigned to the left panel
	// in horizontal split mode (60/40).
	TermWidthRatio = 0.60
)

// clamp returns v if v >= min, otherwise min. Guards against zero/negative
// viewport dimensions on very small terminals.
func clamp(v, min int) int {
	if v < min {
		return min
	}
	return v
}
