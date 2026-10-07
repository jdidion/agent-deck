package main

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// ensureNotifyDaemon starts the always-on notify daemon in the background when
// the TUI launches, unless the operator opted out.
//
// The daemon is a separate process that does ALL notification dispatch (parent
// inbox, desktop/cmux banners, the ask queue); the TUI itself only polls status
// for the list. Without a running daemon none of those fire, and it used to
// require a launchd/systemd service the standalone-TUI user never installed —
// so notifications silently did nothing. Auto-starting it here closes that gap.
//
// Safe to call on every launch: the spawned daemon takes a machine-wide,
// non-blocking flock (AcquireNotifyDaemonLock), so a second daemon — from a
// repeated launch or a second TUI — loses the lock and exits within
// milliseconds. Fully best-effort: any failure is logged and swallowed, because
// a notifier that could block or crash the TUI on startup would be worse than
// no notifier.
func ensureNotifyDaemon() {
	if !session.GetNotificationsSettings().GetAutostartDaemonEnabled() {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		logging.ForComponent(logging.CompNotif).Debug("notify_autostart_no_executable",
			"error", err.Error())
		return
	}

	// The daemon must NOT inherit the TUI's terminal: its pre-logging startup
	// writes and any stray stderr would corrupt the alt-screen. Point stdio at
	// /dev/null; the daemon logs to the XDG cache debug.log via its own setup.
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		logging.ForComponent(logging.CompNotif).Debug("notify_autostart_no_devnull",
			"error", err.Error())
		return
	}
	defer devnull.Close()

	cmd := exec.Command(exe, "notify-daemon")
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	// Own process group: the daemon outlives this TUI and must not receive the
	// TUI's terminal signals (Ctrl+C, SIGHUP on close). It keeps notifying for
	// background sessions after the TUI exits, and the next launch's spawn is a
	// no-op against the still-held lock.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		logging.ForComponent(logging.CompNotif).Debug("notify_autostart_spawn_failed",
			"error", err.Error())
		return
	}
	// Detach: never wait on it, and drop our handle so it cannot become a
	// zombie when it exits (e.g. as the lock loser).
	_ = cmd.Process.Release()
	logging.ForComponent(logging.CompNotif).Debug("notify_autostart_spawned")
}
