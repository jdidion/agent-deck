package session

// Spec: docs/design/2026-10-23-ask-groups.md ("Kinds", "Hook content").
// Every assertion below names the spec rule it checks.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Spec "Kinds": "The existing `permission` kind is renamed `approval` and
// `question` is renamed `decision`."
func TestAskKindNames_RenamedToApprovalDecisionError(t *testing.T) {
	cases := []struct {
		got  AskKind
		want string
	}{
		{AskApproval, "approval"},
		{AskDecision, "decision"},
		{AskError, "error"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("kind constant = %q, want %q", c.got, c.want)
		}
	}
}

// The pre-revision classification table (hook event alone made an approval)
// is superseded by TestDeriveAsk_RevisedKindsTable in ask_pane_truth_test.go:
// spec "Kinds" now requires substate interactive-menu for approval/decision.

// Spec "Kinds": "Comparisons are case-insensitive." (pane shows the menu)
func TestDeriveAsk_EventAndMatcherCaseInsensitive(t *testing.T) {
	tests := []struct {
		name  string
		hs    *HookStatus
		wantK AskKind
	}{
		{"lowercase pretooluse", &HookStatus{Event: "pretooluse", ToolName: "Bash"}, AskApproval},
		{"uppercase PRETOOLUSE", &HookStatus{Event: "PRETOOLUSE", ToolName: "Bash"}, AskApproval},
		{"lowercase pretooluse AskUserQuestion", &HookStatus{Event: "pretooluse", ToolName: "AskUserQuestion"}, AskDecision},
		{"lowercase permissionrequest", &HookStatus{Event: "permissionrequest"}, AskApproval},
		{"mixed-case permission matcher", &HookStatus{Event: "Notification", Matcher: "Permission_Prompt"}, AskApproval},
		{"uppercase elicitation matcher", &HookStatus{Event: "Notification", Matcher: "ELICITATION_DIALOG"}, AskDecision},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, _, ok := deriveAsk(tt.hs, "waiting", SubstateInteractiveMenu)
			if !ok {
				t.Fatalf("not classified as an ask; want %q", tt.wantK)
			}
			if k != tt.wantK {
				t.Errorf("kind = %q, want %q", k, tt.wantK)
			}
		})
	}
}

// Spec "Hook content": "Summary for an `approval` from `PreToolUse`:
// `wants to run <ToolName>` when the message is empty."
func TestDeriveAsk_PreToolUseApprovalSummaryNamesTool(t *testing.T) {
	_, sum, ok := deriveAsk(&HookStatus{Event: "PreToolUse", ToolName: "Bash"}, "waiting", SubstateInteractiveMenu)
	if !ok {
		t.Fatal("PreToolUse+Bash while waiting with the menu open must be an ask")
	}
	if sum != "wants to run Bash" {
		t.Errorf("summary = %q, want %q", sum, "wants to run Bash")
	}
}

// Spec "Hook content": the tool-name summary applies only "when the message is
// empty", so a non-empty message wins.
func TestDeriveAsk_PreToolUseApprovalSummaryPrefersMessage(t *testing.T) {
	_, sum, ok := deriveAsk(&HookStatus{Event: "PreToolUse", ToolName: "Bash", Message: "Run the build?"}, "waiting", SubstateInteractiveMenu)
	if !ok {
		t.Fatal("PreToolUse+Bash while waiting with the menu open must be an ask")
	}
	if sum != "Run the build?" {
		t.Errorf("summary = %q, want the hook message", sum)
	}
}

// Spec "Kinds" (approval = waiting + interactive-menu) through the producer:
// syncAsks opens one row with the new kind name and the tool summary.
func TestSyncAsks_PreToolUseWaitingOpensApproval(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	setAskSubstate(t, askMenuAll)
	byID := map[string]*Instance{"a": {ID: "a"}}
	hooks := map[string]*HookStatus{"a": {Event: "PreToolUse", ToolName: "Bash"}}

	d.syncAsks(profile, db, byID, map[string]string{"a": "waiting"}, hooks)

	open, err := db.ListOpenAskItems()
	if err != nil {
		t.Fatalf("ListOpenAskItems: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open = %d, want 1 (a waiting session with an open menu must reach the queue)", len(open))
	}
	if open[0].Kind != "approval" {
		t.Errorf("stored kind = %q, want %q", open[0].Kind, "approval")
	}
	if open[0].Summary != "wants to run Bash" {
		t.Errorf("summary = %q, want %q", open[0].Summary, "wants to run Bash")
	}
}

// Spec "Kinds": menu + AskUserQuestion is a decision, stored as "decision".
func TestSyncAsks_AskUserQuestionWaitingOpensDecision(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	setAskSubstate(t, askMenuAll)
	byID := map[string]*Instance{"q": {ID: "q"}}
	hooks := map[string]*HookStatus{"q": {Event: "PreToolUse", ToolName: "AskUserQuestion"}}

	d.syncAsks(profile, db, byID, map[string]string{"q": "waiting"}, hooks)

	open, err := db.ListOpenAskItems()
	if err != nil {
		t.Fatalf("ListOpenAskItems: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open = %d, want 1", len(open))
	}
	if open[0].Kind != "decision" {
		t.Errorf("stored kind = %q, want %q", open[0].Kind, "decision")
	}
}

// Spec "Kinds": a running session is not an ask, even with a menu substate.
func TestSyncAsks_PreToolUseRunningOpensNothing(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	setAskSubstate(t, askMenuAll)
	byID := map[string]*Instance{"r": {ID: "r"}}
	hooks := map[string]*HookStatus{"r": {Event: "PreToolUse", ToolName: "Bash"}}

	d.syncAsks(profile, db, byID, map[string]string{"r": "running"}, hooks)

	if n := openAskCount(t, d, profile); n != 0 {
		t.Fatalf("open = %d, want 0 for a tool that is executing", n)
	}
}

// Spec "Hook content": tool_name is carried into HookStatus.ToolName "on every
// read path (watcher and the direct readHookStatusFile fallback)". The watcher
// has two decoders (processFile and scanDisk); all three are checked.
func TestHookStatusReaders_CarryToolName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := "ask-groups-tool-name"
	hooksDir := GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"status":"running","session_id":"s1","event":"PreToolUse","ts":` +
		jsonInt(time.Now().Unix()) + `,"tool_name":"Bash"}`
	if err := os.WriteFile(filepath.Join(hooksDir, id+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("readHookStatusFile", func(t *testing.T) {
		hs := readHookStatusFile(id)
		if hs == nil || hs.ToolName != "Bash" {
			t.Fatalf("readHookStatusFile ToolName = %+v, want Bash", hs)
		}
	})
	t.Run("watcher processFile", func(t *testing.T) {
		w := newTestWatcher(t, hooksDir)
		w.processFile(filepath.Join(hooksDir, id+".json"))
		hs := w.GetHookStatus(id)
		if hs == nil || hs.ToolName != "Bash" {
			t.Fatalf("processFile ToolName = %+v, want Bash", hs)
		}
	})
	t.Run("watcher scanDisk", func(t *testing.T) {
		w := newTestWatcher(t, hooksDir)
		hs := w.scanDisk()[id]
		if hs == nil || hs.ToolName != "Bash" {
			t.Fatalf("scanDisk ToolName = %+v, want Bash", hs)
		}
	})
}

// Spec "Hook content": tool_name is omitempty, so a legacy file without the key
// still decodes, with an empty ToolName.
func TestHookStatusReaders_MissingToolNameIsEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := "ask-groups-no-tool-name"
	hooksDir := GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"status":"waiting","session_id":"s1","event":"Stop","ts":` + jsonInt(time.Now().Unix()) + `}`
	if err := os.WriteFile(filepath.Join(hooksDir, id+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	hs := readHookStatusFile(id)
	if hs == nil {
		t.Fatal("legacy file without tool_name must still decode")
	}
	if hs.ToolName != "" {
		t.Errorf("ToolName = %q, want empty", hs.ToolName)
	}
}
