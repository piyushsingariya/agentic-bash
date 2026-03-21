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

// resolveArg converts a command argument into a real on-disk path.
// Relative paths are resolved against hc.Dir (the runner's real CWD).
// Absolute paths are treated as virtual and translated via pathmap.
func resolveArg(hc interp.HandlerContext, sandboxRoot, p string) string {
	if !filepath.IsAbs(p) {
		return filepath.Join(hc.Dir, p)
	}
	return pathmap.VirtualToReal(sandboxRoot, p)
}

// NewFilesystemInterceptors returns all filesystem command interceptors.
func NewFilesystemInterceptors(cfg Config) []Interceptor {
	return []Interceptor{
		&lsInterceptor{cfg: cfg},
		&statCmdInterceptor{cfg: cfg},
		&catInterceptor{cfg: cfg},
		&headInterceptor{cfg: cfg},
		&tailInterceptor{cfg: cfg},
		&wcInterceptor{cfg: cfg},
	}
}

// ─── ls ───────────────────────────────────────────────────────────────────────

type lsInterceptor struct{ cfg Config }

func (l *lsInterceptor) Name() string { return "ls" }
func (l *lsInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	opts, paths := parseLsArgs(args[1:])

	if len(paths) == 0 {
		paths = []string{hc.Dir}
	}

	for i, p := range paths {
		realP := resolveArg(hc, l.cfg.SandboxRoot, p)
		virtualP := pathmap.RealToVirtual(l.cfg.SandboxRoot, realP)

		if opts.dirOnly {
			info, err := os.Lstat(realP)
			if err != nil {
				fmt.Fprintf(hc.Stderr, "ls: cannot access '%s': No such file or directory\n", virtualP)
				continue
			}
			printLsEntry(hc.Stdout, info, opts)
			continue
		}

		if len(paths) > 1 {
			if i > 0 {
				fmt.Fprintln(hc.Stdout)
			}
			fmt.Fprintf(hc.Stdout, "%s:\n", virtualP)
		}

		if err := lsDir(hc.Stdout, hc.Stderr, realP, virtualP, opts); err != nil {
			fmt.Fprintf(hc.Stderr, "ls: cannot access '%s': No such file or directory\n", virtualP)
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
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			for _, c := range a[1:] {
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

func lsDir(stdout, stderr io.Writer, realDir, virtualDir string, opts lsOpts) error {
	entries, err := os.ReadDir(realDir)
	if err != nil {
		return err
	}

	// Filter hidden files.
	filtered := make([]os.DirEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !opts.all && !opts.almostAll && strings.HasPrefix(name, ".") {
			continue
		}
		filtered = append(filtered, e)
	}

	// Sort.
	switch {
	case opts.sortTime:
		sort.Slice(filtered, func(i, j int) bool {
			iInfo, _ := filtered[i].Info()
			jInfo, _ := filtered[j].Info()
			if iInfo == nil || jInfo == nil {
				return false
			}
			return iInfo.ModTime().After(jInfo.ModTime())
		})
	case opts.sortSize:
		sort.Slice(filtered, func(i, j int) bool {
			iInfo, _ := filtered[i].Info()
			jInfo, _ := filtered[j].Info()
			if iInfo == nil || jInfo == nil {
				return false
			}
			return iInfo.Size() > jInfo.Size()
		})
	default:
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
		printLsEntry(stdout, info, opts)
	}

	if opts.recursive {
		for _, e := range filtered {
			if e.IsDir() {
				subReal := filepath.Join(realDir, e.Name())
				subVirtual := filepath.Join(virtualDir, e.Name())
				fmt.Fprintf(stdout, "\n%s:\n", subVirtual)
				_ = lsDir(stdout, stderr, subReal, subVirtual, opts)
			}
		}
	}

	return nil
}

func printLsEntry(w io.Writer, info fs.FileInfo, opts lsOpts) {
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
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGTPE"[exp])
}

// ─── stat (command) ───────────────────────────────────────────────────────────

type statCmdInterceptor struct{ cfg Config }

func (s *statCmdInterceptor) Name() string { return "stat" }
func (s *statCmdInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	if len(args) < 2 {
		fmt.Fprintln(hc.Stderr, "stat: missing operand")
		return interp.NewExitStatus(1)
	}

	exitCode := uint8(0)
	for _, p := range args[1:] {
		if strings.HasPrefix(p, "-") {
			continue // skip format flags for now
		}
		realP := resolveArg(hc, s.cfg.SandboxRoot, p)
		virtualP := pathmap.RealToVirtual(s.cfg.SandboxRoot, realP)

		info, err := os.Lstat(realP)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "stat: cannot statx '%s': No such file or directory\n", virtualP)
			exitCode = 1
			continue
		}

		fileType := "regular file"
		if info.IsDir() {
			fileType = "directory"
		} else if info.Mode()&fs.ModeSymlink != 0 {
			fileType = "symbolic link"
		}

		fmt.Fprintf(hc.Stdout, "  File: %s\n", virtualP)
		fmt.Fprintf(hc.Stdout, "  Size: %-15d Blocks: %-10d IO Block: 4096   %s\n",
			info.Size(), (info.Size()+511)/512, fileType)
		fmt.Fprintf(hc.Stdout, "Access: (%04o/%s)\n", info.Mode().Perm(), info.Mode())
		fmt.Fprintf(hc.Stdout, "Modify: %s\n", info.ModTime().Format(time.RFC3339))
	}

	if exitCode != 0 {
		return interp.NewExitStatus(exitCode)
	}
	return nil
}

// ─── cat ──────────────────────────────────────────────────────────────────────

type catInterceptor struct{ cfg Config }

func (c *catInterceptor) Name() string { return "cat" }
func (c *catInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)

	if len(args) < 2 {
		_, _ = io.Copy(hc.Stdout, hc.Stdin)
		return nil
	}

	showLineNums := false
	exitCode := uint8(0)
	for _, a := range args[1:] {
		if a == "-n" {
			showLineNums = true
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}

		realP := resolveArg(hc, c.cfg.SandboxRoot, a)
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

// ─── head ─────────────────────────────────────────────────────────────────────

type headInterceptor struct{ cfg Config }

func (h *headInterceptor) Name() string { return "head" }
func (h *headInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	n, paths := parseNFlag(args[1:], 10)

	if len(paths) == 0 {
		printNLines(hc.Stdout, hc.Stdin, n)
		return nil
	}

	exitCode := uint8(0)
	for _, p := range paths {
		realP := resolveArg(hc, h.cfg.SandboxRoot, p)
		virtualP := pathmap.RealToVirtual(h.cfg.SandboxRoot, realP)
		f, err := os.Open(realP)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "head: cannot open '%s': No such file or directory\n", virtualP)
			exitCode = 1
			continue
		}
		if len(paths) > 1 {
			fmt.Fprintf(hc.Stdout, "==> %s <==\n", virtualP)
		}
		printNLines(hc.Stdout, f, n)
		f.Close()
	}
	if exitCode != 0 {
		return interp.NewExitStatus(exitCode)
	}
	return nil
}

// ─── tail ─────────────────────────────────────────────────────────────────────

type tailInterceptor struct{ cfg Config }

func (t *tailInterceptor) Name() string { return "tail" }
func (t *tailInterceptor) Handle(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	n, paths := parseNFlag(args[1:], 10)

	if len(paths) == 0 {
		printLastNLines(hc.Stdout, hc.Stdin, n)
		return nil
	}

	exitCode := uint8(0)
	for _, p := range paths {
		realP := resolveArg(hc, t.cfg.SandboxRoot, p)
		virtualP := pathmap.RealToVirtual(t.cfg.SandboxRoot, realP)
		f, err := os.Open(realP)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "tail: cannot open '%s': No such file or directory\n", virtualP)
			exitCode = 1
			continue
		}
		if len(paths) > 1 {
			fmt.Fprintf(hc.Stdout, "==> %s <==\n", virtualP)
		}
		printLastNLines(hc.Stdout, f, n)
		f.Close()
	}
	if exitCode != 0 {
		return interp.NewExitStatus(exitCode)
	}
	return nil
}

// ─── wc ───────────────────────────────────────────────────────────────────────

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
	if !countLines && !countWords && !countBytes {
		countLines, countWords, countBytes = true, true, true
	}

	if len(paths) == 0 {
		data, _ := io.ReadAll(hc.Stdin)
		printWcLine(hc.Stdout, data, "", countLines, countWords, countBytes)
		return nil
	}

	exitCode := uint8(0)
	for _, p := range paths {
		realP := resolveArg(hc, w.cfg.SandboxRoot, p)
		virtualP := pathmap.RealToVirtual(w.cfg.SandboxRoot, realP)
		data, err := os.ReadFile(realP)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "wc: %s: No such file or directory\n", virtualP)
			exitCode = 1
			continue
		}
		printWcLine(hc.Stdout, data, virtualP, countLines, countWords, countBytes)
	}
	if exitCode != 0 {
		return interp.NewExitStatus(exitCode)
	}
	return nil
}

func printWcLine(w io.Writer, data []byte, name string, lines, words, bytes bool) {
	var parts []string
	s := string(data)
	if lines {
		parts = append(parts, fmt.Sprintf("%7d", strings.Count(s, "\n")))
	}
	if words {
		parts = append(parts, fmt.Sprintf("%7d", len(strings.Fields(s))))
	}
	if bytes {
		parts = append(parts, fmt.Sprintf("%7d", len(data)))
	}
	if name != "" {
		parts = append(parts, name)
	}
	fmt.Fprintln(w, strings.Join(parts, " "))
}

// ─── shared helpers ───────────────────────────────────────────────────────────

func parseNFlag(args []string, defaultN int) (int, []string) {
	n := defaultN
	var paths []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-n" && i+1 < len(args) {
			fmt.Sscanf(args[i+1], "%d", &n)
			i++
		} else if strings.HasPrefix(a, "-") {
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
