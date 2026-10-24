package session

import "testing"

// Spec docs/design/2026-10-23-ask-groups.md "Daemon autostart" (revised):
// "`[notifications] autostart_daemon` defaults to **true** again (nil = true)."
func TestAutostartDaemon_DefaultsOnWhenKeyAbsent(t *testing.T) {
	writeDesktopNotifyConfig(t, "desktop = true")
	if !GetNotificationsSettings().GetAutostartDaemonEnabled() {
		t.Error("autostart disabled with no autostart_daemon key; spec default is true")
	}
}

// Spec "Daemon autostart": nil = true, so the zero-value config enables it.
func TestAutostartDaemon_ZeroValueConfigIsOn(t *testing.T) {
	if !(NotificationsConfig{}).GetAutostartDaemonEnabled() {
		t.Error("zero-value NotificationsConfig disables autostart; spec default is true (nil = true)")
	}
}

// An explicit true still enables auto-start.
func TestAutostartDaemon_ExplicitTrueEnables(t *testing.T) {
	writeDesktopNotifyConfig(t, "autostart_daemon = true")
	if !GetNotificationsSettings().GetAutostartDaemonEnabled() {
		t.Error("autostart_daemon = true did not enable auto-start")
	}
}
