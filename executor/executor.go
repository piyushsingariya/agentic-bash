package executor

import "context"

// Result holds the output of a single command execution.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Error    error
}

// Executor runs shell commands and returns their output.
type Executor interface {
	Run(ctx context.Context, cmd string, env []string, dir string) Result
}
