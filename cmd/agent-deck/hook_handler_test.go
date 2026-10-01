package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestMapEventToStatus(t *testing.T) {
	tests := []struct {
		event  string
		expect string
	}{
		{"SessionStart", "waiting"},
		{"BeforeAgent", "running"},
		{"AfterAgent", "waiting"},
		{"UserPromptSubmit", "running"},
		{"Stop", "waiting"},
		{"PermissionRequest", "waiting"},
		{"Notification", ""},
		{"SessionEnd", "dead"},
		{"PreCompact", ""},
		{"UnknownEvent", ""},
		// Hermes shell hook events
		{"pre_llm_call", "running"},
		{"post_llm_call", "waiting"},
		{"pre_tool_call", "running"},
		// Hermes-only key: mid-turn, a finished tool call means the LLM keeps
		// working; the turn-end waiting edge belongs to post_llm_call.
		{"post_tool_call", "running"},
		// Claude/Cursor post-tool events keep mapping to waiting.
		{"PostToolUse", "waiting"},
		{"on_session_start", "waiting"},
		// Hermes fires on_session_end after EVERY run_conversation call (once
		// per user message), not at process exit — it is the turn-end edge,
		// and the only one an interrupted turn gets (post_llm_call is skipped
		// on interrupt). The real process-exit event is on_session_finalize.
		{"on_session_end", "waiting"},
		{"on_session_finalize", "dead"},
		// Per-API-call heartbeat: keeps "running" fresh through long
		// multi-step turns that would outlive the hook freshness window.
		{"pre_api_request", "running"},
		{"post_api_request", "running"},
		{"subagent_stop", ""},
		// Cursor Agent CLI hook events (camelCase)
		{"sessionStart", "waiting"},
		{"beforeSubmitPrompt", "running"},
		{"preToolUse", "running"},
		{"postToolUse", "waiting"},
		{"postToolUseFailure", "waiting"},
		{"stop", "waiting"},
		{"sessionEnd", "dead"},
	}

	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			got := mapEventToStatus(tt.event)
			if got != tt.expect {
				t.Errorf("mapEventToStatus(%q) = %q, want %q", tt.event, got, tt.expect)
			}
		})
	}
}

func TestHookStatusFile_JSON(t *testing.T) {
	sf := hookStatusFile{
		Status:    "running",
		SessionID: "abc-123",
		Event:     "UserPromptSubmit",
		Timestamp: 1707900000,
	}

	data, err := json.Marshal(sf)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var read hookStatusFile
	if err := json.Unmarshal(data, &read); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if read.Status != "running" {
		t.Errorf("Status = %q, want running", read.Status)
	}
	if read.SessionID != "abc-123" {
		t.Errorf("SessionID = %q, want abc-123", read.SessionID)
	}
	if read.Event != "UserPromptSubmit" {
		t.Errorf("Event = %q, want UserPromptSubmit", read.Event)
	}
	if read.Timestamp != 1707900000 {
		t.Errorf("Timestamp = %d, want 1707900000", read.Timestamp)
	}
}

func TestHookHandler_WritesStatusFile(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatalf("Failed to create hooks dir: %v", err)
	}

	instanceID := "test-instance-123"
	sf := hookStatusFile{
		Status:    "waiting",
		SessionID: "sess-456",
		Event:     "PermissionRequest",
		Timestamp: time.Now().Unix(),
	}

	data, err := json.Marshal(sf)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	// Simulate atomic write
	filePath := filepath.Join(hooksDir, instanceID+".json")
	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		t.Fatalf("Failed to write tmp: %v", err)
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		t.Fatalf("Failed to rename: %v", err)
	}

	// Verify file exists and has correct content
	readData, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read back: %v", err)
	}

	var read hookStatusFile
	if err := json.Unmarshal(readData, &read); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if read.Status != "waiting" {
		t.Errorf("Status = %q, want waiting", read.Status)
	}
	if read.Event != "PermissionRequest" {
		t.Errorf("Event = %q, want PermissionRequest", read.Event)
	}
}

func TestHookHandler_MissingInstanceID(t *testing.T) {
	// When AGENTDECK_INSTANCE_ID is not set, handleHookHandler should return silently
	os.Unsetenv("AGENTDECK_INSTANCE_ID")

	// This should not panic or produce any output
	handleHookHandler()
}

func TestHookPayload_Unmarshal(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		event   string
		session string
	}{
		{
			name:    "SessionStart",
			input:   `{"hook_event_name":"SessionStart","session_id":"abc-123","source":"claude"}`,
			event:   "SessionStart",
			session: "abc-123",
		},
		{
			name:    "Stop",
			input:   `{"hook_event_name":"Stop","session_id":"def-456"}`,
			event:   "Stop",
			session: "def-456",
		},
		{
			name:    "unknown fields ignored",
			input:   `{"hook_event_name":"UserPromptSubmit","session_id":"ghi-789","extra":"ignored"}`,
			event:   "UserPromptSubmit",
			session: "ghi-789",
		},
		{
			name:    "Cursor stop with conversation_id",
			input:   `{"hook_event_name":"stop","conversation_id":"conv-123"}`,
			event:   "stop",
			session: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p hookPayload
			if err := json.Unmarshal([]byte(tt.input), &p); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			if p.HookEventName != tt.event {
				t.Errorf("HookEventName = %q, want %q", p.HookEventName, tt.event)
			}
			if p.SessionID != tt.session {
				t.Errorf("SessionID = %q, want %q", p.SessionID, tt.session)
			}
		})
	}
}

func TestWriteHookStatus_EmptyEventDoesNotBackfillJSON(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	instanceID := "inst-sticky"
	writeHookStatus(instanceID, "waiting", "sess-1", "SessionStart", "")
	writeHookStatus(instanceID, "running", "", "UserPromptSubmit", "")

	data, err := os.ReadFile(filepath.Join(getHooksDir(), instanceID+".json"))
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	var hook hookStatusFile
	if err := json.Unmarshal(data, &hook); err != nil {
		t.Fatalf("unmarshal hook: %v", err)
	}
	if hook.SessionID != "" {
		t.Fatalf("hook session_id = %q, want empty for compatibility", hook.SessionID)
	}
	if got := session.ReadHookSessionAnchor(instanceID); got != "sess-1" {
		t.Fatalf("session anchor = %q, want sess-1", got)
	}
}

func TestWriteHookStatus_ClearsStickySessionOnSessionEnd(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	instanceID := "inst-end"
	writeHookStatus(instanceID, "waiting", "sess-2", "SessionStart", "")
	writeHookStatus(instanceID, "dead", "", "SessionEnd", "")
	writeHookStatus(instanceID, "waiting", "", "Stop", "")

	data, err := os.ReadFile(filepath.Join(getHooksDir(), instanceID+".json"))
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	var hook hookStatusFile
	if err := json.Unmarshal(data, &hook); err != nil {
		t.Fatalf("unmarshal hook: %v", err)
	}
	if hook.SessionID != "" {
		t.Fatalf("hook session_id = %q, want empty after SessionEnd cleanup", hook.SessionID)
	}
	if got := session.ReadHookSessionAnchor(instanceID); got != "" {
		t.Fatalf("session anchor = %q, want empty after SessionEnd cleanup", got)
	}
}

func TestWriteHookStatus_StopDoesNotClearStickySession(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	instanceID := "inst-stop"
	writeHookStatus(instanceID, "waiting", "sess-3", "SessionStart", "")
	writeHookStatus(instanceID, "waiting", "", "Stop", "")

	if got := session.ReadHookSessionAnchor(instanceID); got != "sess-3" {
		t.Fatalf("session anchor = %q, want sess-3", got)
	}
}

func TestIsTerminalHookEvent(t *testing.T) {
	tests := []struct {
		event  string
		expect bool
	}{
		{event: "SessionEnd", expect: true},
		{event: "session_end", expect: true},
		{event: "on_session_end", expect: false}, // Hermes per-turn event
		{event: "on_session_finalize", expect: true},
		{event: "session.closed", expect: true},
		{event: "ThreadClosed", expect: true},
		{event: "thread/terminated", expect: true},
		{event: "thread_done", expect: true},
		{event: "thread-exit", expect: true},
		{event: "Stop", expect: false},
		{event: "UserPromptSubmit", expect: false},
		{event: "thread_suspended", expect: false},
		{event: "session_pending", expect: false},
	}

	for _, tt := range tests {
		if got := isTerminalHookEvent(tt.event); got != tt.expect {
			t.Fatalf("isTerminalHookEvent(%q) = %v, want %v", tt.event, got, tt.expect)
		}
	}
}

// isolateTestHome points HOME and the XDG base dirs at a fresh tempdir so
// agent-deck runtime paths (hooks/) resolve off the developer's real home
// (2026-06-04 data-loss incident). Mirrors the HOME+XDG pattern used by the
// other cmd/agent-deck tests.
func isolateTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
}

// runHookHandlerWithPayload drives handleHookHandler end to end for a single
// payload: it points HOME at an isolated tempdir, sets AGENTDECK_INSTANCE_ID,
// feeds payloadJSON on stdin, and returns the decoded status file plus whether
// one was written. It restores os.Stdin before returning.
func runHookHandlerWithPayload(t *testing.T, instanceID, payloadJSON string) (hookStatusFile, bool) {
	t.Helper()
	isolateTestHome(t)
	t.Setenv("AGENTDECK_INSTANCE_ID", instanceID)
	// Keep the generation-locked and DSP paths out of the way: an unset
	// generation writes plainly; unset DSP mode keeps PermissionRequest from
	// emitting an allow decision (which is irrelevant to status retention).
	t.Setenv("AGENTDECK_HOOK_GENERATION", "")
	t.Setenv("AGENTDECK_DSP_MODE", "")

	f, err := os.CreateTemp(t.TempDir(), "hook-stdin-*")
	if err != nil {
		t.Fatalf("create stdin temp: %v", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(payloadJSON); err != nil {
		t.Fatalf("write stdin temp: %v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatalf("seek stdin temp: %v", err)
	}
	orig := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = orig }()

	handleHookHandler()
	_ = f.Close()

	data, err := os.ReadFile(filepath.Join(getHooksDir(), instanceID+".json"))
	if os.IsNotExist(err) {
		return hookStatusFile{}, false
	}
	if err != nil {
		t.Fatalf("read hook status file: %v", err)
	}
	var sf hookStatusFile
	if err := json.Unmarshal(data, &sf); err != nil {
		t.Fatalf("unmarshal hook status file: %v", err)
	}
	return sf, true
}

// TestHookHandler_RetainsMatcherAndMessage covers the human-ask-queue content
// retention: the decoded Notification matcher and Claude's human-readable
// message are persisted into the status file alongside status=="waiting", for
// both the permission-prompt and elicitation-dialog matchers, and a
// PermissionRequest (which carries a message but no matcher) retains its
// message. A plain informational notification and an ordinary Stop are asserted
// unchanged.
func TestHookHandler_RetainsMatcherAndMessage(t *testing.T) {
	tests := []struct {
		name        string
		payload     string
		wantWritten bool
		wantStatus  string
		wantMatcher string
		wantMessage string
	}{
		{
			name:        "notification permission_prompt",
			payload:     `{"hook_event_name":"Notification","matcher":"permission_prompt","message":"Claude wants to run: rm -rf build","session_id":"s1"}`,
			wantWritten: true,
			wantStatus:  "waiting",
			wantMatcher: "permission_prompt",
			wantMessage: "Claude wants to run: rm -rf build",
		},
		{
			name:        "notification elicitation_dialog",
			payload:     `{"hook_event_name":"Notification","matcher":"elicitation_dialog","message":"Which migration should I apply?","session_id":"s1"}`,
			wantWritten: true,
			wantStatus:  "waiting",
			wantMatcher: "elicitation_dialog",
			wantMessage: "Which migration should I apply?",
		},
		{
			name:        "permissionrequest carries message but no matcher",
			payload:     `{"hook_event_name":"PermissionRequest","message":"Approve tool use?","session_id":"s1"}`,
			wantWritten: true,
			wantStatus:  "waiting",
			wantMatcher: "",
			wantMessage: "Approve tool use?",
		},
		{
			name:        "plain informational notification writes nothing",
			payload:     `{"hook_event_name":"Notification","message":"just fyi, still working"}`,
			wantWritten: false,
		},
		{
			name:        "stop is unaffected",
			payload:     `{"hook_event_name":"Stop","session_id":"s1"}`,
			wantWritten: true,
			wantStatus:  "waiting",
			wantMatcher: "",
			wantMessage: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sf, written := runHookHandlerWithPayload(t, "inst-ask", tt.payload)
			if written != tt.wantWritten {
				t.Fatalf("status file written = %v, want %v (sf=%#v)", written, tt.wantWritten, sf)
			}
			if !tt.wantWritten {
				return
			}
			if sf.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", sf.Status, tt.wantStatus)
			}
			if sf.Matcher != tt.wantMatcher {
				t.Errorf("Matcher = %q, want %q", sf.Matcher, tt.wantMatcher)
			}
			if sf.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", sf.Message, tt.wantMessage)
			}
		})
	}
}

// TestHookHandler_MatcherMessageOmittedWhenEmpty verifies the omitempty
// contract: a record with no ask content (an ordinary Stop) writes neither the
// matcher nor the message JSON key, so pre-existing status files stay
// byte-compatible.
func TestHookHandler_MatcherMessageOmittedWhenEmpty(t *testing.T) {
	isolateTestHome(t)
	t.Setenv("AGENTDECK_INSTANCE_ID", "inst-omit")
	t.Setenv("AGENTDECK_HOOK_GENERATION", "")

	f, err := os.CreateTemp(t.TempDir(), "hook-stdin-*")
	if err != nil {
		t.Fatalf("create stdin temp: %v", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(`{"hook_event_name":"Stop","session_id":"s1"}`); err != nil {
		t.Fatalf("write stdin temp: %v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatalf("seek stdin temp: %v", err)
	}
	orig := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = orig }()

	handleHookHandler()
	_ = f.Close()

	data, err := os.ReadFile(filepath.Join(getHooksDir(), "inst-omit.json"))
	if err != nil {
		t.Fatalf("read hook status file: %v", err)
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatalf("unmarshal generic: %v", err)
	}
	if _, ok := generic["matcher"]; ok {
		t.Errorf("matcher key present in %s, want omitted", string(data))
	}
	if _, ok := generic["message"]; ok {
		t.Errorf("message key present in %s, want omitted", string(data))
	}
}

// TestParentIsDSP_EnvVarOverride verifies the explicit env-var path of the DSP
// detection used by handleHookHandler to emit a PermissionRequest allow.
// The env-var path is the cross-platform fallback when /proc is unavailable
// (macOS) or when the launch site wants to declare DSP intent explicitly.
func TestParentIsDSP_EnvVarOverride(t *testing.T) {
	t.Setenv("AGENTDECK_DSP_MODE", "1")
	if !parentIsDSP() {
		t.Errorf("parentIsDSP() = false; expected true when AGENTDECK_DSP_MODE=1")
	}
}

// TestParentIsDSP_NoOverrideNoFlag verifies that without the env var override
// AND without --dangerously-skip-permissions in the parent's cmdline,
// parentIsDSP returns false. The Go test runner is not launched with DSP, so
// /proc/<ppid>/cmdline should not contain the flag in normal CI.
func TestParentIsDSP_NoOverrideNoFlag(t *testing.T) {
	t.Setenv("AGENTDECK_DSP_MODE", "")
	if parentIsDSP() {
		t.Skipf("parentIsDSP() returned true unexpectedly; the test runner's parent appears to have --dangerously-skip-permissions in its cmdline. Skipping the negative assertion.")
	}
}
