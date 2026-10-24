# Ask groups, attention states, and preview refresh

Status: accepted 2026-10-23. Extends `2026-08-14-human-ask-queue.md`.

## Problem

1. Sessions blocked on a tool approval never reach the ask queue. Current
   Claude Code sends no `PermissionRequest` or `Notification` hook before a
   permission prompt; the last hook event is `PreToolUse`, and agent-deck
   reports `waiting` from the pane. `deriveAsk` only recognises
   `permissionrequest` / `permission_prompt` / `elicitation_dialog`, so these
   sessions are skipped. Observed: 15 waiting sessions, all with last event
   `PreToolUse`, zero open approval asks.
2. The Asks panel is a flat list. The operator wants it grouped by what the
   request needs, with actions that apply to a whole group.
3. The session list shows one icon (`◐`) for every waiting state, so "blocked
   on approval", "asking a question" and "finished, your turn" look the same.
4. After detaching from a session back to the list, the preview keeps showing
   stale output until the next background refresh.

## Kinds

Revised 2026-10-23 after a live check and review: "waiting with last hook
`PreToolUse`" is mostly a finished turn whose last hook record is stale (19 of
21 such sessions were at their normal prompt; only 2 showed a permission
dialog). Treating it as an approval made approve-all type `1` + Enter into 39
idle prompts. The pane is the ground truth, so an approval or decision now
requires the pane to show an open menu: the session's substate is
`interactive-menu` (`tmux.SubstateInteractiveMenu`, set from
`hasOpenInteractiveMenu` on the pane capture the status pass already takes).

Every attention state classifies into exactly one kind, from the live status,
the cached substate, and the latest hook record (event, matcher, message, tool
name):

| Kind | Condition | Group |
|---|---|---|
| `decision` | status `waiting`, substate `interactive-menu`, and the hook record says a question: matcher `elicitation_dialog`, or tool `AskUserQuestion` (any event) | Needs decision |
| `approval` | status `waiting`, substate `interactive-menu`, and not a decision | Needs approval |
| `error` | status `error` | Error |
| (not an ask) | `waiting` without substate `interactive-menu` (your turn), and every other status | none |

Hook events alone never make an approval or decision, because the hook record
can be stale. Comparisons are case-insensitive. `ExitPlanMode` (plan approval)
is an `approval`. Summary: the hook message when present, else `wants to run
<ToolName>` when a tool is known, else `wants permission to run a command`
(approval) or `is asking a question` (decision).

## Dispatching keys

Approve (`1` + Enter) and deny (Escape) are sent only to a session whose pane
shows a permission prompt at the moment of sending: dispatch takes a fresh
pane capture and requires `tmux.PaneShowsPermissionPrompt` to hold for it (see
"Second review round"). A session that fails the check is
skipped and counted, never sent to. Dispatch runs off the Bubble Tea event loop
(a `tea.Cmd`), sessions in parallel, and reports one result message:
`Approved 2, skipped 19 (no prompt on screen), failed 0` (or `Denied ...`).
Failures (send error, no tmux session) are counted and named, not swallowed.
While a group dispatch is in flight, further approve/deny keys are ignored, so
repeated presses do not send extra rounds. The per-item `y`/`n` follow the same
check. The confirmation prompt counts the rows the panel shows (the cached
substate can lag the pane, since hook-driven statuses skip the capture; the
per-send check is the gate). While a dispatch runs, the panel shows
`Approving N sessions…`, then the result line; with nothing to act on it says
so instead of doing nothing silently.

The existing `permission` kind is renamed `approval` and `question` is renamed
`decision`. Rows already stored with the old names are read as the new names.

## Hook content

The hook handler retains `tool_name` from the payload next to `matcher` and
`message`, in the hook status file (`tool_name`, omitempty) and in
`HookStatus.ToolName`, on every read path (watcher and the direct
`readHookStatusFile` fallback). Summary for an `approval` from `PreToolUse`:
`wants to run <ToolName>` when the message is empty.

## Asks panel

- Rows are grouped under three headers, in this order: **Needs approval**,
  **Needs decision**, **Error**. Each header shows its count. Empty groups are
  omitted. Within a group, newest first.
- The cursor moves over item rows and header rows. On a header the per-item
  keys act on the whole group.
- Item keys: `enter` jump to the session; `r` dismiss; on an approval item `y`
  approve (sends `1` + Enter, the same as quick-approve) and `n` deny (sends
  Escape).
- Group keys (cursor on the header, or `Y`/`N`/`R`/`D` anywhere for the group
  the cursor is in): `Y` approve all (approval group), `N` deny all (approval
  group), `R` restart all (error group), `D` dismiss all (any group).
- Every group action first shows a confirmation naming the action and count,
  e.g. `Approve 15 pending tool calls? (y/n)`. Only `y` proceeds; any other key
  cancels. Per-item actions do not confirm.
- After an action the panel refreshes from the store. An approved, denied or
  restarted item resolves on the producer's next pass when its session leaves
  the attention status; dismissed items resolve immediately, as today.
- Footer hint lists the keys that apply to the cursor's row.

## Session row icons

| State | Glyph | Color |
|---|---|---|
| running | `●` | green |
| needs approval | `?` | yellow |
| needs decision | `✎` | magenta |
| waiting, your turn (not an ask) | `◐` | dim |
| idle | `○` | dim |
| error | `✕` | red |
| stopped | `■` | dim |

The row uses the same classification as the ask queue, computed from the
session's status and cached hook record at render time (no new I/O on the
render path). Header counts (`● 13 ◐ 6`) gain separate counts for `?` and `✎`
when non-zero. Help lists the glyphs.

## Preview refresh after detach

When the user detaches from a session back to the list, the preview for the
selected session is refetched immediately (its cached capture invalidated and a
fetch issued on the attach-return path), so the first frame after return shows
current output rather than the pre-attach capture.

## Daemon autostart

Revised: `[notifications] autostart_daemon` defaults to **true** again (nil =
true). Defaulting it off silently disabled notifications and the ask producer
for every user without launchd/systemd. The leak it was meant to stop (a TUI
under a throwaway HOME, as in upstream's TUI tests, leaving a detached daemon
behind) is fixed at the source: the notify daemon exits cleanly on its next
poll once the HOME it started with no longer exists.

## Other fixes from review

- Clearing a session's hook evidence (`ClearHookStatus`, the Codex reject
  path, the Hermes spawn seed) also clears the cached matcher and tool name, so
  a stale record cannot keep a row on `?`.
- The Asks panel scrolls: it renders only the rows that fit its height, keeps
  the cursor in view, and always shows the footer and any confirmation prompt.
- Attach-return preview refresh keeps the old capture on screen until the new
  one lands (it expires the cache time instead of deleting the entry), and a
  fetch result is applied only if it belongs to the latest fetch for that
  session, so an in-flight pre-attach capture cannot overwrite the fresh one.
- The filter bar does not count archived sessions as `?` or `✎`.

## Second review round (2026-10-24)

- **Approve answers only a permission prompt.** The dispatch gate requires the
  pane to show a permission dialog (the numbered Yes/No "Do you want to
  proceed?" shape, including plan approval), not any open menu: an
  AskUserQuestion picker or other selection list is skipped, so `1` never picks
  a question's first option. The check uses the same frame preparation as the
  status classifier (`prepareFrame`), so Claude's agent roster below the footer
  cannot push the dialog out of the inspected tail.
- **Capture errors are failures.** A pane capture that errors or times out at
  dispatch counts as failed and names the session; skipped means the capture
  succeeded and showed no permission prompt.
- **Daemon captures are bounded.** The producer captures only waiting
  instances, at most once per instance every 5 s (reusing the last result in
  between), each capture under the per-probe budget, and stops capturing for
  the rest of the pass once the pass budget is spent.
- **Unknown is not "menu gone".** An instance whose capture errored, timed out
  or was skipped for budget has an unknown substate: its open row is kept and
  no row is opened for it. An open approval or decision resolves on a missing
  menu only after two consecutive successful captures without one.
- **HOME identity.** The daemon records HOME's identity (`os.Stat` at start)
  and exits when HOME is missing or is no longer the same directory
  (`os.SameFile`), checked before the first pass and after every pass, so a
  pass that recreates HOME cannot keep it alive.
- **Panel status.** Showing the panel clears any old status line, and each
  send has a timeout, so a dispatch result always arrives and the in-flight
  guard always clears.
- **Counts.** Archived sessions count in none of the `●`/`?`/`✎`/`◐` pills'
  ask arithmetic: an archived waiting session is not "your turn" either.
- **Remote preview.** Remote preview fetches carry a generation too, so an
  in-flight pre-attach remote capture cannot overwrite the post-attach one.
