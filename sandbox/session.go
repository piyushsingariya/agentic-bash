package sandbox

import (
	"bufio"
	"os"
	"strings"
)

// hostEnvMap reads the current process's environment into a key→value map.
// Used to seed the sandbox env when Options.Env is nil.
func hostEnvMap() map[string]string {
	pairs := os.Environ()
	m := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		if i := strings.IndexByte(pair, '='); i > 0 {
			m[pair[:i]] = pair[i+1:]
		}
	}
	return m
}

// ShellState holds the persistent state that survives across Run() calls
// within the same Sandbox session: environment variables, current working
// directory, command history, and (in later phases) shell function definitions
// and the installed package manifest.
type ShellState struct {
	Env     map[string]string
	Cwd     string
	History []string

	// Functions holds shell function definitions.
	// Populated in Phase 2 once the in-process mvdan.cc/sh interpreter is wired in.
	Functions map[string]string

	// Installed tracks package names installed via shims (Phase 6).
	Installed []string
}

// newShellState builds the initial ShellState from the provided Options.
//
// Environment initialisation rules:
//   - opts.Env == nil  → inherit the host process environment. This ensures
//     PATH and other system variables are available so external commands work
//     without any extra configuration.
//   - opts.Env != nil  → use exactly the supplied map (empty map = empty env).
func newShellState(opts Options) *ShellState {
	var env map[string]string
	if opts.Env == nil {
		env = hostEnvMap()
	} else {
		env = make(map[string]string, len(opts.Env))
		for k, v := range opts.Env {
			env[k] = v
		}
	}
	return &ShellState{
		Env:       env,
		Cwd:       opts.WorkDir,
		Functions: make(map[string]string),
	}
}

// EnvSlice converts the env map into the []string{"KEY=VALUE",...} format
// expected by os/exec.
func (s *ShellState) EnvSlice() []string {
	out := make([]string, 0, len(s.Env))
	for k, v := range s.Env {
		out = append(out, k+"="+v)
	}
	return out
}

// parseEnvFile reads the output of the shell's `env` command from path and
// returns a key→value map.
//
// Format assumption: each line is "KEY=VALUE"; the value may contain '=' but
// not newlines (known Phase 1 limitation — resolved in Phase 2 by reading the
// in-process interpreter's variable table directly).
//
// Lines that look like shell-internal variables (keys containing spaces, tabs,
// parentheses, or braces) are silently skipped.
func parseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	result := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		idx := strings.IndexByte(line, '=')
		if idx < 1 {
			continue
		}
		key := line[:idx]
		// Skip keys that are clearly shell internals or contain invalid characters.
		if strings.ContainsAny(key, " \t(){}") {
			continue
		}
		result[key] = line[idx+1:]
	}
	return result, scanner.Err()
}
