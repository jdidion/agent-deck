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

Every attention state classifies into exactly one kind. Classification uses the
live status plus the latest hook record (event, matcher, message, and the new
tool name).

| Kind | Condition | Group |
|---|---|---|
| `approval` | status `waiting` and (event `PermissionRequest`, or matcher `permission_prompt`, or event `PreToolUse` with a tool other than `AskUserQuestion`) | Needs approval |
| `decision` | status `waiting` and (matcher `elicitation_dialog`, or event `PreToolUse` with tool `AskUserQuestion`) | Needs decision |
| `error` | status `error` | Error |
| (not an ask) | any other `waiting` (e.g. after `Stop`), and every other status | none |

Event and matcher comparisons are case-insensitive, as today. `ExitPlanMode`
(plan approval) is an `approval`. A `PreToolUse` record while the status is
`running` is not an ask (the tool is executing).

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

`[notifications] autostart_daemon` defaults to false. A TUI launched under a
throwaway HOME (upstream's TUI tests) leaked a detached notify daemon per run.
Users who want the TUI to start the daemon set it to true; the recommended
setup is launchd/systemd.
