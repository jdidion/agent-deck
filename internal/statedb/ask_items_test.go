package statedb

import (
	"testing"
	"time"
)

// findAskItem returns the row with the given id from a slice, or nil.
func findAskItem(items []*AskItemRow, id string) *AskItemRow {
	for _, it := range items {
		if it.ID == id {
			return it
		}
	}
	return nil
}

func countAskItems(t *testing.T, db *StateDB) int {
	t.Helper()
	var n int
	if err := db.DB().QueryRow("SELECT COUNT(*) FROM ask_items").Scan(&n); err != nil {
		t.Fatalf("count ask_items: %v", err)
	}
	return n
}

func TestUpsertAndListOpenAskItems(t *testing.T) {
	db := newTestDB(t)

	created := time.Now().Add(-5 * time.Minute).Truncate(time.Nanosecond)
	row := &AskItemRow{
		ID:         "ask-1",
		InstanceID: "inst-1",
		Profile:    "default",
		Kind:       "permission",
		Summary:    "flow wants to run a command",
		ContentSig: "1234",
		Event:      "notification",
		CreatedAt:  created,
	}
	if err := db.UpsertAskItem(row); err != nil {
		t.Fatalf("UpsertAskItem: %v", err)
	}

	open, err := db.ListOpenAskItems()
	if err != nil {
		t.Fatalf("ListOpenAskItems: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("expected 1 open item, got %d", len(open))
	}
	got := open[0]
	if got.ID != row.ID || got.InstanceID != row.InstanceID || got.Profile != row.Profile ||
		got.Kind != row.Kind || got.Summary != row.Summary || got.ContentSig != row.ContentSig ||
		got.Event != row.Event {
		t.Fatalf("field mismatch: got %+v want %+v", got, row)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt round-trip: got %v want %v", got.CreatedAt, created)
	}
	if !got.ResolvedAt.IsZero() {
		t.Fatalf("open item ResolvedAt should be zero, got %v", got.ResolvedAt)
	}
}

func TestUpsertAskItemIdempotent(t *testing.T) {
	db := newTestDB(t)

	created := time.Now().Add(-10 * time.Minute).Truncate(time.Nanosecond)
	first := &AskItemRow{
		ID:         "ask-dup",
		InstanceID: "inst-1",
		Kind:       "question",
		Summary:    "original summary",
		ContentSig: "sig-a",
		CreatedAt:  created,
	}
	if err := db.UpsertAskItem(first); err != nil {
		t.Fatalf("first UpsertAskItem: %v", err)
	}

	// Same id, different summary and a later created_at: ON CONFLICT DO NOTHING
	// must leave the original row untouched.
	second := &AskItemRow{
		ID:         "ask-dup",
		InstanceID: "inst-1",
		Kind:       "question",
		Summary:    "CLOBBERED summary",
		ContentSig: "sig-a",
		CreatedAt:  created.Add(3 * time.Minute),
	}
	if err := db.UpsertAskItem(second); err != nil {
		t.Fatalf("second UpsertAskItem: %v", err)
	}

	if n := countAskItems(t, db); n != 1 {
		t.Fatalf("expected 1 row after idempotent upsert, got %d", n)
	}
	open, err := db.ListOpenAskItems()
	if err != nil {
		t.Fatalf("ListOpenAskItems: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("expected 1 open item, got %d", len(open))
	}
	if open[0].Summary != "original summary" {
		t.Fatalf("summary was clobbered: got %q", open[0].Summary)
	}
	if !open[0].CreatedAt.Equal(created) {
		t.Fatalf("created_at was clobbered: got %v want %v", open[0].CreatedAt, created)
	}
}

func TestResolveAskItem(t *testing.T) {
	db := newTestDB(t)

	row := &AskItemRow{ID: "ask-r", InstanceID: "inst-1", CreatedAt: time.Now()}
	if err := db.UpsertAskItem(row); err != nil {
		t.Fatalf("UpsertAskItem: %v", err)
	}

	resolveAt := time.Now().Truncate(time.Nanosecond)
	if err := db.ResolveAskItem("ask-r", resolveAt); err != nil {
		t.Fatalf("ResolveAskItem: %v", err)
	}

	open, err := db.ListOpenAskItems()
	if err != nil {
		t.Fatalf("ListOpenAskItems: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("resolved item should not be open, got %d open", len(open))
	}

	// Resolving again is a no-op: no error, still resolved, timestamp unchanged.
	if err := db.ResolveAskItem("ask-r", resolveAt.Add(time.Hour)); err != nil {
		t.Fatalf("second ResolveAskItem: %v", err)
	}
	all, err := db.ListAskItems(true, 0)
	if err != nil {
		t.Fatalf("ListAskItems: %v", err)
	}
	got := findAskItem(all, "ask-r")
	if got == nil {
		t.Fatalf("resolved item missing from ListAskItems(true)")
	}
	if !got.ResolvedAt.Equal(resolveAt) {
		t.Fatalf("re-resolve changed the timestamp: got %v want %v", got.ResolvedAt, resolveAt)
	}

	// Resolving an absent id is a no-op, not an error.
	if err := db.ResolveAskItem("does-not-exist", time.Now()); err != nil {
		t.Fatalf("ResolveAskItem(absent): %v", err)
	}
}

func TestListAskItemsIncludeResolved(t *testing.T) {
	db := newTestDB(t)

	if err := db.UpsertAskItem(&AskItemRow{ID: "open-1", InstanceID: "i", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("upsert open: %v", err)
	}
	if err := db.UpsertAskItem(&AskItemRow{ID: "res-1", InstanceID: "i", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("upsert to-resolve: %v", err)
	}
	if err := db.ResolveAskItem("res-1", time.Now()); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	withResolved, err := db.ListAskItems(true, 0)
	if err != nil {
		t.Fatalf("ListAskItems(true): %v", err)
	}
	if len(withResolved) != 2 {
		t.Fatalf("includeResolved=true expected 2, got %d", len(withResolved))
	}
	if findAskItem(withResolved, "res-1") == nil {
		t.Fatalf("resolved item missing when includeResolved=true")
	}

	onlyOpen, err := db.ListAskItems(false, 0)
	if err != nil {
		t.Fatalf("ListAskItems(false): %v", err)
	}
	if len(onlyOpen) != 1 || onlyOpen[0].ID != "open-1" {
		t.Fatalf("includeResolved=false expected only open-1, got %+v", onlyOpen)
	}
}

func TestListAskItemsLimitAndOrder(t *testing.T) {
	db := newTestDB(t)

	base := time.Now().Add(-time.Hour)
	for i, id := range []string{"a", "b", "c"} {
		row := &AskItemRow{
			ID:         id,
			InstanceID: "i",
			// stagger created_at so ordering is deterministic; c newest.
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := db.UpsertAskItem(row); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}

	all, err := db.ListAskItems(false, 0)
	if err != nil {
		t.Fatalf("ListAskItems: %v", err)
	}
	if len(all) != 3 || all[0].ID != "c" || all[2].ID != "a" {
		t.Fatalf("expected newest-first c,b,a; got %+v", all)
	}

	limited, err := db.ListAskItems(false, 2)
	if err != nil {
		t.Fatalf("ListAskItems limit: %v", err)
	}
	if len(limited) != 2 || limited[0].ID != "c" || limited[1].ID != "b" {
		t.Fatalf("limit=2 expected c,b; got %+v", limited)
	}
}

func TestPruneResolvedAskItems(t *testing.T) {
	db := newTestDB(t)

	now := time.Now()

	// Open item: never pruned regardless of age.
	if err := db.UpsertAskItem(&AskItemRow{ID: "open", InstanceID: "i", CreatedAt: now.Add(-24 * time.Hour)}); err != nil {
		t.Fatalf("upsert open: %v", err)
	}
	// Old resolved item: should be pruned.
	if err := db.UpsertAskItem(&AskItemRow{ID: "old-res", InstanceID: "i", CreatedAt: now.Add(-24 * time.Hour)}); err != nil {
		t.Fatalf("upsert old-res: %v", err)
	}
	if err := db.ResolveAskItem("old-res", now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("resolve old-res: %v", err)
	}
	// Recently resolved item: should be kept.
	if err := db.UpsertAskItem(&AskItemRow{ID: "recent-res", InstanceID: "i", CreatedAt: now.Add(-24 * time.Hour)}); err != nil {
		t.Fatalf("upsert recent-res: %v", err)
	}
	if err := db.ResolveAskItem("recent-res", now.Add(-1*time.Minute)); err != nil {
		t.Fatalf("resolve recent-res: %v", err)
	}

	cutoff := now.Add(-1 * time.Hour)
	if err := db.PruneResolvedAskItems(cutoff); err != nil {
		t.Fatalf("PruneResolvedAskItems: %v", err)
	}

	all, err := db.ListAskItems(true, 0)
	if err != nil {
		t.Fatalf("ListAskItems: %v", err)
	}
	if findAskItem(all, "old-res") != nil {
		t.Fatalf("old resolved item should have been pruned")
	}
	if findAskItem(all, "open") == nil {
		t.Fatalf("open item must survive prune")
	}
	if findAskItem(all, "recent-res") == nil {
		t.Fatalf("recently-resolved item must survive prune")
	}
}

func TestAskItemZeroResolvedRoundTrips(t *testing.T) {
	db := newTestDB(t)

	if err := db.UpsertAskItem(&AskItemRow{ID: "z", InstanceID: "i", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	all, err := db.ListAskItems(true, 0)
	if err != nil {
		t.Fatalf("ListAskItems: %v", err)
	}
	got := findAskItem(all, "z")
	if got == nil {
		t.Fatalf("item z missing")
	}
	if !got.ResolvedAt.IsZero() {
		t.Fatalf("zero ResolvedAt should round-trip as zero time, got %v (unix %d)", got.ResolvedAt, got.ResolvedAt.Unix())
	}
}
