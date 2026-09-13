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
// It is LEVEL-triggered and STATUS-driven: an ask is open for exactly as long as
// its instance sits in an attention status (waiting/error), and there is at most
// one open ask per instance. The lifecycle does NOT key on the transcript size.
// An earlier design did, on the assumption that a waiting session appends nothing
// so its content signal is stable across polls. That assumption is false — a
// long-lived managed agent keeps appending to its JSONL even while it blocks on a
// permission prompt, so the signal moved every poll and one unanswered prompt
// churned into hundreds of create-then-resolve rows, leaving the open-only panel
// almost always empty. ContentSig is kept as metadata but no longer gates open or
// resolve.
//
// Resolve runs before open so that an instance whose ask just closed (it left the
// attention status) is eligible to open a fresh one in the same pass only when it
// is still asking — which, by definition of "left the attention status", it is
// not. A genuine second prompt after the human answers re-alerts: answering moves
// the status out of waiting, resolving the first ask, so the next waiting finds no
// open ask and opens a new one.
//
// Best-effort throughout: a store error is logged, never fatal, and never stalls
// the poll loop. The calls are local SQLite writes behind withBusyRetry, so
// unlike the desktop notifier (which shells out) they need no goroutine.
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

	open, err := db.ListOpenAskItems()
	if err != nil {
		askLog.Debug("ask_list_open_failed", slog.String("error", err.Error()))
		return
	}
	now := time.Now()

	// Resolve. An open item closes when its session is gone from the live set,
	// or is no longer in an attention status (the ask was answered and the agent
	// moved on). hasOpen tracks which instances still hold an open ask after this
	// pass, so the open phase below does not create a duplicate for them.
	hasOpen := make(map[string]bool, len(open))
	for _, item := range open {
		if _, live := byID[item.InstanceID]; !live || !isAttentionStatus(statuses[item.InstanceID]) {
			d.resolveAsk(db, item.ID, now)
			continue
		}
		hasOpen[item.InstanceID] = true
	}

	// Open. One ask per instance: skip any that already has one open. A pending
	// ask is re-derived every pass, so this gate — not the transcript signal — is
	// what makes a sustained wait collapse to a single row.
	for id, inst := range byID {
		if inst == nil || hasOpen[id] {
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
			CreatedAt:  now,
		}
		if err := db.UpsertAskItem(row); err != nil {
			askLog.Debug("ask_upsert_failed",
				slog.String("instance_id", id), slog.String("error", err.Error()))
		}
		hasOpen[id] = true
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
