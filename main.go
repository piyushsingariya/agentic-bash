package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/chzyer/readline"
	"github.com/spf13/cobra"

	sbfs "github.com/piyushsingariya/agentic-bash/fs"
	"github.com/piyushsingariya/agentic-bash/sandbox"
	"github.com/piyushsingariya/agentic-bash/tui"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

// sandboxFlags holds the common sandbox configuration flags shared across
// the run, shell, snapshot, and restore subcommands.
type sandboxFlags struct {
	timeout   string
	memoryMB  int
	cpuPct    float64
	network   string
	allowlist string
	isolation string
	env       []string
	workdir   string
	outputMB  int
}

func (f *sandboxFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.timeout, "timeout", "30s", "wall-clock timeout per command (e.g. 30s, 2m)")
	cmd.Flags().IntVar(&f.memoryMB, "memory", 0, "peak memory cap in MiB (Linux cgroupv2 only; 0=unlimited)")
	cmd.Flags().Float64Var(&f.cpuPct, "cpu", 0, "CPU quota as percent of one core (Linux only; 0=unlimited)")
	cmd.Flags().StringVar(&f.network, "network", "allow", "network mode: allow | deny | allowlist")
	cmd.Flags().StringVar(&f.allowlist, "allowlist", "", "comma-separated domains/CIDRs for --network=allowlist")
	cmd.Flags().StringVar(&f.isolation, "isolation", "auto", "isolation strategy: auto | namespace | landlock | none")
	cmd.Flags().StringArrayVar(&f.env, "env", nil, "KEY=VALUE pairs added to the sandbox environment (repeatable)")
	cmd.Flags().StringVar(&f.workdir, "workdir", "", "initial working directory inside the sandbox")
	cmd.Flags().IntVar(&f.outputMB, "output-cap", 0, "combined stdout+stderr cap in MiB (0=unlimited)")
}

func (f *sandboxFlags) toOptions() (sandbox.Options, error) {
	timeout, err := time.ParseDuration(f.timeout)
	if err != nil {
		return sandbox.Options{}, fmt.Errorf("invalid --timeout %q: %w", f.timeout, err)
	}

	netMode := sandbox.NetworkAllow
	switch strings.ToLower(f.network) {
	case "deny":
		netMode = sandbox.NetworkDeny
	case "allowlist":
		netMode = sandbox.NetworkAllowlist
	case "allow", "":
	default:
		return sandbox.Options{}, fmt.Errorf("unknown --network %q (want allow|deny|allowlist)", f.network)
	}

	isoLevel := sandbox.IsolationAuto
	switch strings.ToLower(f.isolation) {
	case "auto", "":
	case "namespace":
		isoLevel = sandbox.IsolationNamespace
	case "landlock":
		isoLevel = sandbox.IsolationLandlock
	case "none":
		isoLevel = sandbox.IsolationNone
	default:
		return sandbox.Options{}, fmt.Errorf("unknown --isolation %q (want auto|namespace|landlock|none)", f.isolation)
	}

	envMap := map[string]string{}
	for _, pair := range f.env {
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			return sandbox.Options{}, fmt.Errorf("--env %q: expected KEY=VALUE", pair)
		}
		envMap[k] = v
	}

	var allowlistParsed []string
	if f.allowlist != "" {
		for _, item := range strings.Split(f.allowlist, ",") {
			if t := strings.TrimSpace(item); t != "" {
				allowlistParsed = append(allowlistParsed, t)
			}
		}
	}

	opts := sandbox.Options{
		Isolation: isoLevel,
		Limits: sandbox.ResourceLimits{
			Timeout:       timeout,
			MaxMemoryMB:   f.memoryMB,
			MaxCPUPercent: f.cpuPct,
			MaxOutputMB:   f.outputMB,
		},
		Network: sandbox.NetworkPolicy{
			Mode:      netMode,
			Allowlist: allowlistParsed,
		},
		WorkDir: f.workdir,
	}
	if len(envMap) > 0 {
		opts.Env = envMap
	}
	return opts, nil
}

// rootCmd builds the root cobra command.
func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "agentic-bash",
		Short: "Embedded Go sandbox for AI agents",
		Long: `agentic-bash provides a stateful, isolated bash execution environment
for AI agents — no Docker, no root, no external daemons required.`,
		SilenceUsage: true,
	}
	root.AddCommand(runCmd(), shellCmd(), snapshotCmd(), restoreCmd(), tuiCmd())
	return root
}

// ── run ──────────────────────────────────────────────────────────────────────

func runCmd() *cobra.Command {
	var sf sandboxFlags
	var inline string

	cmd := &cobra.Command{
		Use:   "run [script.sh]",
		Short: "Execute a script file or inline command in the sandbox",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := sf.toOptions()
			if err != nil {
				return err
			}

			var script string
			switch {
			case inline != "":
				script = inline
			case len(args) == 1:
				data, readErr := os.ReadFile(args[0])
				if readErr != nil {
					return fmt.Errorf("read script %q: %w", args[0], readErr)
				}
				script = string(data)
			default:
				return fmt.Errorf("provide a script file or --cmd")
			}

			sb, err := sandbox.New(opts)
			if err != nil {
				return fmt.Errorf("create sandbox: %w", err)
			}
			defer sb.Close()

			exitCode, runErr := sb.RunStream(cmd.Context(), script, os.Stdout, os.Stderr)
			if runErr != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
			}
			os.Exit(exitCode)
			return nil
		},
	}

	sf.register(cmd)
	cmd.Flags().StringVar(&inline, "cmd", "", "inline command string to execute (alternative to script file)")
	return cmd
}

// ── shell ─────────────────────────────────────────────────────────────────────

func shellCmd() *cobra.Command {
	var sf sandboxFlags

	cmd := &cobra.Command{
		Use:   "shell",
		Short: "Start an interactive REPL session inside the sandbox",
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := sf.toOptions()
			if err != nil {
				return err
			}

			sb, err := sandbox.New(opts)
			if err != nil {
				return fmt.Errorf("create sandbox: %w", err)
			}
			defer sb.Close()

			return runREPL(sb)
		},
	}

	sf.register(cmd)
	return cmd
}

// runREPL runs the interactive readline REPL loop.
func runREPL(sb *sandbox.Sandbox) error {
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          "$ ",
		HistoryFile:     "/tmp/agentic-bash-history",
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		return fmt.Errorf("readline: %w", err)
	}
	defer rl.Close()

	fmt.Printf("agentic-bash shell  (isolation=%s, workdir=%s)\n",
		sb.Isolation().Name(), sb.State().Cwd)
	fmt.Print("Type commands, %reset, %snapshot <file>, %restore <file>, or Ctrl-D to exit.\n")

	for {
		line, err := rl.Readline()
		if err != nil { // EOF or Ctrl-D
			fmt.Println()
			return nil
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Meta-commands.
		switch {
		case line == "%reset":
			sb.Reset()
			fmt.Println("sandbox state reset")
			continue
		case strings.HasPrefix(line, "%snapshot "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "%snapshot "))
			if err := doSnapshot(sb, path); err != nil {
				fmt.Fprintf(os.Stderr, "snapshot error: %v\n", err)
			} else {
				fmt.Printf("snapshot saved to %s\n", path)
			}
			continue
		case strings.HasPrefix(line, "%restore "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "%restore "))
			if err := doRestore(sb, path); err != nil {
				fmt.Fprintf(os.Stderr, "restore error: %v\n", err)
			} else {
				fmt.Printf("state restored from %s\n", path)
			}
			continue
		}

		start := time.Now()
		exitCode, runErr := sb.RunStream(context.Background(), line, os.Stdout, os.Stderr)
		dur := time.Since(start).Round(time.Millisecond)

		if runErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
		}
		fmt.Fprintf(os.Stderr, "[exit=%d %s]\n", exitCode, dur)
	}
}

// ── snapshot ──────────────────────────────────────────────────────────────────

func snapshotCmd() *cobra.Command {
	var sf sandboxFlags
	var script, outFile string

	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Run a command then save the resulting sandbox state to a file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if outFile == "" {
				return fmt.Errorf("--out is required")
			}
			opts, err := sf.toOptions()
			if err != nil {
				return err
			}

			sb, err := sandbox.New(opts)
			if err != nil {
				return fmt.Errorf("create sandbox: %w", err)
			}
			defer sb.Close()

			if script != "" {
				if exitCode, runErr := sb.RunStream(cmd.Context(), script, os.Stdout, os.Stderr); runErr != nil || exitCode != 0 {
					if runErr != nil {
						return runErr
					}
					return fmt.Errorf("command exited with code %d", exitCode)
				}
			}

			if err := doSnapshot(sb, outFile); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "snapshot saved to %s\n", outFile)
			return nil
		},
	}

	sf.register(cmd)
	cmd.Flags().StringVar(&script, "cmd", "", "command to run before snapshotting (optional)")
	cmd.Flags().StringVar(&outFile, "out", "", "output file path for the snapshot (required)")
	return cmd
}

// ── restore ───────────────────────────────────────────────────────────────────

func restoreCmd() *cobra.Command {
	var sf sandboxFlags
	var inFile string

	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Load a snapshot and attach an interactive shell",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if inFile == "" {
				return fmt.Errorf("--in is required")
			}
			opts, err := sf.toOptions()
			if err != nil {
				return err
			}

			sb, err := sandbox.New(opts)
			if err != nil {
				return fmt.Errorf("create sandbox: %w", err)
			}
			defer sb.Close()

			if err := doRestore(sb, inFile); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "state restored from %s\n", inFile)
			return runREPL(sb)
		},
	}

	sf.register(cmd)
	cmd.Flags().StringVar(&inFile, "in", "", "snapshot file to restore from (required)")
	return cmd
}

// ── tui ───────────────────────────────────────────────────────────────────────

func tuiCmd() *cobra.Command {
	var vertical bool

	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Launch the interactive split-panel TUI (terminal + log)",
		RunE: func(_ *cobra.Command, _ []string) error {
			logCh := make(chan tui.LogEntry, 256)

			sb, err := sandbox.New(sandbox.Options{
				OnCommand: func(c string) {
					nonBlocking(logCh, tui.LogEntry{At: time.Now(), Level: tui.LogLevelCMD, Message: c})
				},
				OnResult: func(r sandbox.ExecutionResult) {
					if r.Error != nil {
						nonBlocking(logCh, tui.LogEntry{At: time.Now(), Level: tui.LogLevelError, Message: r.Error.Error()})
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
						nonBlocking(logCh, tui.LogEntry{At: time.Now(), Level: tui.LogLevelFile, Message: "+ " + f})
					}
					for _, f := range r.FilesModified {
						nonBlocking(logCh, tui.LogEntry{At: time.Now(), Level: tui.LogLevelFile, Message: "~ " + f})
					}
					for _, f := range r.FilesDeleted {
						nonBlocking(logCh, tui.LogEntry{At: time.Now(), Level: tui.LogLevelFile, Message: "- " + f})
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
				return fmt.Errorf("create sandbox: %w", err)
			}
			defer sb.Close()

			logCh <- tui.LogEntry{
				At:      time.Now(),
				Level:   tui.LogLevelSandbox,
				Message: fmt.Sprintf("sandbox initialized, isolation=%s, workdir=%s", sb.Isolation().Name(), sb.State().Cwd),
			}

			split := tui.SplitHorizontal
			if vertical {
				split = tui.SplitVertical
			}

			model := tui.NewModel(tui.Config{
				Sandbox:       sb,
				LogCh:         logCh,
				SplitMode:     split,
				IsolationName: sb.Isolation().Name(),
			})

			p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
			if _, err := p.Run(); err != nil {
				return fmt.Errorf("tui error: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&vertical, "vertical", false, "stack panels top/bottom instead of left/right")
	return cmd
}

// ── helpers ───────────────────────────────────────────────────────────────────

func doSnapshot(sb *sandbox.Sandbox, path string) error {
	data, err := sbfs.Snapshot(sb.FS())
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

func doRestore(sb *sandbox.Sandbox, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read snapshot %q: %w", path, err)
	}
	return sbfs.Restore(sb.FS(), data)
}

// nonBlocking sends to a buffered channel without blocking.
func nonBlocking(ch chan<- tui.LogEntry, e tui.LogEntry) {
	select {
	case ch <- e:
	default:
	}
}

