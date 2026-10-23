package session

import "testing"

// The lock is what makes TUI auto-start safe: a second daemon must fail to
// acquire and exit rather than run alongside the first and double-fire every
// notification.
func TestAcquireNotifyDaemonLock_SingleInstance(t *testing.T) {
	release1, ok1, err := AcquireNotifyDaemonLock()
	if err != nil {
		t.Fatalf("first acquire errored: %v", err)
	}
	if !ok1 {
		t.Fatal("first acquire did not get the lock")
	}

	// A second acquire while the first is held must be refused, without error.
	release2, ok2, err := AcquireNotifyDaemonLock()
	if err != nil {
		t.Fatalf("second acquire errored: %v", err)
	}
	if ok2 {
		if release2 != nil {
			release2()
		}
		t.Fatal("second acquire took the lock while the first was held; two daemons would run")
	}
	if release2 != nil {
		t.Error("refused acquire returned a non-nil release")
	}

	// After the first releases, the lock is available again.
	release1()

	release3, ok3, err := AcquireNotifyDaemonLock()
	if err != nil {
		t.Fatalf("third acquire errored: %v", err)
	}
	if !ok3 {
		t.Fatal("lock was not reacquirable after release")
	}
	release3()
}

func TestAutostartDaemon_DefaultsOn(t *testing.T) {
	// The whole notification stack is inert without a running daemon, so the
	// safe default when the key is absent is to auto-start one.
	writeDesktopNotifyConfig(t, "desktop = true")
	if !GetNotificationsSettings().GetAutostartDaemonEnabled() {
		t.Error("autostart defaulted off with no autostart_daemon key; want on")
	}
}

func TestAutostartDaemon_ExplicitOff(t *testing.T) {
	writeDesktopNotifyConfig(t, "autostart_daemon = false")
	if GetNotificationsSettings().GetAutostartDaemonEnabled() {
		t.Error("autostart_daemon = false did not disable auto-start")
	}
}
