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

// The content-signal branch, isolated: the lifecycle no longer keys open/
// resolve on ContentSig at all, so a seeded item whose stored signal no
// longer matches what a live re-derive would compute must STAY open as long
// as the instance is still in an attention status. This is the inverse of
// the old (buggy) expectation: under the previous content-sig-gated design a
// mismatched signal resolved the item every poll, which is exactly the
// churn this rewrite guards against.
func TestSyncAsks_ContentSigMismatchStaysOpenWhileWaiting(t *testing.T) {
	d, db, profile := newAskDaemon(t)

	// Seed two open items. The live instances have no transcript, so a live
	// re-derive of the signal is always "". "stale" was seeded with a signal
	// that no longer matches; "fresh" was seeded with a signal that still
	// does. Neither fact may matter to the lifecycle now.
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
	seed("fresh", "")

	byID := map[string]*Instance{"stale": {ID: "stale"}, "fresh": {ID: "fresh"}}
	statuses := map[string]string{"stale": "waiting", "fresh": "waiting"}

	d.syncAsks(profile, db, byID, statuses, nil)

	open, err := db.ListOpenAskItems()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("open items = %+v, want both still open: a ContentSig mismatch must not resolve an ask while its instance is still waiting", open)
	}
}

// TestSyncAsks_ReopensAfterResolveWithEmptySignal is the exact case that
// silently dropped a reopened ask before identity moved off the transcript
// content signal: an instance with NO resolvable transcript (Tool "", or any
// non-Claude tool -- transitionEventOutputHash returns "") whose ask is
// resolved and then reopens must get a fresh id, not collide with the
// resolved row's id. Under the old (instance, content-sig) identity, both
// opens hashed to the SAME id (both signals are ""), so the second open's
// INSERT ... ON CONFLICT(id) DO NOTHING silently no-opped against the
// already-resolved row and the reopen never surfaced.
func TestSyncAsks_ReopensAfterResolveWithEmptySignal(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	inst := &Instance{ID: "x", Title: "flow"} // Tool "" -> no transcript -> sig ""
	byID := map[string]*Instance{"x": inst}
	hooks := map[string]*HookStatus{"x": {Event: "permissionrequest"}}

	// 1. Open.
	d.syncAsks(profile, db, byID, map[string]string{"x": "waiting"}, hooks)
	open, err := db.ListOpenAskItems()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("setup: open = %d, want 1", len(open))
	}
	firstID := open[0].ID

	// 2. Answered: the status leaves waiting, resolving the ask.
	d.syncAsks(profile, db, byID, map[string]string{"x": "running"}, hooks)
	if n := openAskCount(t, d, profile); n != 0 {
		t.Fatalf("open = %d after the answer, want 0 (resolved)", n)
	}

	// The per-open discriminator is the open timestamp (UnixNano). Force a
	// distinct nanosecond between the first open and the reopen below so the
	// id is guaranteed to differ even on a fast test run where time.Now()
	// could otherwise repeat.
	time.Sleep(2 * time.Millisecond)

	// 3. Reopen: a genuine second prompt on the same instance, same (empty)
	// content signal.
	d.syncAsks(profile, db, byID, map[string]string{"x": "waiting"}, hooks)
	open, err = db.ListOpenAskItems()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open = %d after reopen, want 1", len(open))
	}
	if open[0].ID == firstID {
		t.Error("reopen after resolve reused the first ask's id; with an empty content signal this collides with the resolved row and the upsert's ON CONFLICT(id) DO NOTHING silently drops the reopen")
	}
}

// TestSyncAsks_PrunesOldResolvedItems: syncAsks throttles PruneResolvedAskItems
// per profile (askPruneInterval) and, when it runs, deletes resolved items
// older than askResolvedRetention while leaving open items and
// recently-resolved items alone.
func TestSyncAsks_PrunesOldResolvedItems(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	now := time.Now()

	oldRow := &statedb.AskItemRow{
		ID:         askItemID("old", "1"),
		InstanceID: "old",
		Profile:    profile,
		Kind:       string(AskPermission),
		Summary:    "old ask",
		CreatedAt:  now.Add(-72 * time.Hour),
	}
	if err := db.UpsertAskItem(oldRow); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	if err := db.ResolveAskItem(oldRow.ID, now.Add(-50*time.Hour)); err != nil {
		t.Fatalf("resolve old: %v", err)
	}

	recentRow := &statedb.AskItemRow{
		ID:         askItemID("recent", "1"),
		InstanceID: "recent",
		Profile:    profile,
		Kind:       string(AskPermission),
		Summary:    "recent ask",
		CreatedAt:  now.Add(-1 * time.Hour),
	}
	if err := db.UpsertAskItem(recentRow); err != nil {
		t.Fatalf("seed recent: %v", err)
	}
	if err := db.ResolveAskItem(recentRow.ID, now.Add(-30*time.Minute)); err != nil {
		t.Fatalf("resolve recent: %v", err)
	}

	// newAskDaemon's lastAskPrune map has no entry for profile, so this call's
	// throttle check (IsZero()) fires the prune.
	d.syncAsks(profile, db, map[string]*Instance{}, map[string]string{}, nil)

	items, err := db.ListAskItems(true, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var gotOld, gotRecent bool
	for _, item := range items {
		if item.ID == oldRow.ID {
			gotOld = true
		}
		if item.ID == recentRow.ID {
			gotRecent = true
		}
	}
	if gotOld {
		t.Error("a resolved item older than askResolvedRetention (48h) should have been pruned")
	}
	if !gotRecent {
		t.Error("a recently-resolved item (well within askResolvedRetention) should still be present")
	}
}

// End-to-end with a REAL growing transcript, proving the behaviours that
// were hard for the desktop notifier and the whole point of the status-driven
// rewrite: a sustained wait collapses to a single ask no matter how much the
// transcript grows underneath it, resolution happens only when the status
// leaves the attention set, and a genuine second prompt opens a NEW item
// rather than being suppressed.
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

	// The agent is still blocked on the SAME prompt, but its transcript keeps
	// growing underneath it — this is the exact regression the rewrite
	// guards against. Status stays waiting; the ask must neither resolve nor
	// duplicate.
	writeAskTranscript(t, inst, "line one\nline two -- still blocked, transcript grew\n")
	d.syncAsks(profile, db, byID, map[string]string{"wh": "waiting"}, perm)
	open, _ = db.ListOpenAskItems()
	if len(open) != 1 {
		t.Fatalf("sustained wait with transcript growth: open = %d, want 1 (no duplicate)", len(open))
	}
	if open[0].ID != firstID {
		t.Error("sustained wait with transcript growth changed the ask id; growth alone must not open a new ask")
	}

	// Human answers: the agent resumes. No hook ask now, status back to
	// running. Resolution is on the status leaving waiting, not on transcript
	// advance.
	writeAskTranscript(t, inst, "line one\nline two -- still blocked, transcript grew\nline three -- the answer and the work\n")
	d.syncAsks(profile, db, byID, map[string]string{"wh": "running"}, nil)
	if n := openAskCount(t, d, profile); n != 0 {
		t.Fatalf("after the answer, open = %d, want 0 (resolved on status leaving waiting)", n)
	}

	// Prompt 2: a genuinely new request, transcript grown further.
	writeAskTranscript(t, inst, "line one\nline two -- still blocked, transcript grew\nline three -- the answer and the work\nline four\n")
	d.syncAsks(profile, db, byID, map[string]string{"wh": "waiting"}, perm)
	open, _ = db.ListOpenAskItems()
	if len(open) != 1 {
		t.Fatalf("prompt 2: open = %d, want 1 (a second prompt must re-alert)", len(open))
	}
	if open[0].ID == firstID {
		t.Error("prompt 2 reused the first ask's id; a new turn must be a new item")
	}
}

// Direct repro of the measured production failure: a single unanswered
// permission prompt whose transcript keeps growing underneath it (469 rows
// observed for one instance under the old content-sig-gated design). With
// the status-driven lifecycle, the open count must stay exactly 1 after
// EVERY pass, no matter how many times the transcript grows while the
// instance sits in `waiting`.
func TestSyncAsks_SustainedWaitGrowingTranscriptStaysSingleOpen(t *testing.T) {
	d, db, profile := newAskDaemon(t)

	inst := &Instance{
		ID:              "wh",
		Title:           "flow",
		Tool:            "claude",
		ClaudeSessionID: "22222222-2222-2222-2222-222222222222",
		ProjectPath:     t.TempDir(),
	}
	byID := map[string]*Instance{"wh": inst}
	perm := map[string]*HookStatus{"wh": {Event: "notification", Matcher: "permission_prompt", Message: "Run the build?"}}
	statuses := map[string]string{"wh": "waiting"}

	var firstID string
	content := "line one\n"
	for i := 0; i < 8; i++ {
		content += "more agent output emitted while still blocked on the same prompt\n"
		writeAskTranscript(t, inst, content)
		d.syncAsks(profile, db, byID, statuses, perm)

		open, err := db.ListOpenAskItems()
		if err != nil {
			t.Fatalf("iteration %d: list: %v", i, err)
		}
		if len(open) != 1 {
			t.Fatalf("iteration %d: open = %d, want exactly 1 despite a growing transcript under a sustained wait", i, len(open))
		}
		if i == 0 {
			firstID = open[0].ID
		} else if open[0].ID != firstID {
			t.Fatalf("iteration %d: ask id changed from %q to %q; a sustained wait must stay a single ask", i, firstID, open[0].ID)
		}
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
