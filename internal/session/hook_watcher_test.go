package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatusFileWatcher_ProcessFile(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	_ = os.MkdirAll(hooksDir, 0755)

	// Create a watcher (don't start the goroutine, just test processFile)
	w := &StatusFileWatcher{
		hooksDir: hooksDir,
		statuses: make(map[string]*HookStatus),
	}

	// Write a status file
	status := struct {
		Status                   string `json:"status"`
		SessionID                string `json:"session_id"`
		Event                    string `json:"event"`
		Timestamp                int64  `json:"ts"`
		CodexStartedGeneration   string `json:"codex_started_generation"`
		CodexCompletedGeneration string `json:"codex_completed_generation"`
		CodexStartedSessionID    string `json:"codex_started_session_id"`
		CodexCompletedSessionID  string `json:"codex_completed_session_id"`
	}{
		Status:                   "running",
		SessionID:                "abc-123",
		Event:                    "UserPromptSubmit",
		Timestamp:                time.Now().Unix(),
		CodexStartedGeneration:   "thread:turn",
		CodexCompletedGeneration: "thread:turn",
		CodexStartedSessionID:    "thread",
		CodexCompletedSessionID:  "thread",
	}
	data, _ := json.Marshal(status)
	filePath := filepath.Join(hooksDir, "instance-001.json")
	_ = os.WriteFile(filePath, data, 0644)

	// Process the file
	w.processFile(filePath)

	// Verify the status was recorded
	hs := w.GetHookStatus("instance-001")
	if hs == nil {
		t.Fatal("Expected hook status to be set")
	}
	if hs.Status != "running" {
		t.Errorf("Status = %q, want running", hs.Status)
	}
	if hs.SessionID != "abc-123" {
		t.Errorf("SessionID = %q, want abc-123", hs.SessionID)
	}
	if hs.Event != "UserPromptSubmit" {
		t.Errorf("Event = %q, want UserPromptSubmit", hs.Event)
	}
	if hs.CodexStartedGeneration != "thread:turn" || hs.CodexCompletedGeneration != "thread:turn" ||
		hs.CodexStartedSessionID != "thread" || hs.CodexCompletedSessionID != "thread" {
		t.Fatalf("Codex evidence not propagated: %#v", hs)
	}
}

func TestStatusFileWatcher_LoadExisting(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	_ = os.MkdirAll(hooksDir, 0755)

	// Pre-create some status files
	for _, inst := range []struct {
		id     string
		status string
	}{
		{"inst-1", "running"},
		{"inst-2", "idle"},
		{"inst-3", "waiting"},
	} {
		data, _ := json.Marshal(map[string]any{
			"status":     inst.status,
			"session_id": "sess-" + inst.id,
			"event":      "Test",
			"ts":         time.Now().Unix(),
		})
		_ = os.WriteFile(filepath.Join(hooksDir, inst.id+".json"), data, 0644)
	}

	w := &StatusFileWatcher{
		hooksDir: hooksDir,
		statuses: make(map[string]*HookStatus),
	}

	w.loadExisting()

	// All three should be loaded
	for _, id := range []string{"inst-1", "inst-2", "inst-3"} {
		if w.GetHookStatus(id) == nil {
			t.Errorf("Missing status for %s", id)
		}
	}

	// Verify correct statuses
	if w.GetHookStatus("inst-1").Status != "running" {
		t.Error("inst-1 should be running")
	}
	if w.GetHookStatus("inst-2").Status != "idle" {
		t.Error("inst-2 should be idle")
	}
	if w.GetHookStatus("inst-3").Status != "waiting" {
		t.Error("inst-3 should be waiting")
	}
}

// TestStatusFileWatcher_RetainsMatcherAndMessage verifies the human-ask-queue
// content is threaded through the file->HookStatus decode: a status file
// carrying matcher/message json keys yields a HookStatus with both populated
// (alongside Status=="waiting"), for both the permission and elicitation
// matchers.
func TestStatusFileWatcher_RetainsMatcherAndMessage(t *testing.T) {
	cases := []struct {
		id      string
		matcher string
		message string
	}{
		{"inst-perm", "permission_prompt", "Claude wants to run: rm -rf build"},
		{"inst-elic", "elicitation_dialog", "Which migration should I apply?"},
	}

	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	_ = os.MkdirAll(hooksDir, 0755)

	w := &StatusFileWatcher{
		hooksDir: hooksDir,
		statuses: make(map[string]*HookStatus),
	}

	for _, tc := range cases {
		data, _ := json.Marshal(map[string]any{
			"status":     "waiting",
			"session_id": "sess-" + tc.id,
			"event":      "Notification",
			"ts":         time.Now().Unix(),
			"matcher":    tc.matcher,
			"message":    tc.message,
		})
		filePath := filepath.Join(hooksDir, tc.id+".json")
		_ = os.WriteFile(filePath, data, 0644)
		w.processFile(filePath)

		hs := w.GetHookStatus(tc.id)
		if hs == nil {
			t.Fatalf("%s: expected hook status to be set", tc.id)
		}
		if hs.Status != "waiting" {
			t.Errorf("%s: Status = %q, want waiting", tc.id, hs.Status)
		}
		if hs.Matcher != tc.matcher {
			t.Errorf("%s: Matcher = %q, want %q", tc.id, hs.Matcher, tc.matcher)
		}
		if hs.Message != tc.message {
			t.Errorf("%s: Message = %q, want %q", tc.id, hs.Message, tc.message)
		}
	}
}

func TestStatusFileWatcher_NonExistentInstance(t *testing.T) {
	w := &StatusFileWatcher{
		statuses: make(map[string]*HookStatus),
	}

	hs := w.GetHookStatus("nonexistent")
	if hs != nil {
		t.Error("Expected nil for nonexistent instance")
	}
}

func TestStatusFileWatcher_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	_ = os.MkdirAll(hooksDir, 0755)

	w := &StatusFileWatcher{
		hooksDir: hooksDir,
		statuses: make(map[string]*HookStatus),
	}

	// Write invalid JSON
	filePath := filepath.Join(hooksDir, "bad-inst.json")
	_ = os.WriteFile(filePath, []byte("not json"), 0644)

	// Should not panic
	w.processFile(filePath)

	// Should not create an entry
	if w.GetHookStatus("bad-inst") != nil {
		t.Error("Should not create status from invalid JSON")
	}
}

func TestStatusFileWatcher_UpdatesExisting(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	_ = os.MkdirAll(hooksDir, 0755)

	w := &StatusFileWatcher{
		hooksDir: hooksDir,
		statuses: make(map[string]*HookStatus),
	}

	filePath := filepath.Join(hooksDir, "inst-x.json")

	// First write: running
	data1, _ := json.Marshal(map[string]any{
		"status": "running", "session_id": "s1", "event": "UserPromptSubmit", "ts": time.Now().Unix(),
	})
	_ = os.WriteFile(filePath, data1, 0644)
	w.processFile(filePath)

	if w.GetHookStatus("inst-x").Status != "running" {
		t.Error("Expected running after first write")
	}

	// Second write: idle
	data2, _ := json.Marshal(map[string]any{
		"status": "idle", "session_id": "s1", "event": "Stop", "ts": time.Now().Unix(),
	})
	_ = os.WriteFile(filePath, data2, 0644)
	w.processFile(filePath)

	if w.GetHookStatus("inst-x").Status != "idle" {
		t.Error("Expected idle after second write")
	}
}
