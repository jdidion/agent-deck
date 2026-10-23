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

func TestAutostartDaemon_DefaultsOff(t *testing.T) {
	// A TUI under a throwaway HOME must not leak a detached daemon, so the
	// default when the key is absent is not to auto-start one.
	writeDesktopNotifyConfig(t, "desktop = true")
	if GetNotificationsSettings().GetAutostartDaemonEnabled() {
		t.Error("autostart defaulted on with no autostart_daemon key; want off")
	}
}

func TestAutostartDaemon_ExplicitOff(t *testing.T) {
	writeDesktopNotifyConfig(t, "autostart_daemon = false")
	if GetNotificationsSettings().GetAutostartDaemonEnabled() {
		t.Error("autostart_daemon = false did not disable auto-start")
	}
}
