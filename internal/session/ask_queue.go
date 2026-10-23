package session

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

var askLog = logging.ForComponent(logging.CompSession)

// AskKind classifies a request an agent has made of the human. See
// docs/design/2026-08-14-human-ask-queue.md.
type AskKind string

const (
	AskPermission AskKind = "permission" // permissionrequest / notification+permission_prompt
	AskQuestion   AskKind = "question"   // notification+elicitation_dialog
	AskError      AskKind = "error"      // session entered the error status
)

// askItemID is the stable identity of an ask. The same (instance, content
// signal) always hashes to the same id, so re-observing one pending ask across
// polls collapses onto a single row — the store upsert is ON CONFLICT(id) DO
// NOTHING. A genuine new turn changes the content signal, and therefore the id.
func askItemID(instanceID, sig string) string {
	sum := sha256.Sum256([]byte(instanceID + "\x00" + sig))
	return hex.EncodeToString(sum[:16])
}

// deriveAsk classifies an instance's current state as an ask, or reports
// ok == false when it is not a request the human owes a response to.
//
// Scoped to explicit requests and errors. A plain finished-and-waiting turn
// (a Stop with no permission/elicitation matcher) is deliberately NOT an ask:
// the queue is for things the human is being asked to DO, and enqueuing every
// idle-at-the-prompt session would drown the real requests. This is the same
// judgement that excludes idle from the desktop notifier.
func deriveAsk(hs *HookStatus, toStatus string) (AskKind, string, bool) {
	st := normalizeStatusString(toStatus)
	var event, matcher, msg string
	if hs != nil {
		event = strings.ToLower(strings.TrimSpace(hs.Event))
		matcher = strings.TrimSpace(hs.Matcher)
		msg = strings.TrimSpace(hs.Message)
	}
	// A permission/question ask requires the session to actually be waiting.
	// The hook status is the LATEST event and can be stale: a session that
	// answered a prompt and resumed carries a running status with a
	// still-permission-shaped matcher for a beat. Gating on the live status
	// stops that stale record from re-opening a resolved ask.
	switch {
	case st == string(StatusWaiting) && (event == "permissionrequest" || matcher == "permission_prompt"):
		return AskPermission, summaryOr(msg, "wants permission to run a command"), true
	case st == string(StatusWaiting) && matcher == "elicitation_dialog":
		return AskQuestion, summaryOr(msg, "is asking a question"), true
	case st == string(StatusError):
		return AskError, summaryOr(msg, "hit an error"), true
	default:
		return "", "", false
	}
}

func summaryOr(s, fallback string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return fallback
}

// isAttentionStatus is the set of statuses that keep an ask open. Deliberately
// narrow: only waiting and error. Any other status means the session is no
// longer asking, which is one of the resolution triggers below.
func isAttentionStatus(status string) bool {
	s := normalizeStatusString(status)
	return s == string(StatusWaiting) || s == string(StatusError)
}

// syncAsks reconciles the ask_items store with the live state of one profile,
// once per poll pass.
//
// It is LEVEL-triggered, not edge-triggered, and that is the whole reason it is
// a self-contained pass rather than a hook into the transition-notifier edge
// machinery. The store keys each ask on (instance, content signal) and upserts
// ON CONFLICT DO NOTHING, so simply asserting "this ask exists" every pass is
// idempotent: the first pass inserts, every later pass while the ask is pending
// is a no-op. No transition edge, no from/to bookkeeping, and no dependence on
// the daemon having observed the running snapshot that preceded the ask.
//
// Best-effort throughout: a store error is logged, never fatal, and never
// stalls the poll loop. The calls are local SQLite writes behind withBusyRetry,
// so unlike the desktop notifier (which shells out) they need no goroutine.
func (d *TransitionDaemon) syncAsks(
	profile string,
	db *statedb.StateDB,
	byID map[string]*Instance,
	statuses map[string]string,
	hookStatuses map[string]*HookStatus,
) {
	if db == nil {
		return
	}

	// Open (idempotent). A pending ask appends nothing to the transcript, so
	// its content signal — and thus its id — is stable across polls, and the
	// upsert is a no-op after the first observation.
	for id, inst := range byID {
		if inst == nil {
			continue
		}
		kind, summary, ok := deriveAsk(hookStatuses[id], statuses[id])
		if !ok {
			continue
		}
		sig := transitionEventOutputHash(inst)
		row := &statedb.AskItemRow{
			ID:         askItemID(id, sig),
			InstanceID: id,
			Profile:    profile,
			Kind:       string(kind),
			Summary:    summary,
			ContentSig: sig,
			Event:      hookEventOf(hookStatuses[id]),
			CreatedAt:  time.Now(),
		}
		if err := db.UpsertAskItem(row); err != nil {
			askLog.Debug("ask_upsert_failed",
				slog.String("instance_id", id), slog.String("error", err.Error()))
		}
	}

	// Resolve. An open item closes when its session has moved on: deleted, no
	// longer in an attention status, or its transcript advanced past the signal
	// the ask opened at (the ask was answered and the agent resumed).
	//
	// Keying resolution on the content signal, not on an observed running
	// snapshot, is what makes a waiting -> idle -> waiting fast turn resolve
	// correctly: the answered turn always appends to the transcript, so the
	// signal moves even when the daemon never sampled a running status in
	// between. The status check is the fallback for sessions with no resolvable
	// transcript (whose signal never moves), so an errored shell still clears.
	open, err := db.ListOpenAskItems()
	if err != nil {
		askLog.Debug("ask_list_open_failed", slog.String("error", err.Error()))
		return
	}
	now := time.Now()
	for _, item := range open {
		inst, live := byID[item.InstanceID]
		switch {
		case !live:
			d.resolveAsk(db, item.ID, now)
		case transitionEventOutputHash(inst) != item.ContentSig:
			d.resolveAsk(db, item.ID, now)
		case !isAttentionStatus(statuses[item.InstanceID]):
			d.resolveAsk(db, item.ID, now)
		}
	}
}

func (d *TransitionDaemon) resolveAsk(db *statedb.StateDB, id string, at time.Time) {
	if err := db.ResolveAskItem(id, at); err != nil {
		askLog.Debug("ask_resolve_failed",
			slog.String("id", id), slog.String("error", err.Error()))
	}
}

func hookEventOf(hs *HookStatus) string {
	if hs == nil {
		return ""
	}
	return hs.Event
}
