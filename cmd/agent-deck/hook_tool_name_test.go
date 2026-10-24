package main

// Spec docs/design/2026-10-23-ask-groups.md "Hook content": "The hook handler
// retains `tool_name` from the payload next to `matcher` and `message`, in the
// hook status file (`tool_name`, omitempty)".

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// readRawHookStatus returns the status file for instanceID as a generic map so
// key presence (omitempty) can be checked independently of the struct.
func readRawHookStatus(t *testing.T, instanceID string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(getHooksDir(), instanceID+".json"))
	if err != nil {
		t.Fatalf("read hook status file: %v", err)
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatalf("unmarshal hook status file: %v", err)
	}
	return generic
}

// Spec "Hook content": PreToolUse is the last hook before a permission prompt
// (spec Problem 1), so its tool_name must reach the status file.
func TestHookHandler_PreToolUseRetainsToolName(t *testing.T) {
	// Only the installed synchronous receiver publishes PreToolUse.
	t.Setenv(session.PreToolHookSyncMarkerEnv, "1")
	sf, written := runHookHandlerWithPayload(t, "inst-tool",
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"},"session_id":"s1"}`)
	if !written {
		t.Fatal("PreToolUse wrote no status file")
	}
	if sf.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want %q", sf.ToolName, "Bash")
	}
	raw := readRawHookStatus(t, "inst-tool")
	var got string
	if err := json.Unmarshal(raw["tool_name"], &got); err != nil || got != "Bash" {
		t.Errorf(`status file "tool_name" = %s, want "Bash"`, raw["tool_name"])
	}
}

// Spec "Kinds": PreToolUse+AskUserQuestion is a decision, so the tool name must
// survive for that tool too.
func TestHookHandler_PreToolUseRetainsAskUserQuestionToolName(t *testing.T) {
	t.Setenv(session.PreToolHookSyncMarkerEnv, "1")
	sf, written := runHookHandlerWithPayload(t, "inst-auq",
		`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"s1"}`)
	if !written {
		t.Fatal("PreToolUse wrote no status file")
	}
	if sf.ToolName != "AskUserQuestion" {
		t.Errorf("ToolName = %q, want %q", sf.ToolName, "AskUserQuestion")
	}
}

// Spec "Hook content": tool_name is retained "next to matcher and message", so
// a PermissionRequest carrying a tool_name keeps it alongside its message.
func TestHookHandler_PermissionRequestRetainsToolNameAndMessage(t *testing.T) {
	sf, written := runHookHandlerWithPayload(t, "inst-perm",
		`{"hook_event_name":"PermissionRequest","tool_name":"Edit","message":"Approve edit?","session_id":"s1"}`)
	if !written {
		t.Fatal("PermissionRequest wrote no status file")
	}
	if sf.ToolName != "Edit" {
		t.Errorf("ToolName = %q, want %q", sf.ToolName, "Edit")
	}
	if sf.Message != "Approve edit?" {
		t.Errorf("Message = %q, want %q", sf.Message, "Approve edit?")
	}
}

// Spec "Hook content": "(`tool_name`, omitempty)". A Stop has no tool, so the
// key must be absent.
func TestHookHandler_ToolNameOmittedWhenEmpty(t *testing.T) {
	_, written := runHookHandlerWithPayload(t, "inst-stop", `{"hook_event_name":"Stop","session_id":"s1"}`)
	if !written {
		t.Fatal("Stop wrote no status file")
	}
	raw := readRawHookStatus(t, "inst-stop")
	if _, ok := raw["tool_name"]; ok {
		t.Errorf("tool_name key present on a Stop record, want omitted: %s", raw["tool_name"])
	}
}
