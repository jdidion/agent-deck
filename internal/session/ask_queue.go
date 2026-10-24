package session

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strconv"
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
	AskApproval AskKind = "approval" // waiting on a tool approval (permission prompt, PreToolUse, plan approval)
	AskDecision AskKind = "decision" // waiting on a multiple-choice or free-text answer
	AskError    AskKind = "error"    // session entered the error status
)

// NormalizeAskKind maps a stored kind to its current name. Rows written before
// the approval/decision rename carry "permission" and "question".
func NormalizeAskKind(kind string) AskKind {
	switch k := AskKind(strings.ToLower(strings.TrimSpace(kind))); k {
	case "permission":
		return AskApproval
	case "question":
		return AskDecision
	default:
		return k
	}
}

// DeriveAskKind is the kind-only form of deriveAsk, for callers outside the
// producer (the TUI row glyphs) that need the same classification.
func DeriveAskKind(hs *HookStatus, status string, substate Substate) AskKind {
	kind, _, _ := deriveAsk(hs, status, substate)
	return kind
}

// askCapture captures a waiting instance's pane for syncAsks and classifies
// it. A non-nil error means the substate is unknown. An instance without a
// tmux session has no menu.
var askCapture = func(inst *Instance) (Substate, error) {
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		return SubstateNone, nil
	}
	return tmuxSess.CaptureSubstate()
}

// askNow is the clock the ask capture cache reads.
var askNow = time.Now

// askCaptureTTL is how long syncAsks reuses an instance's last capture before
// taking a new one.
const askCaptureTTL = 5 * time.Second

// askResolveMisses is how many consecutive fresh captures without a menu
// resolve an open approval or decision. One missed frame (a redraw caught
// mid-capture) must not close the row and reopen it under a new id.
const askResolveMisses = 2

// askCaptureEntry is the last successful capture of one waiting instance.
type askCaptureEntry struct {
	sub    Substate
	at     time.Time
	misses int // consecutive fresh captures without a menu
}

// askObservation is what one syncAsks pass knows about a waiting instance's
// pane. known is false when the capture errored, timed out or was skipped for
// the pass budget.
type askObservation struct {
	sub    Substate
	known  bool
	misses int
}

// observeAskSubstates reads the substate of every waiting instance in byID,
// sequentially. A capture younger than askCaptureTTL is reused; otherwise the
// pane is captured under statusProbeBudget, and once syncPassBudget (measured
// from start) is spent the remaining instances are left unknown. Captures run
// on the daemon's single-threaded poll loop, so both budgets keep a wedged
// tmux server from stalling every profile. Instances that are not waiting
// drop their cache entry, so a later wait starts from a fresh capture.
func (d *TransitionDaemon) observeAskSubstates(profile string, byID map[string]*Instance, statuses map[string]string, start time.Time) map[string]askObservation {
	prev := d.askCaptures[profile]
	next := make(map[string]askCaptureEntry)
	obs := make(map[string]askObservation)
	for id, inst := range byID {
		if inst == nil || normalizeStatusString(statuses[id]) != string(StatusWaiting) {
			continue
		}
		entry, cached := prev[id]
		if cached && askNow().Sub(entry.at) < askCaptureTTL {
			next[id] = entry
			obs[id] = askObservation{sub: entry.sub, known: true, misses: entry.misses}
			continue
		}
		if cached {
			// Expired, but its miss run carries over to the next capture.
			next[id] = entry
		}
		if time.Since(start) > syncPassBudget {
			d.logProbeStall(profile, "", "ask_pass_budget")
			continue
		}
		sub, timedOut, err := askCaptureBounded(inst)
		if timedOut {
			d.logProbeStall(profile, id, "ask_probe_budget")
			continue
		}
		if err != nil {
			askLog.Debug("ask_capture_failed",
				slog.String("instance_id", id), slog.String("error", err.Error()))
			continue
		}
		misses := 0
		if sub != SubstateInteractiveMenu {
			misses = entry.misses + 1
		}
		next[id] = askCaptureEntry{sub: sub, at: askNow(), misses: misses}
		obs[id] = askObservation{sub: sub, known: true, misses: misses}
	}
	d.askCaptures[profile] = next
	return obs
}

// askCaptureBounded runs askCapture under statusProbeBudget. On timeout the
// capture is abandoned; its goroutine finishes on its own.
func askCaptureBounded(inst *Instance) (sub Substate, timedOut bool, err error) {
	type result struct {
		sub Substate
		err error
	}
	done := make(chan result, 1)
	go func() {
		sub, err := askCapture(inst)
		done <- result{sub, err}
	}()
	select {
	case r := <-done:
		return r.sub, false, r.err
	case <-time.After(statusProbeBudget):
		return SubstateNone, true, nil
	}
}

const (
	// askPruneInterval rate-limits PruneResolvedAskItems so it runs at most once
	// per profile per interval instead of every poll.
	askPruneInterval = time.Hour
	// askResolvedRetention is how long a resolved ask row is kept before pruning,
	// so a recently-closed request stays inspectable for a while but the table
	// stays bounded for a long-lived profile.
	askResolvedRetention = 48 * time.Hour
)

// askItemID is the identity of one open ask, hashed from the instance and a
// per-open discriminator.
//
// Identity must NOT be derived from the transcript content signal. That was the
// original scheme and it had two failure modes, both proven in review: when the
// signal is empty (every non-Claude tool, remote/SSH sessions, and any Claude
// session before its transcript file exists) or merely unchanged between two
// turns, a second ask for the instance hashed to the SAME id as the first —
// which, once the first was resolved, made the ON CONFLICT(id) DO NOTHING upsert
// a silent no-op, so the reopen never surfaced. Idempotency for a SUSTAINED wait
// is enforced by the hasOpen gate in syncAsks (one open ask per instance), not by
// the id, so the discriminator only has to be unique per open episode; the open
// timestamp is. The transcript signal is kept as row metadata (ContentSig), not
// as identity.
func askItemID(instanceID, disambig string) string {
	sum := sha256.Sum256([]byte(instanceID + "\x00" + disambig))
	return hex.EncodeToString(sum[:16])
}

// deriveAsk classifies an instance's current state as an ask, or reports
// ok == false when it is not a request the human owes a response to.
//
// Scoped to explicit requests and errors. A plain finished-and-waiting turn is
// deliberately NOT an ask: the queue is for things the human is being asked to
// DO, and enqueuing every idle-at-the-prompt session would drown the real
// requests. This is the same judgement that excludes idle from the desktop
// notifier.
//
// The pane is the ground truth for approvals and decisions: they require the
// substate interactive-menu. The hook record is the LATEST event and is often
// stale (a PreToolUse from a turn that already finished), so it only picks
// between decision and approval and supplies the summary.
func deriveAsk(hs *HookStatus, toStatus string, substate Substate) (AskKind, string, bool) {
	st := normalizeStatusString(toStatus)
	var matcher, msg, tool string
	if hs != nil {
		matcher = strings.ToLower(strings.TrimSpace(hs.Matcher))
		msg = strings.TrimSpace(hs.Message)
		tool = strings.TrimSpace(hs.ToolName)
	}
	if st == string(StatusError) {
		return AskError, summaryOr(msg, "hit an error"), true
	}
	// An approval/decision requires the session to be waiting with the menu
	// on screen. A running session with a permission-shaped record has already
	// been answered, and a waiting one without the menu is at its prompt.
	if st != string(StatusWaiting) || substate != SubstateInteractiveMenu {
		return "", "", false
	}
	if matcher == "elicitation_dialog" || strings.EqualFold(tool, "AskUserQuestion") {
		return AskDecision, summaryOr(msg, "is asking a question"), true
	}
	fallback := "wants permission to run a command"
	if tool != "" {
		fallback = "wants to run " + tool
	}
	return AskApproval, summaryOr(msg, fallback), true
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
	start := time.Now()
	if db == nil {
		return
	}
	if d.askCaptures == nil {
		d.askCaptures = map[string]map[string]askCaptureEntry{}
	}

	open, err := db.ListOpenAskItems()
	if err != nil {
		askLog.Debug("ask_list_open_failed", slog.String("error", err.Error()))
		return
	}
	now := time.Now()
	obs := d.observeAskSubstates(profile, byID, statuses, start)
	waiting := func(id string) bool {
		return normalizeStatusString(statuses[id]) == string(StatusWaiting)
	}
	derive := func(id string) (AskKind, string, bool) {
		return deriveAsk(hookStatuses[id], statuses[id], obs[id].sub)
	}

	// Resolve. An open item closes when its session is gone from the live set,
	// or is no longer in an attention status (the ask was answered and the agent
	// moved on). hasOpen tracks which instances still hold an open ask after this
	// pass, so the open phase below does not create a duplicate for them.
	//
	// An open approval or decision also closes when its instance no longer
	// derives that kind: the request changed kind (the open phase then opens
	// the new kind in the same pass), or the menu left the pane, which counts
	// only after askResolveMisses consecutive captures without it. A waiting
	// instance whose substate is unknown this pass keeps its row.
	hasOpen := make(map[string]bool, len(open))
	for _, item := range open {
		id := item.InstanceID
		if _, live := byID[id]; !live || !isAttentionStatus(statuses[id]) {
			d.resolveAsk(db, item.ID, now)
			continue
		}
		if stored := NormalizeAskKind(item.Kind); stored != AskError {
			o := obs[id]
			if waiting(id) && (!o.known || (o.sub != SubstateInteractiveMenu && o.misses < askResolveMisses)) {
				hasOpen[id] = true
				continue
			}
			if kind, _, ok := derive(id); !ok || kind != stored {
				d.resolveAsk(db, item.ID, now)
				continue
			}
		}
		hasOpen[id] = true
	}

	// Open. One ask per instance: skip any that already has one open. A pending
	// ask is re-derived every pass, so this gate — not the transcript signal — is
	// what makes a sustained wait collapse to a single row. A waiting instance
	// whose substate is unknown opens nothing.
	for id, inst := range byID {
		if inst == nil || hasOpen[id] || (waiting(id) && !obs[id].known) {
			continue
		}
		kind, summary, ok := derive(id)
		if !ok {
			continue
		}
		sig := transitionEventOutputHash(inst)
		row := &statedb.AskItemRow{
			ID:         askItemID(id, strconv.FormatInt(now.UnixNano(), 10)),
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

	// Prune resolved rows so the table stays bounded, throttled per profile.
	// Without this, every answered/errored turn leaves a row behind forever.
	if last := d.lastAskPrune[profile]; last.IsZero() || now.Sub(last) >= askPruneInterval {
		d.lastAskPrune[profile] = now
		if err := db.PruneResolvedAskItems(now.Add(-askResolvedRetention)); err != nil {
			askLog.Debug("ask_prune_failed",
				slog.String("profile", profile), slog.String("error", err.Error()))
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
