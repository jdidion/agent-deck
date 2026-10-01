package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeFileInfo lets a test fabricate os.FileInfo with an arbitrary
// *syscall.Stat_t so rootPinnedSymlink's uid/dev checks can be exercised
// without actually chowning files on disk.
type fakeFileInfo struct {
	name string
	mode os.FileMode
	sys  *syscall.Stat_t
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() interface{}   { return f.sys }

// TestRootPinnedSymlink exercises each of rootPinnedSymlink's three
// independent gates (link uid 0, link device == root device, parent dir
// root-owned with no group/other write) so a regression dropping any one of
// them fails a case.
func TestRootPinnedSymlink(t *testing.T) {
	rootInfo, err := os.Stat("/")
	if err != nil {
		t.Fatalf("stat /: %v", err)
	}
	rootSt, ok := rootInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("os.Stat(\"/\").Sys() is not *syscall.Stat_t on this platform")
	}
	rootDev := rootSt.Dev

	t.Run("root uid, root device, root parent -> true", func(t *testing.T) {
		link := filepath.Join("/", "x")
		info := fakeFileInfo{name: "x", mode: os.ModeSymlink, sys: &syscall.Stat_t{Uid: 0, Dev: rootDev}}
		if got := rootPinnedSymlink(link, info); !got {
			t.Fatalf("rootPinnedSymlink(%q) = false, want true", link)
		}
	})

	t.Run("root uid, root device, user-owned temp parent -> false", func(t *testing.T) {
		base := t.TempDir()
		link := filepath.Join(base, "x")
		info := fakeFileInfo{name: "x", mode: os.ModeSymlink, sys: &syscall.Stat_t{Uid: 0, Dev: rootDev}}
		if got := rootPinnedSymlink(link, info); got {
			t.Fatalf("rootPinnedSymlink(%q) = true, want false (parent is user-writable)", link)
		}
	})

	t.Run("non-root uid, root device, root parent -> false", func(t *testing.T) {
		uid := os.Getuid()
		if uid == 0 {
			t.Skip("test runs as root; cannot exercise non-root uid case")
		}
		link := filepath.Join("/", "x")
		info := fakeFileInfo{name: "x", mode: os.ModeSymlink, sys: &syscall.Stat_t{Uid: uint32(uid), Dev: rootDev}}
		if got := rootPinnedSymlink(link, info); got {
			t.Fatalf("rootPinnedSymlink(%q) = true, want false (link not root-owned)", link)
		}
	})

	t.Run("root uid, off-volume device, root parent -> false", func(t *testing.T) {
		link := filepath.Join("/", "x")
		info := fakeFileInfo{name: "x", mode: os.ModeSymlink, sys: &syscall.Stat_t{Uid: 0, Dev: rootDev + 1}}
		if got := rootPinnedSymlink(link, info); got {
			t.Fatalf("rootPinnedSymlink(%q) = true, want false (link on different device than root)", link)
		}
	})

	if runtime.GOOS == "darwin" {
		t.Run("darwin: root uid, root device, /tmp parent (mode 1777) -> false", func(t *testing.T) {
			link := "/tmp/x"
			info := fakeFileInfo{name: "x", mode: os.ModeSymlink, sys: &syscall.Stat_t{Uid: 0, Dev: rootDev}}
			if got := rootPinnedSymlink(link, info); got {
				t.Fatalf("rootPinnedSymlink(%q) = true, want false (/private/tmp is world-writable)", link)
			}
		})
	}
}

// t.TempDir() sits under a root-owned system alias on macOS (/var ->
// /private/var). That alias must not trip the guard, while a symlink planted
// in a user-writable directory below it must still be refused by name.
func TestEnsureNoSymlinkPath_SystemAliasAllowedPlantedLinkRefused(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ensureNoSymlinkPath(filepath.Join(real, "missing", "leaf.jsonl")); err != nil {
		t.Fatalf("plain temp path refused: %v", err)
	}

	planted := filepath.Join(base, "planted")
	if err := os.Symlink(real, planted); err != nil {
		t.Fatal(err)
	}
	err := ensureNoSymlinkPath(filepath.Join(planted, "leaf.jsonl"))
	if err == nil {
		t.Fatal("symlink planted below the temp root was accepted")
	}
	if !strings.Contains(err.Error(), "path component is a symlink: "+planted) {
		t.Fatalf("refusal must name the planted link %q, got %v", planted, err)
	}
}
