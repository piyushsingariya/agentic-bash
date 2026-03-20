package executor

// StateExtractor is an optional interface implemented by stateful executors
// (e.g. ShellExecutor) that manage shell state internally between Run() calls.
//
// When a Sandbox's executor implements StateExtractor, the Sandbox uses these
// methods to synchronise ShellState instead of the temp-file capture approach
// used by NativeExecutor.
type StateExtractor interface {
	// ExtractState returns the current exported environment and working
	// directory as observed by the interpreter after the most recent Run().
	ExtractState() (env map[string]string, cwd string)

	// ResetState discards all accumulated session state and reinitialises
	// the executor with the supplied environment and working directory.
	// Called by Sandbox.Reset().
	ResetState(env map[string]string, cwd string) error
}
