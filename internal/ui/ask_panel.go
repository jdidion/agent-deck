package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/charmbracelet/lipgloss"
)

// The ask panel is a dedicated full-screen overlay listing the open human-ask
// queue across every session in the current profile — "flow wants to run a
// command", "natera is asking which migration", "jdidion hit an error". It is
// the ask-grained companion to the session-grained notification bar. See
// docs/design/2026-08-14-human-ask-queue.md and 2026-10-23-ask-groups.md.
//
// Home owns the store reads, the title resolution, and the key routing (jump,
// dismiss, approve/deny, group actions and their confirmation); this component
// groups the rows Home supplies by kind, tracks the cursor, and renders,
// mirroring GlobalSearch's shape.

var (
	askPanelHeaderStyle = lipgloss.NewStyle().
				Foreground(ColorCyan).
				Bold(true)

	askPanelRowStyle = lipgloss.NewStyle().
				Padding(0, 2)

	askPanelSelectedStyle = lipgloss.NewStyle().
				Padding(0, 2).
				Background(ColorCyan).
				Foreground(ColorBg)

	askPanelSummaryStyle = lipgloss.NewStyle().
				Foreground(ColorComment)

	askPanelAgeStyle = lipgloss.NewStyle().
				Foreground(ColorPurple)

	askPanelEmptyStyle = lipgloss.NewStyle().
				Foreground(ColorComment).
				Italic(true)

	askPanelHintStyle = lipgloss.NewStyle().
				Foreground(ColorComment)

	askPanelConfirmStyle = lipgloss.NewStyle().
				Foreground(ColorYellow).
				Bold(true)

	askPanelGroupStyle = lipgloss.NewStyle().
				Bold(true)

	askPanelGroupSelectedStyle = lipgloss.NewStyle().
					Bold(true).
					Background(ColorCyan).
					Foreground(ColorBg)
)

// askRow pairs a store row with the human-readable session title Home resolves
// for it. Summary and age come straight off the item; the title falls back to
// the InstanceID when the owning session cannot be resolved.
type askRow struct {
	item  *statedb.AskItemRow
	title string
}

// askGroupOrder is the display order of the ask groups. Kinds outside it
// (none today) render last under a generic header so nothing is hidden.
var askGroupOrder = []session.AskKind{session.AskApproval, session.AskDecision, session.AskError}

// askGroupTitle names a group header.
func askGroupTitle(kind session.AskKind) string {
	switch kind {
	case session.AskApproval:
		return "Needs approval"
	case session.AskDecision:
		return "Needs decision"
	case session.AskError:
		return "Error"
	default:
		return "Other"
	}
}

// askLine is one display line: a group header (row == nil) or an item.
type askLine struct {
	kind session.AskKind
	row  *askRow
}

// AskPanel is the open-ask-queue overlay. It is a pure view over rows Home
// supplies via SetRows; it never touches the store itself. Rows are grouped by
// kind under headers (docs/design/2026-10-23-ask-groups.md), and the cursor
// moves over both header and item lines.
type AskPanel struct {
	rows    []askRow
	groups  map[session.AskKind][]askRow
	lines   []askLine
	cursor  int
	offset  int    // first line rendered when the list is taller than the panel
	confirm string // pending group-action prompt; "" when none
	status  string // progress or result of the last approve/deny; "" when none
	width   int
	height  int
	visible bool
}

// NewAskPanel creates a hidden ask panel.
func NewAskPanel() *AskPanel {
	return &AskPanel{
		rows:    []askRow{},
		cursor:  0,
		visible: false,
	}
}

// Show makes the panel visible. Rows are populated separately via SetRows
// (Home refreshes from the store right before showing).
func (ap *AskPanel) Show() {
	ap.visible = true
}

// Hide hides the panel and drops any pending confirmation.
func (ap *AskPanel) Hide() {
	ap.visible = false
	ap.confirm = ""
	ap.status = ""
}

// IsVisible reports whether the panel is visible.
func (ap *AskPanel) IsVisible() bool {
	// Nil-safe like the sibling overlays (e.g. AgentsPanel.IsVisible): the key
	// path calls this on every keystroke, and a Home built without the
	// constructor (test literals) has a nil askPanel. Production always sets it.
	return ap != nil && ap.visible
}

// SetSize sets the overlay dimensions.
func (ap *AskPanel) SetSize(width, height int) {
	ap.width = width
	ap.height = height
}

// SetConfirm shows prompt in place of the key hint; "" clears it.
func (ap *AskPanel) SetConfirm(prompt string) {
	ap.confirm = prompt
}

// SetStatus shows a progress or result line above the key hint; "" clears it.
func (ap *AskPanel) SetStatus(status string) {
	ap.status = status
}

// SetRows replaces the display rows and regroups them. Stored legacy kinds
// read as their current names. The cursor stays on the same item or header
// when it survives the refresh; otherwise it is clamped. On first fill it lands
// on the first item rather than its header.
func (ap *AskPanel) SetRows(rows []askRow) {
	prevKind, prevID, hadLines := ap.cursorKey()

	ap.rows = rows
	ap.groups = make(map[session.AskKind][]askRow)
	var extra []session.AskKind
	for _, r := range rows {
		if r.item == nil {
			continue
		}
		kind := session.NormalizeAskKind(r.item.Kind)
		known := false
		for _, k := range askGroupOrder {
			if k == kind {
				known = true
				break
			}
		}
		if !known {
			kind = ""
			if len(ap.groups[kind]) == 0 {
				extra = append(extra, kind)
			}
		}
		ap.groups[kind] = append(ap.groups[kind], r)
	}
	ap.lines = ap.lines[:0]
	for _, kind := range append(append([]session.AskKind{}, askGroupOrder...), extra...) {
		group := ap.groups[kind]
		if len(group) == 0 {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool {
			return group[i].item.CreatedAt.After(group[j].item.CreatedAt)
		})
		ap.lines = append(ap.lines, askLine{kind: kind})
		for i := range group {
			ap.lines = append(ap.lines, askLine{kind: kind, row: &group[i]})
		}
	}

	if !hadLines {
		ap.cursor = 0
		if len(ap.lines) > 1 {
			ap.cursor = 1
		}
		return
	}
	for i, l := range ap.lines {
		if l.kind != prevKind {
			continue
		}
		if (prevID == "" && l.row == nil) || (prevID != "" && l.row != nil && l.row.item.ID == prevID) {
			ap.cursor = i
			return
		}
	}
	ap.clampCursor()
}

// cursorKey identifies the line under the cursor: its group, and the item id
// ("" on a header). ok is false when there are no lines.
func (ap *AskPanel) cursorKey() (session.AskKind, string, bool) {
	if len(ap.lines) == 0 {
		return "", "", false
	}
	ap.clampCursor()
	l := ap.lines[ap.cursor]
	if l.row == nil {
		return l.kind, "", true
	}
	return l.kind, l.row.item.ID, true
}

// clampCursor keeps the cursor within [0, len-1], or 0 when empty.
func (ap *AskPanel) clampCursor() {
	if len(ap.lines) == 0 {
		ap.cursor = 0
		return
	}
	if ap.cursor < 0 {
		ap.cursor = 0
	}
	if ap.cursor >= len(ap.lines) {
		ap.cursor = len(ap.lines) - 1
	}
}

// MoveUp moves the cursor up one line (headers included).
func (ap *AskPanel) MoveUp() {
	if ap.cursor > 0 {
		ap.cursor--
	}
}

// MoveDown moves the cursor down one line (headers included).
func (ap *AskPanel) MoveDown() {
	if ap.cursor < len(ap.lines)-1 {
		ap.cursor++
	}
}

// Selected returns the store row under the cursor, or nil when the queue is
// empty or the cursor is on a group header.
func (ap *AskPanel) Selected() *statedb.AskItemRow {
	if len(ap.lines) == 0 {
		return nil
	}
	ap.clampCursor()
	if r := ap.lines[ap.cursor].row; r != nil {
		return r.item
	}
	return nil
}

// OnHeader reports whether the cursor is on a group header.
func (ap *AskPanel) OnHeader() bool {
	if len(ap.lines) == 0 {
		return false
	}
	ap.clampCursor()
	return ap.lines[ap.cursor].row == nil
}

// CursorGroup returns the kind of the group the cursor is in, or "" when the
// queue is empty.
func (ap *AskPanel) CursorGroup() session.AskKind {
	if len(ap.lines) == 0 {
		return ""
	}
	ap.clampCursor()
	return ap.lines[ap.cursor].kind
}

// GroupItems returns the items in group kind, newest first.
func (ap *AskPanel) GroupItems(kind session.AskKind) []*statedb.AskItemRow {
	group := ap.groups[kind]
	out := make([]*statedb.AskItemRow, 0, len(group))
	for _, r := range group {
		out = append(out, r.item)
	}
	return out
}

// askKindGlyph maps an ask kind to a glyph and color, in the spirit of the
// session statusIcon/statusColor palette in internal/session/notifications.go.
func askKindGlyph(kind string) (string, lipgloss.Color) {
	switch session.NormalizeAskKind(kind) {
	case session.AskApproval:
		return "?", ColorYellow
	case session.AskDecision:
		return "✎", ColorMagenta
	case session.AskError:
		return "✕", ColorRed
	default:
		return "•", ColorComment
	}
}

// askAge renders a compact age for the ask, reusing humanizeSince (the TUI's
// single source of truth for relative time) but dropping its " ago" suffix so
// the column reads "3m" / "1h" rather than "3m ago".
func askAge(created time.Time) string {
	if created.IsZero() {
		return ""
	}
	return strings.TrimSuffix(humanizeSince(time.Since(created)), " ago")
}

// View renders the overlay. Returns "" when hidden.
func (ap *AskPanel) View() string {
	if !ap.visible {
		return ""
	}

	// Overlay width, clamped like GlobalSearch so it stays readable on very
	// wide or very narrow terminals.
	totalWidth := ap.width - 4
	if totalWidth > 120 {
		totalWidth = 120
	}
	if totalWidth < 40 {
		totalWidth = 40
	}
	// Inner width available for row text (inside the border + padding).
	innerWidth := totalWidth - 4
	if innerWidth < 20 {
		innerWidth = 20
	}

	var b strings.Builder

	header := askPanelHeaderStyle.Render(fmt.Sprintf("⚡ Asks (%d)", len(ap.lines)-len(ap.groups)))
	b.WriteString(header + "\n\n")

	footer := askPanelHintStyle.Render(ap.hint())
	if ap.confirm != "" {
		footer = askPanelConfirmStyle.Render(ap.confirm)
	} else if ap.status != "" {
		footer = askPanelConfirmStyle.Render(ap.status) + "\n" + footer
	}

	if len(ap.lines) == 0 {
		b.WriteString(askPanelEmptyStyle.Render("No open requests — you're caught up."))
		b.WriteString("\n")
	} else {
		start, end := ap.visibleRange(totalWidth-2, footer)
		for i := start; i < end; i++ {
			l := ap.lines[i]
			if l.row == nil {
				b.WriteString(ap.renderGroupHeader(i, l.kind) + "\n")
				continue
			}
			b.WriteString(ap.renderRow(i, *l.row, innerWidth) + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(footer)

	boxStyle := lipgloss.NewStyle().
		Width(totalWidth).
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(0, 1)

	return centerInScreen(boxStyle.Render(b.String()), ap.width, ap.height)
}

// visibleRange returns the [start, end) slice of lines that fits the panel
// height around the header, the blank lines and the footer (wrapped at
// contentWidth), scrolling ap.offset so the cursor stays in view. A panel with
// no height set shows every line.
func (ap *AskPanel) visibleRange(contentWidth int, footer string) (int, int) {
	n := len(ap.lines)
	if ap.height <= 0 {
		return 0, n
	}
	footerLines := lipgloss.Height(lipgloss.NewStyle().Width(contentWidth).Render(footer))
	// Border (2), title and its blank line (2), blank line above the footer (1).
	avail := ap.height - 5 - footerLines
	if avail < 1 {
		avail = 1
	}
	if n <= avail {
		ap.offset = 0
		return 0, n
	}
	if ap.cursor < ap.offset {
		ap.offset = ap.cursor
	}
	if ap.cursor >= ap.offset+avail {
		ap.offset = ap.cursor - avail + 1
	}
	if ap.offset > n-avail {
		ap.offset = n - avail
	}
	if ap.offset < 0 {
		ap.offset = 0
	}
	return ap.offset, ap.offset + avail
}

// renderGroupHeader renders a group header line with its count.
func (ap *AskPanel) renderGroupHeader(index int, kind session.AskKind) string {
	glyph, color := askKindGlyph(string(kind))
	label := fmt.Sprintf("%s (%d)", askGroupTitle(kind), len(ap.groups[kind]))
	if index == ap.cursor {
		return askPanelGroupSelectedStyle.Render("› " + glyph + " " + label)
	}
	return askPanelGroupStyle.Render(lipgloss.NewStyle().Foreground(color).Render(glyph) + " " + label)
}

// hint lists the keys that apply to the line under the cursor.
func (ap *AskPanel) hint() string {
	if len(ap.lines) == 0 {
		return "esc close"
	}
	kind := ap.CursorGroup()
	var keys []string
	if ap.OnHeader() {
		if kind == session.AskApproval {
			keys = append(keys, "y approve all", "n deny all")
		}
		if kind == session.AskError {
			keys = append(keys, "R restart all")
		}
		keys = append(keys, "r dismiss all")
	} else {
		keys = append(keys, "enter jump")
		if kind == session.AskApproval {
			keys = append(keys, "y approve", "n deny", "Y/N approve/deny all")
		}
		if kind == session.AskError {
			keys = append(keys, "R restart all")
		}
		keys = append(keys, "r dismiss", "D dismiss all")
	}
	keys = append(keys, "esc close")
	return "↑/↓ move · " + strings.Join(keys, " · ")
}

// renderRow formats one queue row as "<glyph>  <title>   <summary>   <age>",
// highlighting the selected row. Title and summary are budgeted against the
// available width so the age column stays visible.
func (ap *AskPanel) renderRow(index int, row askRow, innerWidth int) string {
	glyph, glyphColor := askKindGlyph(row.item.Kind)
	age := askAge(row.item.CreatedAt)

	title := row.title
	if title == "" {
		title = row.item.InstanceID
	}
	summary := strings.TrimSpace(strings.ReplaceAll(row.item.Summary, "\n", " "))

	// Budget: glyph (1) + gaps + age. Give the title up to ~40% of the
	// remaining space and hand the rest to the summary.
	budget := innerWidth - lipgloss.Width(glyph) - lipgloss.Width(age) - 6
	if budget < 10 {
		budget = 10
	}
	titleBudget := budget * 2 / 5
	if titleBudget < 8 {
		titleBudget = 8
	}
	summaryBudget := budget - titleBudget
	if summaryBudget < 0 {
		summaryBudget = 0
	}

	title = truncateCol(title, titleBudget)
	summary = truncateCol(summary, summaryBudget)

	if index == ap.cursor {
		// Selected: render the whole line in the selection style so it reads as
		// one highlighted band (glyph color is dropped for contrast).
		line := fmt.Sprintf("› %s  %s", glyph, title)
		if summary != "" {
			line += "  " + summary
		}
		if age != "" {
			line += "  " + age
		}
		return askPanelSelectedStyle.Render(line)
	}

	glyphStr := lipgloss.NewStyle().Foreground(glyphColor).Render(glyph)
	line := fmt.Sprintf("%s  %s", glyphStr, title)
	if summary != "" {
		line += "  " + askPanelSummaryStyle.Render(summary)
	}
	if age != "" {
		line += "  " + askPanelAgeStyle.Render(age)
	}
	return askPanelRowStyle.Render(line)
}

// truncateCol clamps s to at most width display columns, appending an ellipsis
// when it has to cut. width <= 0 yields "".
func truncateCol(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width <= 1 {
		return "…"
	}
	// Trim rune-wise until it fits with room for the ellipsis.
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}
