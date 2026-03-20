# TUI Plan for agentic-bash

## Overview

Transform `main.go` into a production-grade Terminal User Interface (TUI) with two live panels:

- **Left panel (60%)** — Interactive shell: user types commands, sees output like a real terminal
- **Right panel (40%)** — Real-time structured log stream from sandbox internals

Default split is horizontal (60/40). A `--vertical` flag switches to a stacked top/bottom layout.

---

## Architecture

```
main.go  ──creates──►  sandbox (with hooks)  ──writes──►  logCh (chan LogEntry, buf=256)
                                                                    │
tea.NewProgram(model)                                               │
        │                                                    listenLogCh tea.Cmd
        ▼                                                           │
   Model.Update()  ◄────────────────────────────────────────────────┘
        │
        ├── TerminalPanel  (left, 60%)   — viewport + textinput + spinner
        └── LogPanel       (right, 40%)  — viewport, auto-scroll, color-coded
```

The sandbox `OnCommand`, `OnResult`, and `OnViolation` hooks write `LogEntry` values to a
buffered channel using a non-blocking send. A dedicated `tea.Cmd` (`listenLogCh`) blocks on
that channel and re-issues itself after every receipt — the canonical bubbletea pattern for
goroutine-safe messaging. Command execution runs inside a `tea.Cmd` goroutine so the UI never
freezes.

---

## File Structure

```
main.go                   — rewritten entry point
tui/
  events.go               — LogLevel, LogEntry, all tea.Msg types
  styles.go               — all lipgloss styles (single source of truth)
  terminal_panel.go       — TerminalPanel: viewport + textinput + spinner
  log_panel.go            — LogPanel: viewport, auto-scroll, color rendering
  model.go                — root Model, layout math, runCommand, listenLogCh
plans/
  tui.md                  — this file
```

---

## New Dependencies

```
github.com/charmbracelet/bubbletea  v1.3.5
github.com/charmbracelet/bubbles    v0.20.0
github.com/charmbracelet/lipgloss   v1.1.0
```

Add with:
```
go get github.com/charmbracelet/bubbletea@v1.3.5 \
       github.com/charmbracelet/bubbles@v0.20.0 \
       github.com/charmbracelet/lipgloss@v1.1.0
go mod tidy
```

---

## Sandbox Changes

### Add `RunContext` to `sandbox.Sandbox`

The current `sandbox.Run()` creates its own internal `context.WithTimeout` with no way for
callers to inject cancellation. To support Ctrl+C interrupt from the TUI (and any future
caller), the sandbox must store an external cancellable context.

**Design:**

- Add a `ctx context.Context` field to `Sandbox`.
- On `New()`, default it to `context.Background()`.
- Add `SetContext(ctx context.Context)` so callers can replace it at any time.
- In `Run()`, merge the caller-set context with the per-run timeout:
  ```go
  ctx, cancel := context.WithDeadline(s.ctx, time.Now().Add(s.opts.Limits.Timeout))
  defer cancel()
  ```
- Expose a `RunContext(ctx context.Context, cmd string) ExecutionResult` method that
  temporarily overrides the context for one call without mutating the sandbox field.

**Files changed:** `sandbox/sandbox.go`, `sandbox/options.go` (no struct changes needed,
just the new method).

**TUI usage:** `runCommand` tea.Cmd calls `sb.RunContext(ctx, cmd)` where `ctx` is a
`context.WithCancel` tied to the Ctrl+C keypress. Cancelling it surfaces as a timeout/error
result in `MsgCommandFinished`.

---

## Struct Definitions

### `tui/events.go`

```go
package tui

import (
    "time"
    "github.com/piyushsingariya/agentic-bash/sandbox"
)

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
type LogEntry struct {
    At      time.Time
    Level   LogLevel
    Message string
}

// tea.Msg types — all inter-goroutine messages cross the boundary through these.

// MsgLogEntry arrives from listenLogCh for every entry the sandbox hooks emit.
type MsgLogEntry struct{ Entry LogEntry }

// MsgCommandStarted is sent optimistically just before sandbox.RunContext() is called,
// so the terminal panel can show the spinner and echo the prompt+command immediately.
type MsgCommandStarted struct{ Cmd string }

// MsgCommandFinished carries the full result and a value-copy of the post-run ShellState.
type MsgCommandFinished struct {
    Result sandbox.ExecutionResult
    State  sandbox.ShellState // value copy, not pointer — safe across goroutines
}
```

### `tui/styles.go`

All lipgloss style variables. No style literals appear anywhere else.

```go
package tui

import "github.com/charmbracelet/lipgloss"

var (
    StyleTerminalBorder = lipgloss.NewStyle().
        Border(lipgloss.RoundedBorder()).
        BorderForeground(lipgloss.Color("62"))   // indigo

    StyleLogBorder = lipgloss.NewStyle().
        Border(lipgloss.RoundedBorder()).
        BorderForeground(lipgloss.Color("241"))  // dark gray

    StylePrompt   = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))   // bright green
    StyleStdout   = lipgloss.NewStyle()
    StyleStderr   = lipgloss.NewStyle().Foreground(lipgloss.Color("202"))  // orange-red
    StyleExitCode = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))  // red
    StyleSpinner  = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))  // pink

    StyleLevelCMD       = lipgloss.NewStyle().Foreground(lipgloss.Color("33"))
    StyleLevelResult    = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
    StyleLevelResultErr = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
    StyleLevelFile      = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
    StyleLevelMetric    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
    StyleLevelViolation = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
    StyleLevelSandbox   = lipgloss.NewStyle().Foreground(lipgloss.Color("51"))
    StyleLevelError     = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
    StyleTimestamp      = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

    StyleStatusBar     = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("252")).Padding(0, 1)
    StyleStatusRunning = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("214")).Bold(true).Padding(0, 1)
    StyleStatusCwd     = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("86")).Padding(0, 1)
)

const (
    BorderWidth     = 2 // left + right border chars
    BorderHeight    = 2 // top + bottom border chars
    StatusBarHeight = 1
    TermWidthRatio  = 0.60 // terminal panel takes 60% in horizontal split
)
```

### `tui/terminal_panel.go`

```go
type TerminalPanel struct {
    viewport   viewport.Model
    input      textinput.Model
    spinner    spinner.Model
    running    bool
    history    []string // submitted commands, oldest first
    historyIdx int      // -1 = not navigating; 0 = newest
    content    []string // accumulated rendered lines for viewport
    width      int      // inner width (post-border)
    height     int      // inner height (post-border)
    cwd        string   // updated from MsgCommandFinished.State.Cwd
}
```

Key behaviors:
- `Update(MsgCommandStarted)` → set `running=true`, append `prompt+cmd` line, start spinner tick
- `Update(MsgCommandFinished)` → set `running=false`, append stdout/stderr/exit-code lines, `GotoBottom()`, reset input
- `Update(tea.KeyMsg{Up})` → navigate history newest-first, populate input
- `Update(tea.KeyMsg{Down})` → navigate history toward older, clear on overshoot
- Any other keypress resets `historyIdx = -1`
- `View()` = `viewport` + separator line (`─` × width) + spinner-or-prompt+input

### `tui/log_panel.go`

```go
type LogPanel struct {
    viewport   viewport.Model
    entries    []LogEntry
    autoScroll bool // true = follow tail; false = user scrolled up
    width      int
    height     int
}

const MaxLogEntries = 1000
```

Key behaviors:
- `Update(MsgLogEntry)` → append entry, trim to `MaxLogEntries`, `rerender()`, `GotoBottom()` if `autoScroll`
- `Update(scroll events)` → detect manual scroll by comparing `YOffset` before/after; re-enable `autoScroll` when `viewport.AtBottom()`
- `rerender()` → rebuilds full viewport content string from `entries` slice; called on append and on `SetSize`
- `renderEntry(e)` → `timestamp + level-tag + color-styled message`, truncated to `width - 13`

### `tui/model.go`

```go
type SplitMode int

const (
    SplitHorizontal SplitMode = iota // left 60% | right 40%
    SplitVertical                    // top 50% / bottom 50%
)

type Model struct {
    sandbox       *sandbox.Sandbox
    logCh         <-chan LogEntry
    cancelRun     context.CancelFunc  // non-nil while a command is running; called on Ctrl+C
    terminal      TerminalPanel
    logPanel      LogPanel
    splitMode     SplitMode
    totalWidth    int
    totalHeight   int
    isolationName string
    cmdCount      int
    running       bool
}

type Config struct {
    Sandbox       *sandbox.Sandbox
    LogCh         <-chan LogEntry
    SplitMode     SplitMode
    IsolationName string
}
```

---

## Layout Calculation

```
totalHeight - StatusBarHeight(1) = contentHeight

Horizontal split (default, 60/40):
  termOuter  = int(float64(totalWidth) * TermWidthRatio)   // 60%
  logOuter   = totalWidth - termOuter                      // 40%, handles odd widths
  termInnerW = termOuter - BorderWidth(2)
  logInnerW  = logOuter  - BorderWidth(2)
  innerH     = contentHeight - BorderHeight(2)
  terminal.SetSize(termInnerW, innerH)
  logPanel.SetSize(logInnerW,  innerH)

  Within TerminalPanel.SetSize:
    viewport.Height = innerH - 3   (separator row + input row + spinner/prompt row)
    input.Width     = innerW - len(promptPrefix) - 2

Vertical split (--vertical):
  topH    = contentHeight / 2
  botH    = contentHeight - topH
  innerW  = totalWidth - BorderWidth(2)
  terminal.SetSize(innerW, topH - BorderHeight(2))
  logPanel.SetSize(innerW, botH - BorderHeight(2))
```

Guard against degenerate sizes (tiny terminal):
```go
func clamp(v, min int) int {
    if v < min { return min }
    return v
}
// Applied in every SetSize before assigning to viewport.
```

---

## Data Flow

```
User types a command and presses Enter
        │
        ▼
model.Update(tea.KeyMsg{Enter})
  ├─ ctx, cancel = context.WithCancel(context.Background())
  ├─ m.cancelRun = cancel
  ├─ m.running = true
  ├─ m.cmdCount++
  ├─ terminal.PushHistory(cmd)
  ├─ emit MsgCommandStarted → terminal shows spinner + echoes prompt+cmd
  └─ issue tea.Cmd: runCommand(sandbox, cmd, ctx, cancel)
                              │
                        [bubbletea goroutine]
                              │
                        sb.RunContext(ctx, cmd)
                          ├─ OnCommand(cmd)      → nonBlocking(logCh, CMD entry)
                          ├─ [executor runs]
                          └─ OnResult(result)    → nonBlocking(logCh, Result entry)
                                                 → nonBlocking(logCh, File entries × N)
                                                 → nonBlocking(logCh, Metric entry)
                              │
                        returns MsgCommandFinished
                              │
                        model.Update(MsgCommandFinished)
                          ├─ m.running = false, m.cancelRun = nil
                          └─ terminal.Update() → appends stdout/stderr/exit-code, resets input

User presses Ctrl+C while running:
  model.Update(KeyCtrlC) → m.cancelRun() → sb.RunContext ctx cancelled
                                         → Run() surfaces as error/timeout result
                                         → MsgCommandFinished arrives normally

Concurrently (always running):
  listenLogCh blocks on logCh
      └─ entry arrives → returns MsgLogEntry
      model.Update(MsgLogEntry)
          ├─ logPanel.Update() → append + rerender + maybe GotoBottom
          └─ re-issue listenLogCh for next entry
```

---

## Status Bar (1 row, pinned to bottom)

```
[ ~/workspace/path ]  [ isolation=none  cmds: 5 ]  <gap>  [ RUNNING... ]
  ^^^ cwd (green)       ^^^ center info (gray bg)           ^^^ amber, only when running
```

Rendered with `lipgloss.JoinHorizontal`. The gap is filled with `StyleStatusBar`-colored spaces
so the full row is covered. Width is computed as `totalWidth - lipgloss.Width(left+mid+right)`.

---

## main.go Rewrite

```go
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
    vertical := flag.Bool("vertical", false, "stack panels top/bottom instead of left/right")
    flag.Parse()

    logCh := make(chan tui.LogEntry, 256)

    sb, err := sandbox.New(sandbox.Options{
        OnCommand: func(cmd string) {
            nonBlocking(logCh, tui.LogEntry{At: time.Now(), Level: tui.LogLevelCMD, Message: cmd})
        },
        OnResult: func(r sandbox.ExecutionResult) {
            if r.Error != nil {
                nonBlocking(logCh, tui.LogEntry{At: time.Now(), Level: tui.LogLevelError, Message: r.Error.Error()})
                return
            }
            icon := "✓"
            if r.ExitCode != 0 { icon = "✗" }
            nonBlocking(logCh, tui.LogEntry{
                At: time.Now(), Level: tui.LogLevelResult,
                Message: fmt.Sprintf("%s exit=%d duration=%s", icon, r.ExitCode, r.Duration.Round(time.Millisecond)),
            })
            for _, f := range r.FilesCreated  { nonBlocking(logCh, tui.LogEntry{At: time.Now(), Level: tui.LogLevelFile, Message: "+ " + f}) }
            for _, f := range r.FilesModified { nonBlocking(logCh, tui.LogEntry{At: time.Now(), Level: tui.LogLevelFile, Message: "~ " + f}) }
            for _, f := range r.FilesDeleted  { nonBlocking(logCh, tui.LogEntry{At: time.Now(), Level: tui.LogLevelFile, Message: "- " + f}) }
            if r.CPUTime > 0 || r.MemoryPeakMB > 0 {
                nonBlocking(logCh, tui.LogEntry{
                    At: time.Now(), Level: tui.LogLevelMetric,
                    Message: fmt.Sprintf("cpu=%s mem=%dMB", r.CPUTime.Round(time.Millisecond), r.MemoryPeakMB),
                })
            }
        },
        OnViolation: func(v sandbox.PolicyViolation) {
            word := "logged"
            if v.Blocked { word = "blocked" }
            nonBlocking(logCh, tui.LogEntry{
                At: time.Now(), Level: tui.LogLevelViolation,
                Message: fmt.Sprintf("policy %s: %s — %s", word, v.Type, v.Detail),
            })
        },
    })
    if err != nil {
        fmt.Fprintf(os.Stderr, "error: %v\n", err)
        os.Exit(1)
    }
    defer sb.Close()

    logCh <- tui.LogEntry{
        At: time.Now(), Level: tui.LogLevelSandbox,
        Message: fmt.Sprintf("sandbox initialized, isolation=%s", sb.Isolation().Name()),
    }

    split := tui.SplitHorizontal
    if *vertical { split = tui.SplitVertical }

    model := tui.NewModel(tui.Config{
        Sandbox:       sb,
        LogCh:         logCh,
        SplitMode:     split,
        IsolationName: sb.Isolation().Name(),
    })

    p := tea.NewProgram(model,
        tea.WithAltScreen(),
        tea.WithMouseCellMotion(), // scroll log panel with mouse wheel
    )
    if _, err := p.Run(); err != nil {
        fmt.Fprintf(os.Stderr, "tui error: %v\n", err)
        os.Exit(1)
    }
}

// nonBlocking sends to a buffered channel without ever blocking.
// Dropping entries is preferable to blocking a sandbox hook callback.
func nonBlocking(ch chan<- tui.LogEntry, e tui.LogEntry) {
    select {
    case ch <- e:
    default:
    }
}
```

---

## Async Tea Commands

```go
// listenLogCh blocks waiting for one LogEntry, returns it as MsgLogEntry.
// The Update function re-issues this after every receipt to keep the channel draining.
func listenLogCh(ch <-chan LogEntry) tea.Cmd {
    return func() tea.Msg {
        return MsgLogEntry{Entry: <-ch}
    }
}

// runCommand executes cmd via RunContext so ctx cancellation (Ctrl+C) propagates.
// cancel is always called when done to release the context resources.
func runCommand(sb *sandbox.Sandbox, cmd string, ctx context.Context, cancel context.CancelFunc) tea.Cmd {
    return func() tea.Msg {
        defer cancel()
        result := sb.RunContext(ctx, cmd)
        state := *sb.State() // value copy — safe after Run() returns
        return MsgCommandFinished{Result: result, State: state}
    }
}
```

---

## Critical Gotchas

| # | Issue | Resolution |
|---|-------|------------|
| 1 | bubbletea raw mode vs. sandbox shell | Safe — ShellExecutor is pure in-process (mvdan.cc/sh). Never use NativeExecutor from the TUI; it would inherit raw-mode stdin. |
| 2 | Ctrl+C must interrupt sandbox | Add `RunContext(ctx, cmd)` to sandbox. Merge caller ctx with internal timeout via `context.WithDeadline`. |
| 3 | sandbox.Run() not goroutine-safe for concurrent calls | TUI serializes via `running` bool. bubbletea's Update is single-threaded — only one `runCommand` tea.Cmd is live at a time. |
| 4 | Hook blocks if logCh is full | Use `nonBlocking()` (select+default) everywhere. 256-buffer absorbs bursts; drop is safe since output is also in terminal panel. |
| 5 | Stale viewport on resize | Call `rerender()` / `viewport.SetContent()` inside every `SetSize()` so the next `View()` uses fresh wrapping. |
| 6 | Odd totalWidth in 60/40 split | `logOuter = totalWidth - termOuter` (not `0.4*totalWidth`). Prevents 1-col gap from float rounding. |
| 7 | sandbox.State() pointer unsafe across goroutines | `runCommand` copies by value: `state := *sb.State()` before returning `MsgCommandFinished`. |
| 8 | Negative inner dimensions on tiny terminals | Guard with `clamp(v, 1)` in every `SetSize`. Prevents viewport panics on terminals narrower than 6 cols. |
| 9 | Auto-scroll vs. manual scroll in log panel | Compare `YOffset` before/after `viewport.Update()`. Decrease = manual scroll up → `autoScroll=false`. `viewport.AtBottom()` → re-enable. |
| 10 | Mouse events reaching both panels | Route mouse events based on cursor X position vs. panel boundary in horizontal split; Y position in vertical split. |

---

## Implementation Steps

### Step 1 — Add dependencies
```
go get github.com/charmbracelet/bubbletea@v1.3.5 \
       github.com/charmbracelet/bubbles@v0.20.0 \
       github.com/charmbracelet/lipgloss@v1.1.0
go mod tidy
```
Verify: `go build ./...` succeeds.

### Step 2 — `tui/events.go`
All `LogLevel` constants, `LogEntry` struct, and the three `tea.Msg` types. No intra-project imports yet. `go build ./tui/` must pass.

### Step 3 — `tui/styles.go`
All lipgloss style variables and layout constants (`BorderWidth`, `TermWidthRatio`, etc.). `go build ./tui/` must pass.

### Step 4 — `tui/log_panel.go`
`LogPanel`, `NewLogPanel`, `SetSize`, `Update` (MsgLogEntry only), `View`, `renderEntry`. Verify rendering logic manually before wiring into the model.

### Step 5 — `tui/terminal_panel.go`
`TerminalPanel`, `NewTerminalPanel`, `SetSize`, `Update`, `View`. Start with basic input + viewport. History navigation and Ctrl+C wired in Step 8.

### Step 6 — `tui/model.go`
Root `Model`, `NewModel`, `Init`, `Update`, `View`. Add `recalcLayout` with all four dimension helpers. Add status bar. Wire `listenLogCh` and `runCommand`.

### Step 7 — Rewrite `main.go`
Add `--vertical` flag. Wire sandbox hooks to `logCh`. Launch `tea.NewProgram` with `WithAltScreen()` and `WithMouseCellMotion()`. Run and verify end-to-end: commands execute, output shows in terminal panel, log entries appear in log panel, status bar tracks cwd.

### Step 8 — Polish terminal panel
- Up/Down arrow history navigation (`historyIdx` logic)
- `exit` / `quit` command → `tea.Quit`
- Ctrl+C → `m.cancelRun()` if running

### Step 9 — Polish log panel
- Auto-scroll / manual-scroll detection
- `MaxLogEntries = 1000` cap with slice trimming
- Mouse wheel routing (horizontal: route by X; vertical: route by Y)

### Step 10 — Add `RunContext` to sandbox
Add `ctx context.Context` field to `Sandbox`. Add `SetContext(ctx)` and `RunContext(ctx, cmd)`. Update `runCommand` in `model.go` to call `RunContext`. Verify Ctrl+C terminates the running command and surfaces `MsgCommandFinished` with an error result.

### Step 11 — Resize stress test
Rapidly resize terminal. Verify:
- No panics (all sizes clamped to ≥1)
- No gaps or overlaps between panels
- Status bar stays exactly 1 row
- Viewport content rewraps immediately

### Step 12 — Integration test
Write `tui/model_test.go` using bubbletea's `tea.NewTestProgram`. Simulate keystrokes (`echo hello` + Enter) and assert rendered output contains the prompt, the stdout, and a log entry. Use `tea.WithoutRenderer()` for headless testing.

---

## Log Entry Reference

| Level     | Prefix | Color       | When emitted |
|-----------|--------|-------------|--------------|
| CMD       | `CMD  →` | blue      | `OnCommand` hook fires |
| Result ✓  | `RES  ✓` | green     | `OnResult`, exit=0 |
| Result ✗  | `RES  ✗` | red       | `OnResult`, exit≠0 |
| File      | `FILE +/~/−` | yellow | `OnResult`, file change lists |
| Metric    | `METR` | gray        | `OnResult`, when cpu or mem > 0 |
| Violation | `VIOL ⚠` | red+bold  | `OnViolation` hook fires |
| Sandbox   | `SBOX` | cyan        | Lifecycle: init, reset, close |
| Error     | `ERR ` | red+bold    | `OnResult`, r.Error != nil |

Timestamp format: `15:04:05.000` (12 chars + 1 space = 13 chars prefix before message body).
Log lines longer than `panelWidth - 13` are truncated to prevent lipgloss layout breakage.
