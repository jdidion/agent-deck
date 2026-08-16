package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func TestDeriveAsk(t *testing.T) {
	tests := []struct {
		name    string
		hs      *HookStatus
		status  string
		wantOK  bool
		wantK   AskKind
		wantSum string
	}{
		{"permissionrequest event", &HookStatus{Event: "permissionrequest"}, "waiting", true, AskPermission, "wants permission to run a command"},
		{"permission matcher with message", &HookStatus{Event: "notification", Matcher: "permission_prompt", Message: "Run npm test?"}, "waiting", true, AskPermission, "Run npm test?"},
		{"elicitation matcher", &HookStatus{Event: "notification", Matcher: "elicitation_dialog"}, "waiting", true, AskQuestion, "is asking a question"},
		{"error status, no hook", nil, "error", true, AskError, "hit an error"},
		{"error status with message", &HookStatus{Message: "push rejected"}, "error", true, AskError, "push rejected"},
		{"plain waiting, no matcher, is not an ask", nil, "waiting", false, "", ""},
		{"finished turn (stop) is not an ask", &HookStatus{Event: "stop"}, "waiting", false, "", ""},
		{"stale permission matcher while running is not an ask", &HookStatus{Matcher: "permission_prompt"}, "running", false, "", ""},
		{"idle is not an ask", nil, "idle", false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, sum, ok := deriveAsk(tt.hs, tt.status)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if k != tt.wantK {
				t.Errorf("kind = %q, want %q", k, tt.wantK)
			}
			if sum != tt.wantSum {
				t.Errorf("summary = %q, want %q", sum, tt.wantSum)
			}
		})
	}
}

func TestAskItemID(t *testing.T) {
	base := askItemID("inst-a", "jsonl:100")
	if askItemID("inst-a", "jsonl:100") != base {
		t.Error("same (instance, sig) must hash to the same id")
	}
	if askItemID("inst-a", "jsonl:200") == base {
		t.Error("a changed content signal must change the id (new turn = new ask)")
	}
	if askItemID("inst-b", "jsonl:100") == base {
		t.Error("a different instance must produce a different id")
	}
}

// openAskCount is a small helper: how many open items the store holds.
func openAskCount(t *testing.T, d *TransitionDaemon, profile string) int {
	t.Helper()
	db := daemonDB(t, d, profile)
	open, err := db.ListOpenAskItems()
	if err != nil {
		t.Fatalf("ListOpenAskItems: %v", err)
	}
	return len(open)
}

// THE IDEMPOTENCY GUARD. A pending ask is re-observed on every poll; the store
// is keyed on (instance, content signal) and upserts DO NOTHING, so syncAsks is
// level-triggered and repeated passes must not accumulate duplicate rows.
func TestSyncAsks_IdempotentOpen(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	inst := &Instance{ID: "a", Title: "flow"} // Tool "" -> no transcript -> sig ""
	byID := map[string]*Instance{"a": inst}
	statuses := map[string]string{"a": "waiting"}
	hooks := map[string]*HookStatus{"a": {Event: "notification", Matcher: "permission_prompt"}}

	for i := 0; i < 5; i++ {
		d.syncAsks(profile, db, byID, statuses, hooks)
	}
	if n := openAskCount(t, d, profile); n != 1 {
		t.Fatalf("open items = %d after 5 passes on one sustained ask, want 1", n)
	}
}

func TestSyncAsks_NonAskNotEnqueued(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	byID := map[string]*Instance{
		"finished": {ID: "finished"},
		"working":  {ID: "working"},
	}
	statuses := map[string]string{"finished": "waiting", "working": "running"}
	hooks := map[string]*HookStatus{
		"finished": {Event: "stop"}, // finished-and-waiting, not a request
	}
	d.syncAsks(profile, db, byID, statuses, hooks)
	if n := openAskCount(t, d, profile); n != 0 {
		t.Fatalf("open items = %d, want 0: a finished turn and a running session are not asks", n)
	}
}

func TestSyncAsks_ResolvesOnStatusLeave(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	inst := &Instance{ID: "a"}
	byID := map[string]*Instance{"a": inst}
	hooks := map[string]*HookStatus{"a": {Event: "permissionrequest"}}

	d.syncAsks(profile, db, byID, map[string]string{"a": "waiting"}, hooks)
	if n := openAskCount(t, d, profile); n != 1 {
		t.Fatalf("setup: open = %d, want 1", n)
	}
	// Session resumed; the hook status is now stale. The ask must close.
	d.syncAsks(profile, db, byID, map[string]string{"a": "running"}, hooks)
	if n := openAskCount(t, d, profile); n != 0 {
		t.Fatalf("open = %d after the session left waiting, want 0", n)
	}
}

func TestSyncAsks_ResolvesWhenInstanceGone(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	inst := &Instance{ID: "a"}
	d.syncAsks(profile, db, map[string]*Instance{"a": inst}, map[string]string{"a": "error"}, nil)
	if n := openAskCount(t, d, profile); n != 1 {
		t.Fatalf("setup: open = %d, want 1", n)
	}
	// Instance removed from the live set (deleted). Its ask is moot.
	d.syncAsks(profile, db, map[string]*Instance{}, map[string]string{}, nil)
	if n := openAskCount(t, d, profile); n != 0 {
		t.Fatalf("open = %d after the instance vanished, want 0", n)
	}
}

// The content-signal branch, isolated: a seeded item whose stored signal no
// longer matches the live instance's must resolve, and a matching one while
// still waiting must stay open.
func TestSyncAsks_ResolvesOnContentSigChange(t *testing.T) {
	d, db, profile := newAskDaemon(t)

	// Seed one open item whose signal is stale, one whose signal still matches.
	// The live instances have no transcript, so their current signal is "".
	seed := func(id, sig string) {
		row := &statedb.AskItemRow{
			ID:         askItemID(id, sig),
			InstanceID: id,
			Profile:    profile,
			Kind:       string(AskPermission),
			Summary:    "seeded",
			ContentSig: sig,
			CreatedAt:  time.Now(),
		}
		if err := db.UpsertAskItem(row); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("stale", "jsonl:500")
	seed("fresh", "") // matches the live "" signal

	byID := map[string]*Instance{"stale": {ID: "stale"}, "fresh": {ID: "fresh"}}
	statuses := map[string]string{"stale": "waiting", "fresh": "waiting"}

	d.syncAsks(profile, db, byID, statuses, nil) // no hooks -> open phase adds nothing

	open, err := db.ListOpenAskItems()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(open) != 1 || open[0].InstanceID != "fresh" {
		t.Fatalf("open items = %+v, want only the still-matching 'fresh' one; the stale-signal item must resolve", open)
	}
}

// End-to-end with a REAL growing transcript, proving the two behaviours that
// were hard for the desktop notifier: an answered ask resolves on transcript
// advance (no running snapshot required), and a genuine second prompt opens a
// NEW item rather than being suppressed.
func TestSyncAsks_RealTranscript_ResolveThenReopen(t *testing.T) {
	d, db, profile := newAskDaemon(t)

	inst := &Instance{
		ID:              "wh",
		Title:           "flow",
		Tool:            "claude",
		ClaudeSessionID: "11111111-1111-1111-1111-111111111111",
		ProjectPath:     t.TempDir(),
	}
	byID := map[string]*Instance{"wh": inst}
	perm := map[string]*HookStatus{"wh": {Event: "notification", Matcher: "permission_prompt", Message: "Run the build?"}}

	writeAskTranscript(t, inst, "line one\n")
	sig1 := transitionEventOutputHash(inst)
	if sig1 == "" {
		t.Fatal("precondition: transcript signal must be resolvable for this instance")
	}

	// Prompt 1.
	d.syncAsks(profile, db, byID, map[string]string{"wh": "waiting"}, perm)
	open, _ := db.ListOpenAskItems()
	if len(open) != 1 {
		t.Fatalf("prompt 1: open = %d, want 1", len(open))
	}
	firstID := open[0].ID
	if open[0].Summary != "Run the build?" {
		t.Errorf("summary = %q, want the hook message", open[0].Summary)
	}

	// Human answers: the agent resumes and appends to the transcript. No hook
	// ask now, status back to running.
	writeAskTranscript(t, inst, "line one\nline two — the answer and the work\n")
	d.syncAsks(profile, db, byID, map[string]string{"wh": "running"}, nil)
	if n := openAskCount(t, d, profile); n != 0 {
		t.Fatalf("after the answer, open = %d, want 0 (resolved on transcript advance)", n)
	}

	// Prompt 2: a genuinely new request, transcript grown further.
	writeAskTranscript(t, inst, "line one\nline two — the answer and the work\nline three\n")
	d.syncAsks(profile, db, byID, map[string]string{"wh": "waiting"}, perm)
	open, _ = db.ListOpenAskItems()
	if len(open) != 1 {
		t.Fatalf("prompt 2: open = %d, want 1 (a second prompt must re-alert)", len(open))
	}
	if open[0].ID == firstID {
		t.Error("prompt 2 reused the first ask's id; a new turn must be a new item")
	}
}

// --- helpers ---

func newAskDaemon(t *testing.T) (*TransitionDaemon, *statedb.StateDB, string) {
	t.Helper()
	inboxTestHome(t)
	profile := "_test-ask-queue"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	db := storage.GetDB()
	if db == nil {
		t.Fatal("nil db")
	}
	d := NewTransitionDaemon()
	d.storages[profile] = storage
	return d, db, profile
}

func daemonDB(t *testing.T, d *TransitionDaemon, profile string) *statedb.StateDB {
	t.Helper()
	s := d.storages[profile]
	if s == nil {
		t.Fatal("no storage for profile")
	}
	return s.GetDB()
}

// writeAskTranscript creates/overwrites the instance's Claude transcript.
// resolveClaudeTranscriptPath only resolves files that already exist, so the
// path is constructed the same way its primary candidate is — the
// symlink-resolved project path, Claude-encoded, under <config>/projects.
func writeAskTranscript(t *testing.T, inst *Instance, content string) {
	t.Helper()
	resolved := inst.ProjectPath
	if r, err := filepath.EvalSymlinks(inst.ProjectPath); err == nil {
		resolved = r
	}
	dir := filepath.Join(GetClaudeConfigDir(), "projects", ConvertToClaudeDirName(resolved))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, inst.ClaudeSessionID+".jsonl"), []byte(content), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}
