package session

import "testing"

// Spec docs/design/2026-10-23-ask-groups.md "Daemon autostart":
// "`[notifications] autostart_daemon` defaults to false."
func TestAutostartDaemon_DefaultsOffWhenKeyAbsent(t *testing.T) {
	writeDesktopNotifyConfig(t, "desktop = true")
	if GetNotificationsSettings().GetAutostartDaemonEnabled() {
		t.Error("autostart enabled with no autostart_daemon key; spec default is false")
	}
}

// Spec "Daemon autostart": the zero-value config (nil pointer) is the default.
func TestAutostartDaemon_ZeroValueConfigIsOff(t *testing.T) {
	if (NotificationsConfig{}).GetAutostartDaemonEnabled() {
		t.Error("zero-value NotificationsConfig enables autostart; spec default is false")
	}
}

// Spec "Daemon autostart": "Users who want the TUI to start the daemon set it
// to true."
func TestAutostartDaemon_ExplicitTrueEnables(t *testing.T) {
	writeDesktopNotifyConfig(t, "autostart_daemon = true")
	if !GetNotificationsSettings().GetAutostartDaemonEnabled() {
		t.Error("autostart_daemon = true did not enable auto-start")
	}
}
