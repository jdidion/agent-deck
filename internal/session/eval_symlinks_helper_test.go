package session

import (
	"path/filepath"
	"testing"
)

// evalSymlinksT returns the symlink-resolved form of an existing path. Code
// that canonicalizes paths (EvalSymlinks, or the kernel reporting open files
// via lsof / /proc/<pid>/fd) yields this form, and on macOS t.TempDir() sits
// under the /var -> /private/var alias, so expectations must use it too.
func evalSymlinksT(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
