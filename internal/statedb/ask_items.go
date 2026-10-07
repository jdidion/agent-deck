package statedb

import (
	"database/sql"
	"time"
)

// AskItemRow is one row of the ask_items table: an open (or resolved) request
// an agent has made of the human. This is a dumb persistence record — the
// producer daemon owns all policy (kind derivation, identity/dedup, when to
// resolve). See docs/design/2026-08-14-human-ask-queue.md.
//
// ResolvedAt is the zero time.Time while the ask is open, and a real timestamp
// once resolved. At the DB boundary it maps to integer 0 (open) or unix-nano.
type AskItemRow struct {
	ID         string
	InstanceID string
	Profile    string
	Kind       string
	Summary    string
	ContentSig string
	Event      string
	CreatedAt  time.Time
	ResolvedAt time.Time // zero == open
}

// askItemNano converts a time.Time to the INTEGER unix-nano value stored in
// ask_items. A zero time maps to 0 so an open item's resolved_at reads as 0
// (never the unix epoch). Mirrors how Touch() uses time.Now().UnixNano().
func askItemNano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

// askItemTime is the inverse of askItemNano: 0 maps back to the zero time.Time
// (open), any other value to the corresponding instant.
func askItemTime(nanos int64) time.Time {
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos)
}

const askItemColumns = `id, instance_id, profile, kind, summary, content_sig, event, created_at, resolved_at`

// UpsertAskItem inserts an ask item, or does nothing if one with the same id
// already exists (INSERT ... ON CONFLICT(id) DO NOTHING). Re-opening an ask
// the producer has already recorded is therefore a no-op that never clobbers
// created_at or summary — the idempotent-open contract the producer relies on
// (a sustained wait re-observed across polls yields the same id).
//
// Wrapped in withBusyRetry like the other short writers; the insert is
// idempotent so retrying on SQLITE_BUSY is safe.
func (s *StateDB) UpsertAskItem(row *AskItemRow) error {
	return withBusyRetry(func() error {
		_, err := s.db.Exec(`
			INSERT INTO ask_items (
				id, instance_id, profile, kind, summary, content_sig, event,
				created_at, resolved_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO NOTHING
		`,
			row.ID, row.InstanceID, row.Profile, row.Kind, row.Summary,
			row.ContentSig, row.Event,
			askItemNano(row.CreatedAt), askItemNano(row.ResolvedAt),
		)
		return err
	})
}

// scanAskItems decodes rows from a `SELECT askItemColumns FROM ask_items`
// query into AskItemRows, converting the unix-nano timestamp columns back to
// time.Time (0 -> zero time). Shared by the two list methods.
func scanAskItems(rows *sql.Rows) ([]*AskItemRow, error) {
	defer rows.Close()
	var result []*AskItemRow
	for rows.Next() {
		r := &AskItemRow{}
		var createdNano, resolvedNano int64
		if err := rows.Scan(
			&r.ID, &r.InstanceID, &r.Profile, &r.Kind, &r.Summary,
			&r.ContentSig, &r.Event, &createdNano, &resolvedNano,
		); err != nil {
			return nil, err
		}
		r.CreatedAt = askItemTime(createdNano)
		r.ResolvedAt = askItemTime(resolvedNano)
		result = append(result, r)
	}
	return result, rows.Err()
}

// ListOpenAskItems returns every open ask item (resolved_at == 0), newest
// first. Read-only.
func (s *StateDB) ListOpenAskItems() ([]*AskItemRow, error) {
	rows, err := s.db.Query(`
		SELECT ` + askItemColumns + `
		FROM ask_items
		WHERE resolved_at = 0
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	return scanAskItems(rows)
}

// ListAskItems returns ask items newest first. When includeResolved is false,
// only open items are returned. A limit <= 0 means no limit. Read-only.
func (s *StateDB) ListAskItems(includeResolved bool, limit int) ([]*AskItemRow, error) {
	query := `SELECT ` + askItemColumns + ` FROM ask_items`
	if !includeResolved {
		query += ` WHERE resolved_at = 0`
	}
	query += ` ORDER BY created_at DESC`
	var args []any
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	return scanAskItems(rows)
}

// ResolveAskItem marks the item resolved as of at. Resolving an item that is
// already resolved (or absent) is a no-op — the WHERE clause requires
// resolved_at == 0, so a second resolve matches zero rows and leaves the
// original resolution timestamp intact.
//
// Wrapped in withBusyRetry: the conditional UPDATE is idempotent (a retry that
// runs after the first attempt already committed simply matches zero rows).
func (s *StateDB) ResolveAskItem(id string, at time.Time) error {
	return withBusyRetry(func() error {
		_, err := s.db.Exec(
			`UPDATE ask_items SET resolved_at = ? WHERE id = ? AND resolved_at = 0`,
			askItemNano(at), id,
		)
		return err
	})
}

// PruneResolvedAskItems deletes resolved items whose resolved_at is before the
// given cutoff, bounding table growth. Open items (resolved_at == 0) and
// recently-resolved items are never touched.
//
// Wrapped in withBusyRetry: a bounded DELETE is idempotent and safe to retry.
func (s *StateDB) PruneResolvedAskItems(before time.Time) error {
	return withBusyRetry(func() error {
		_, err := s.db.Exec(
			`DELETE FROM ask_items WHERE resolved_at != 0 AND resolved_at < ?`,
			askItemNano(before),
		)
		return err
	})
}
