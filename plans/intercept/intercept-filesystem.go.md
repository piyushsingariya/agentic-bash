# executor/intercept/filesystem.go

Rich filesystem command shims. Replaces handleLs. Adds stat, cat, head, tail, wc.

```go
package intercept

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mvdan.cc/sh/v3/interp"
	"github.com/piyushsingariya/agentic-bash/internal/pathmap"
)

// NewFilesystemInterceptors returns all filesystem-related command interceptors.
func NewFilesystemInterceptors(cfg Config) []Interceptor {
	return []Interceptor{
		&lsInterceptor{cfg: cfg},
		&statInterceptor{cfg: cfg},
		&catInterceptor{cfg: cfg},
		&headInterceptor{cfg: cfg},
		&tailInterceptor{cfg: cfg},
		&wcInterceptor{cfg: cfg},
	}
}

// ─── ls ──────────────────────────────────────────────────────────────────────

type lsInterceptor struct{ cfg Config }

func (l *lsInterceptor) Name() string { return "ls" }
func (l *lsInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	opts, paths := parseLsArgs(args[1:])

	if len(paths) == 0 {
		paths = []string{hc.Dir} // default: current real dir
	}

	for i, p := range paths {
		// Translate virtual → real (idempotent if already real).
		realP := pathmap.VirtualToReal(l.cfg.SandboxRoot, p)
		virtualP := pathmap.RealToVirtual(l.cfg.SandboxRoot, realP)

		if opts.dirOnly {
			// -d: show the directory itself, not its contents
			info, err := os.Lstat(realP)
			if err != nil {
				fmt.Fprintf(hc.Stderr, "ls: cannot access '%s': No such file or directory\n", virtualP)
				continue
			}
			printEntry(hc.Stdout, realP, info, opts)
			continue
		}

		if len(paths) > 1 && i > 0 {
			fmt.Fprintln(hc.Stdout)
		}
		if len(paths) > 1 {
			fmt.Fprintf(hc.Stdout, "%s:\n", virtualP)
		}

		if err := lsDir(hc.Stdout, hc.Stderr, realP, virtualP, opts, 0); err != nil {
			fmt.Fprintf(hc.Stderr, "ls: cannot access '%s': %v\n", virtualP, err)
		}
	}
	return nil
}

type lsOpts struct {
	all       bool // -a
	almostAll bool // -A
	long      bool // -l
	human     bool // -h
	recursive bool // -R
	dirOnly   bool // -d
	oneCol    bool // -1
	typeInd   bool // -F
	sortTime  bool // -t
	sortSize  bool // -S
	reverse   bool // -r
}

func parseLsArgs(args []string) (lsOpts, []string) {
	var o lsOpts
	var paths []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") && a != "-" {
			flags := strings.TrimLeft(a, "-")
			for _, c := range flags {
				switch c {
				case 'a':
					o.all = true
				case 'A':
					o.almostAll = true
				case 'l':
					o.long = true
				case 'h':
					o.human = true
				case 'R':
					o.recursive = true
				case 'd':
					o.dirOnly = true
				case '1':
					o.oneCol = true
				case 'F':
					o.typeInd = true
				case 't':
					o.sortTime = true
				case 'S':
					o.sortSize = true
				case 'r':
					o.reverse = true
				}
			}
		} else {
			paths = append(paths, a)
		}
	}
	return o, paths
}

func lsDir(stdout, stderr io.Writer, realDir, virtualDir string, opts lsOpts, depth int) error {
	entries, err := os.ReadDir(realDir)
	if err != nil {
		return err
	}

	// Filter hidden files.
	filtered := entries[:0]
	for _, e := range entries {
		name := e.Name()
		if !opts.all && !opts.almostAll && strings.HasPrefix(name, ".") {
			continue
		}
		if opts.almostAll && (name == "." || name == "..") {
			continue
		}
		filtered = append(filtered, e)
	}

	// Sort.
	if opts.sortTime {
		sort.Slice(filtered, func(i, j int) bool {
			iInfo, _ := filtered[i].Info()
			jInfo, _ := filtered[j].Info()
			if iInfo == nil || jInfo == nil {
				return false
			}
			return iInfo.ModTime().After(jInfo.ModTime())
		})
	} else if opts.sortSize {
		sort.Slice(filtered, func(i, j int) bool {
			iInfo, _ := filtered[i].Info()
			jInfo, _ := filtered[j].Info()
			if iInfo == nil || jInfo == nil {
				return false
			}
			return iInfo.Size() > jInfo.Size()
		})
	} else {
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].Name() < filtered[j].Name()
		})
	}

	if opts.reverse {
		for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
			filtered[i], filtered[j] = filtered[j], filtered[i]
		}
	}

	for _, e := range filtered {
		info, err := e.Info()
		if err != nil {
			continue
		}
		realPath := filepath.Join(realDir, e.Name())
		printEntry(stdout, realPath, info, opts)
	}

	// Recursive: descend into subdirectories.
	if opts.recursive {
		for _, e := range filtered {
			if e.IsDir() {
				subReal := filepath.Join(realDir, e.Name())
				subVirtual := filepath.Join(virtualDir, e.Name())
				fmt.Fprintf(stdout, "\n%s:\n", subVirtual)
				_ = lsDir(stdout, stderr, subReal, subVirtual, opts, depth+1)
			}
		}
	}

	return nil
}

func printEntry(w io.Writer, realPath string, info fs.FileInfo, opts lsOpts) {
	name := info.Name()

	if opts.typeInd {
		switch {
		case info.IsDir():
			name += "/"
		case info.Mode()&0o111 != 0:
			name += "*"
		case info.Mode()&fs.ModeSymlink != 0:
			name += "@"
		}
	}

	if opts.long {
		sizeStr := fmt.Sprintf("%8d", info.Size())
		if opts.human {
			sizeStr = fmt.Sprintf("%8s", humanBytes(info.Size()))
		}
		fmt.Fprintf(w, "%s %s %s %s\n",
			info.Mode().String(),
			sizeStr,
			info.ModTime().Format("Jan _2 15:04"),
			name,
		)
	} else if opts.oneCol {
		fmt.Fprintln(w, name)
	} else {
		fmt.Fprintln(w, name)
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for n := n / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGTPE"[exp])
}

// ─── stat (command) ───────────────────────────────────────────────────────────

type statInterceptor struct{ cfg Config }

func (s *statInterceptor) Name() string { return "stat" }
func (s *statInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	if len(args) < 2 {
		fmt.Fprintln(hc.Stderr, "stat: missing operand")
		return interp.NewExitStatus(1)
	}

	exitCode := uint8(0)
	for _, p := range args[1:] {
		if strings.HasPrefix(p, "-") {
			// Skip flags like -c for now; full format strings are complex.
			continue
		}
		realP := pathmap.VirtualToReal(s.cfg.SandboxRoot, p)
		virtualP := pathmap.RealToVirtual(s.cfg.SandboxRoot, realP)

		info, err := os.Lstat(realP)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "stat: cannot statx '%s': No such file or directory\n", virtualP)
			exitCode = 1
			continue
		}

		fmt.Fprintf(hc.Stdout, "  File: %s\n", virtualP)
		fmt.Fprintf(hc.Stdout, "  Size: %-15d Blocks: %-10d IO Block: 4096\n",
			info.Size(), (info.Size()+511)/512)
		fileType := "regular file"
		if info.IsDir() {
			fileType = "directory"
		} else if info.Mode()&fs.ModeSymlink != 0 {
			fileType = "symbolic link"
		}
		fmt.Fprintf(hc.Stdout, "  Type: %s\n", fileType)
		fmt.Fprintf(hc.Stdout, "Access: (%04o/%s)\n", info.Mode().Perm(), info.Mode())
		fmt.Fprintf(hc.Stdout, "Modify: %s\n", info.ModTime().Format(time.RFC3339))
	}

	if exitCode != 0 {
		return interp.NewExitStatus(exitCode)
	}
	return nil
}

// ─── cat ─────────────────────────────────────────────────────────────────────

type catInterceptor struct{ cfg Config }

func (c *catInterceptor) Name() string { return "cat" }
func (c *catInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	if len(args) < 2 {
		// cat with no args reads stdin — let it fall through to default exec.
		// We can't forward to next here; instead just drain stdin.
		_, _ = io.Copy(hc.Stdout, hc.Stdin)
		return nil
	}

	exitCode := uint8(0)
	showLineNums := false
	for _, a := range args[1:] {
		if a == "-n" {
			showLineNums = true
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue // ignore other flags for now
		}
		realP := pathmap.VirtualToReal(c.cfg.SandboxRoot, a)
		virtualP := pathmap.RealToVirtual(c.cfg.SandboxRoot, realP)

		f, err := os.Open(realP)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "cat: %s: No such file or directory\n", virtualP)
			exitCode = 1
			continue
		}
		if showLineNums {
			sc := bufio.NewScanner(f)
			line := 1
			for sc.Scan() {
				fmt.Fprintf(hc.Stdout, "%6d\t%s\n", line, sc.Text())
				line++
			}
		} else {
			_, _ = io.Copy(hc.Stdout, f)
		}
		f.Close()
	}

	if exitCode != 0 {
		return interp.NewExitStatus(exitCode)
	}
	return nil
}

// ─── head ────────────────────────────────────────────────────────────────────

type headInterceptor struct{ cfg Config }

func (h *headInterceptor) Name() string { return "head" }
func (h *headInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	n, paths := parseNFlag(args[1:], 10)

	exitCode := uint8(0)
	for _, p := range paths {
		realP := pathmap.VirtualToReal(h.cfg.SandboxRoot, p)
		virtualP := pathmap.RealToVirtual(h.cfg.SandboxRoot, realP)
		f, err := os.Open(realP)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "head: cannot open '%s': No such file or directory\n", virtualP)
			exitCode = 1
			continue
		}
		printNLines(hc.Stdout, f, n)
		f.Close()
	}
	if exitCode != 0 {
		return interp.NewExitStatus(exitCode)
	}
	return nil
}

// ─── tail ────────────────────────────────────────────────────────────────────

type tailInterceptor struct{ cfg Config }

func (t *tailInterceptor) Name() string { return "tail" }
func (t *tailInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	n, paths := parseNFlag(args[1:], 10)

	exitCode := uint8(0)
	for _, p := range paths {
		realP := pathmap.VirtualToReal(t.cfg.SandboxRoot, p)
		virtualP := pathmap.RealToVirtual(t.cfg.SandboxRoot, realP)
		f, err := os.Open(realP)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "tail: cannot open '%s': No such file or directory\n", virtualP)
			exitCode = 1
			continue
		}
		printLastNLines(hc.Stdout, f, n)
		f.Close()
	}
	if exitCode != 0 {
		return interp.NewExitStatus(exitCode)
	}
	return nil
}

// ─── wc ──────────────────────────────────────────────────────────────────────

type wcInterceptor struct{ cfg Config }

func (w *wcInterceptor) Name() string { return "wc" }
func (w *wcInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	countLines, countWords, countBytes := false, false, false
	var paths []string
	for _, a := range args[1:] {
		switch a {
		case "-l":
			countLines = true
		case "-w":
			countWords = true
		case "-c":
			countBytes = true
		default:
			if !strings.HasPrefix(a, "-") {
				paths = append(paths, a)
			}
		}
	}
	// Default: all three
	if !countLines && !countWords && !countBytes {
		countLines, countWords, countBytes = true, true, true
	}

	exitCode := uint8(0)
	for _, p := range paths {
		realP := pathmap.VirtualToReal(w.cfg.SandboxRoot, p)
		virtualP := pathmap.RealToVirtual(w.cfg.SandboxRoot, realP)
		data, err := os.ReadFile(realP)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "wc: %s: No such file or directory\n", virtualP)
			exitCode = 1
			continue
		}
		var parts []string
		if countLines {
			parts = append(parts, fmt.Sprintf("%7d", strings.Count(string(data), "\n")))
		}
		if countWords {
			parts = append(parts, fmt.Sprintf("%7d", len(strings.Fields(string(data)))))
		}
		if countBytes {
			parts = append(parts, fmt.Sprintf("%7d", len(data)))
		}
		parts = append(parts, virtualP)
		fmt.Fprintln(hc.Stdout, strings.Join(parts, " "))
	}
	if exitCode != 0 {
		return interp.NewExitStatus(exitCode)
	}
	return nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// parseNFlag parses -n <N> or -N from args, returning n and remaining path args.
func parseNFlag(args []string, defaultN int) (int, []string) {
	n := defaultN
	var paths []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-n" && i+1 < len(args) {
			fmt.Sscanf(args[i+1], "%d", &n)
			i++
		} else if strings.HasPrefix(a, "-") {
			// could be -5 shorthand
			var v int
			if _, err := fmt.Sscanf(a, "-%d", &v); err == nil {
				n = v
			}
		} else {
			paths = append(paths, a)
		}
	}
	return n, paths
}

func printNLines(w io.Writer, r io.Reader, n int) {
	sc := bufio.NewScanner(r)
	for i := 0; i < n && sc.Scan(); i++ {
		fmt.Fprintln(w, sc.Text())
	}
}

func printLastNLines(w io.Writer, r io.Reader, n int) {
	var lines []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	start := len(lines) - n
	if start < 0 {
		start = 0
	}
	for _, l := range lines[start:] {
		fmt.Fprintln(w, l)
	}
}
```
