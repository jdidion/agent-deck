// Package childenv builds the environment for a child process that
// agent-deck spawns (claude workers, pooled MCP servers), guaranteed not to
// inherit the conductor's telegram pollution.
//
// It lives in its own leaf package — not in internal/session — because
// internal/session imports internal/mcppool, so mcppool cannot import session.
// Both packages (and cmd/agent-deck) import this leaf so a single filter is the
// only way to construct a child env. Direct os.Environ() in the spawn-path
// packages is forbidden by golangci forbidigo (see .golangci.yml); this is the
// one allowlisted home for it.
//
// Issue #1163: a child must NEVER inherit the parent's CLAUDE_CONFIG_DIR. The
// conductor's config dir points at a worker-scratch profile whose settings.json
// enables the telegram plugin; inheriting it makes the child load telegram and
// spawn a duplicate poller. #1152 stripped TELEGRAM_* but not CLAUDE_CONFIG_DIR
// — this closes that gap structurally.
package childenv

import (
	"os"
	"strings"
)

const claudeConfigDirPrefix = "CLAUDE_CONFIG_DIR="

// mallocStackLoggingPrefixes are macOS libmalloc stack-logging env vars. When any
// is set (e.g. left behind by Instruments/leaks, or inherited from whatever
// launched agent-deck), every short-lived child prints "MallocStackLogging:
// can't turn off malloc stack logging because it was not enabled." to stderr on
// teardown. A busy agent fires hooks constantly, so its worker's hook-handler
// children flood the session pane with that line. These are developer debugging
// vars with no place in a spawned worker, so strip the whole family — the worker
// inherits a clean env, and so do the hook-handlers it spawns.
var mallocStackLoggingPrefixes = []string{
	"MallocStackLogging",   // and ...NoCompact / ...Directory
	"MALLOC_STACK_LOGGING", // underscore variant
}

// FilterEnv returns env with every TELEGRAM_* var, any inherited
// CLAUDE_CONFIG_DIR, and the macOS MallocStackLogging* debugging vars removed. If
// childConfigDir is non-empty, a single CLAUDE_CONFIG_DIR=<childConfigDir> is
// appended so the child is pinned to its own config dir. The input slice is not
// mutated.
func FilterEnv(env []string, childConfigDir string) []string {
	out := make([]string, 0, len(env)+1)
nextVar:
	for _, kv := range env {
		if strings.HasPrefix(kv, "TELEGRAM_") { // #1152 logic
			continue
		}
		if strings.HasPrefix(kv, claudeConfigDirPrefix) { // #1163: never inherit parent CCD
			continue
		}
		for _, p := range mallocStackLoggingPrefixes {
			if strings.HasPrefix(kv, p) {
				continue nextVar
			}
		}
		out = append(out, kv)
	}
	if childConfigDir != "" {
		out = append(out, claudeConfigDirPrefix+childConfigDir)
	}
	return out
}

// ForLaunch is FilterEnv applied to the current process environment. This is
// the only place os.Environ() is called from a spawn path.
func ForLaunch(childConfigDir string) []string {
	return FilterEnv(os.Environ(), childConfigDir)
}
