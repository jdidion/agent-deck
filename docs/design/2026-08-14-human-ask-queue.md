# Human ask queue

Status: draft / in progress on `feat/ask-queue`.

## Problem

Everything agent-deck surfaces about attention is session-grained. The tmux
notification bar lists waiting *sessions*, the desktop notifier (PR #1893)
alerts per *session*, the status column is per *session*. But the unit a human
actually owes a response to is the *ask*: "flow wants to run a command",
"natera is asking which migration to apply", "jdidion hit an error". One
session can produce several asks over its life, and the human has no single
place that lists the open requests across all sessions. They hunt through
sessions to find what is blocked on them.

This adds an ask queue: a cross-session list of open requests an agent has made
of the human, that the human works down and clears.

## Non-goals (v1)

- Answering from the queue. v1 is read-and-jump: Enter selects the session so
  the human answers in the pane. Answer-in-place via `session send` is a
  deliberate fast-follow, because it inherits all of `session send`'s
  delivery-confirmation complexity (#1793/#1831) and should not gate the store.
- Exact command extraction. v1 summarizes from the hook message and the
  transcript tail; parsing the precise `tool_use` input is later.
- Non-Claude parity beyond what each tool's hook already reports. Claude is the
  first-class path; codex/gemini/cursor/hermes populate what their hooks carry.

## Why this is mostly recombination

The detection, taxonomy, dedup, and resolution signals already exist:

- **Kind** is derivable from data the hook handler already parses.
  `cmd/agent-deck/hook_handler.go` maps events to status and, for
  `notification`, already reads the `matcher` to tell `permission_prompt` from
  `elicitation_dialog` (`:207-213`) — then collapses both to `"waiting"` and
  discards the distinction. Event + matcher gives permission / question / done.
- **Content** is reachable without pane-scraping. Claude's `Notification` hook
  carries a `message`, which `hookPayload` currently does not parse. And the
  Stop-edge transcript reader (#1186) already opens the transcript JSONL tail
  through `session.ValidateTranscriptPath`, a fail-closed containment guard, so
  the assistant's last message is reachable by an already-reviewed path.
- **Lifecycle is STATUS-driven, one open ask per instance.** An earlier design
  keyed both identity and resolution on `transitionEventOutputHash` →
  `transitionContentSignal` (the transcript *size*), on the assumption that a
  waiting session appends nothing so the signal is stable. That assumption is
  false in practice: a long-lived managed agent keeps appending to its JSONL
  even while it sits at a permission prompt, so the size changes every poll. The
  effect was catastrophic churn — one unanswered prompt produced hundreds of
  create-then-resolve rows (measured: 469 for a single instance), and the
  open-only panel almost never caught one. The lifecycle now ignores transcript
  size: an ask opens when an instance enters an attention status with no ask
  already open, and resolves only when it LEAVES that status (or the instance
  disappears). `ContentSig` is retained as metadata but no longer gates the
  lifecycle.
- **The producer edge** is the poll loop in `internal/session/transition_daemon.go`
  that already computes every `running → waiting/error` transition and the
  content signal, at two sites: the snapshot loop (`:442`) and
  `emitHookTransitionCandidates` (`:790`, for turns too fast for a running
  snapshot).

## Data model

```go
// internal/session/ask_queue.go
type AskKind string

const (
    AskPermission AskKind = "permission" // permissionrequest | notification+permission_prompt
    AskQuestion   AskKind = "question"   // notification+elicitation_dialog
    AskError      AskKind = "error"      // status == error
    AskReview     AskKind = "review"     // stop with a completion sentinel (DoneStatus set)
)

type AskItem struct {
    ID         string    // hash of (InstanceID, ContentSig); stable identity
    InstanceID string
    Profile    string
    Kind       AskKind
    Summary    string    // hook message or transcript tail; may be ""
    CreatedAt  time.Time
    ResolvedAt time.Time // zero == open
    ContentSig string    // transitionEventOutputHash(inst) at open
}
```

**Identity** is `(InstanceID, ContentSig)`, hashed into `ID`. Because a pending
ask does not grow the transcript, re-observing it across polls yields the same
`ContentSig` → same `ID` → idempotent open. A genuine new turn changes the
signal → new `ID` → new item.

## Store (`internal/statedb`)

A dumb table; all policy lives in the producer. Mirrors the existing
`instances` table patterns (`withBusyRetry`, unix-nano timestamps, the
upsert-without-clobber discipline).

```sql
CREATE TABLE IF NOT EXISTS ask_items (
    id          TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL,
    profile     TEXT NOT NULL DEFAULT '',
    kind        TEXT NOT NULL DEFAULT '',
    summary     TEXT NOT NULL DEFAULT '',
    content_sig TEXT NOT NULL DEFAULT '',
    event       TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL DEFAULT 0, -- unix nano
    resolved_at INTEGER NOT NULL DEFAULT 0  -- 0 == open
);
CREATE INDEX IF NOT EXISTS idx_ask_open ON ask_items(resolved_at);
CREATE INDEX IF NOT EXISTS idx_ask_instance ON ask_items(instance_id);
```

API:

- `UpsertAskItem(*AskItemRow) error` — `INSERT ... ON CONFLICT(id) DO NOTHING`.
  Opening an item already present is a no-op; never clobbers `created_at`.
- `ListOpenAskItems() ([]*AskItemRow, error)` — `resolved_at == 0`, newest first.
- `ListAskItems(includeResolved bool, limit int) ([]*AskItemRow, error)` — for
  the view.
- `ResolveAskItem(id string, at time.Time) error` — `UPDATE resolved_at WHERE
  id=? AND resolved_at=0`.
- `PruneResolvedAskItems(before time.Time) error` — bound table growth.

The store never decides *what* to resolve; the producer passes explicit ids.

## Producer (`internal/session/transition_daemon.go`)

The daemon is the producer, because it already runs the poll loop and is a
separate process from the TUI, so a shared `state.db` table is the only place
both can meet (the same reason the desktop notifier lives here). The primary
instance owns the write, via the existing claim/primary election.

Two additions to the poll loop, alongside the existing transition handling:

**Resolve first, then open**, once per pass, level-triggered:

**Resolve.** Walk open items; resolve any whose instance is gone from the live
set, or whose live status is no longer an attention status (`waiting`/`error`).
Leaving the attention status is what "the human answered / the agent moved on"
looks like, independent of the transcript.

**Open.** For each live instance in an attention status that `deriveAsk`
classifies AND that has no ask still open after the resolve pass, `UpsertAskItem`
one row. Gating on "no open ask for this instance" is what keeps a sustained
wait to a single row no matter how much the transcript grows underneath it.

A genuine second prompt after the human answers still re-alerts: answering moves
the status out of `waiting`, which resolves the first ask, so the next `waiting`
finds no open ask and opens a fresh one. The one edge case the old
content-signal design handled that this does not: a `waiting → idle → waiting`
turn too fast for the daemon to ever sample the intermediate non-attention
status collapses to a single open ask spanning both prompts. That is acceptable
— it still points the human at a session that needs them — and far better than
the churn the signal-based resolution produced.

Best-effort, like the desktop notifier: a store error is logged, never fatal,
and never stalls the poll loop. The store call is a local SQLite write behind
`withBusyRetry`, not a subprocess, so it needs no goroutine offload.

## Content retention (`cmd/agent-deck/hook_handler.go`, `hook_watcher.go`)

Thread the ask content that is already on the wire but currently dropped:

- Parse `message` from the Notification payload into `hookPayload`.
- Persist `matcher` (string) and `message` into the status-file schema and
  `HookStatus`, so the producer reads a kind and a summary without re-opening
  anything.
- The producer prefers the transcript tail (richer, the agent's actual words)
  via `ValidateTranscriptPath` when a `TranscriptPath` is present, and falls
  back to the hook `message`, then to a generic per-kind string.

## View (`internal/ui`)

A new full-height panel toggled by a key (candidate: `!` or a dedicated
binding), listing open items across sessions, newest first, grouped or tagged
by kind. Each row: session title, kind glyph, summary, age. Enter selects the
owning session (reuse the existing jump-to-session path). `r` marks an item
resolved manually. The panel reads `ListOpenAskItems` on the same refresh tick
the session list uses.

The bar in `notifications.go` stays as-is; this is the durable, ask-grained
companion to that live, session-grained strip.

## Phasing

1. **Store** — `ask_items` table + `AskItemRow` + CRUD, isolated and tested
   against a temp db. (background agent)
2. **Content retention** — `matcher`/`message` through payload → status file →
   `HookStatus`, with kind derivation. Isolated to the hook files. (background
   agent)
3. **Producer** — open/resolve in the daemon loop, consuming (1) and (2).
   Integration; owns the lifecycle tests including the fast-turn resolve.
4. **View** — the TUI panel over (1).
5. **Fast-follow** — answer-in-place via `session send`.

Phases 1 and 2 touch disjoint files and run in parallel. 3 depends on both. 4
depends on 1.

## Testing

- Store: CRUD + idempotent upsert + resolve-only-when-open + prune, temp db.
- Retention: permission/elicitation/plain-notification payloads retain the
  right matcher+message; plain informational notification unchanged.
- Producer: idempotent open across repeated polls (no duplicate item for a
  sustained wait); resolve on signal-advance including the `waiting → idle →
  waiting` fast turn; per-instance and per-profile isolation. Mutation-verify
  the idempotency and the resolve, mirroring the desktop-notifier tests.
- Sandboxed suite per the intake contract.

## Security

- No new content path: the transcript read reuses `ValidateTranscriptPath`, the
  same fail-closed guard the cost/sentinel reader uses.
- Stored summaries are session titles / transcript text and may carry
  agent-generated content; they are rendered in the TUI and written to
  `state.db`, never executed. Same disclosure as the desktop notifier's title
  handling.
- The store is a bounded table with a prune; a runaway session cannot grow it
  without limit.

## Relationship to kata / the north-star

The fleet north-star puts the queue substrate in kata for agent→agent routing.
This queue is agent→human and TUI-native: it is about the panes agent-deck owns
and the human sitting at them, which kata does not model. The detection and the
eventual answer-in-place path are inherently agent-deck's. If a shared store
ever matters, the `state.db` table can export; the producer and view stay here.

## Open questions

- Key binding for the panel, and whether it is a panel or a status filter mode
  on the existing list.
- Whether `AskReview` (a finished turn wanting review) belongs in the same queue
  as blocking asks, or is visually separated — it is not blocking in the same
  way a permission prompt is.
- Coverage matrix for non-Claude tools; what each hook actually carries.
