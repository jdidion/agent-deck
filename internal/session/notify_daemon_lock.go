package session

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// notifyDaemonLockName is the single lockfile that bounds the always-on
// transition notifier to one process, machine-wide. The daemon serves every
// profile in one pass (see profilesForTransitionDaemon), so the lock is global,
// not per-profile.
const notifyDaemonLockName = "notify-daemon.lock"

// AcquireNotifyDaemonLock takes a NON-BLOCKING exclusive advisory flock so a
// second notify-daemon exits immediately instead of double-firing every
// notification. This is what makes TUI auto-start safe: several TUIs (or
// repeated launches) can each spawn `agent-deck notify-daemon`, and all but the
// first lose the lock and return.
//
// Returns (release, true, nil) when the lock was taken; the caller MUST hold it
// for the daemon's lifetime and call release() on exit (closing the fd also
// drops the OS lock, so a crash releases it too). Returns (nil, false, nil)
// when another daemon already holds it — an ordinary, non-error outcome. A
// non-nil error means the lock could not even be attempted (e.g. the locks dir
// is unwritable); the caller decides whether to proceed unlocked.
func AcquireNotifyDaemonLock() (release func(), acquired bool, err error) {
	locks, err := resolveLocksDirForSpawnLock()
	if err != nil {
		return nil, false, fmt.Errorf("resolve locks dir for notify-daemon lock: %w", err)
	}
	if err := os.MkdirAll(locks, 0o700); err != nil {
		return nil, false, fmt.Errorf("create locks dir for notify-daemon lock: %w", err)
	}
	lockPath := filepath.Join(locks, notifyDaemonLockName)
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open notify-daemon lock %q: %w", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// EWOULDBLOCK/EAGAIN: another daemon holds it. Not an error — the caller
		// should just exit. Any other errno is also treated as "don't run",
		// since an unusable lock must not become a double-daemon.
		_ = f.Close()
		return nil, false, nil
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}
