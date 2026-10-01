package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/charmbracelet/lipgloss"
)

// The ask panel is a dedicated full-screen overlay listing the open human-ask
// queue across every session in the current profile — "flow wants to run a
// command", "natera is asking which migration", "jdidion hit an error". It is
// the ask-grained companion to the session-grained notification bar. See
// docs/design/2026-08-14-human-ask-queue.md.
//
// v1 is read-and-jump: Enter selects the owning session so the human answers in
// the pane; there is no answer-in-place. Home owns the store reads, the title
// resolution, and the key routing (jump / dismiss); this component only holds
// display rows and renders them, mirroring GlobalSearch's shape.

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
)

// askRow pairs a store row with the human-readable session title Home resolves
// for it. Summary and age come straight off the item; the title falls back to
// the InstanceID when the owning session cannot be resolved.
type askRow struct {
	item  *statedb.AskItemRow
	title string
}

// AskPanel is the open-ask-queue overlay. It is a pure view over rows Home
// supplies via SetRows; it never touches the store itself.
type AskPanel struct {
	rows    []askRow
	cursor  int
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

// Hide hides the panel.
func (ap *AskPanel) Hide() {
	ap.visible = false
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

// SetRows replaces the display rows, clamping the cursor so it always points at
// a valid row (or 0 when empty). Home calls this on open and on every refresh.
func (ap *AskPanel) SetRows(rows []askRow) {
	ap.rows = rows
	ap.clampCursor()
}

// clampCursor keeps the cursor within [0, len-1], or 0 when empty.
func (ap *AskPanel) clampCursor() {
	if len(ap.rows) == 0 {
		ap.cursor = 0
		return
	}
	if ap.cursor < 0 {
		ap.cursor = 0
	}
	if ap.cursor >= len(ap.rows) {
		ap.cursor = len(ap.rows) - 1
	}
}

// MoveUp moves the cursor toward the newest (top) row.
func (ap *AskPanel) MoveUp() {
	if ap.cursor > 0 {
		ap.cursor--
	}
}

// MoveDown moves the cursor toward the oldest (bottom) row.
func (ap *AskPanel) MoveDown() {
	if ap.cursor < len(ap.rows)-1 {
		ap.cursor++
	}
}

// Selected returns the store row under the cursor, or nil when the queue is
// empty.
func (ap *AskPanel) Selected() *statedb.AskItemRow {
	if len(ap.rows) == 0 {
		return nil
	}
	ap.clampCursor()
	return ap.rows[ap.cursor].item
}

// askKindGlyph maps an ask kind to a glyph and color, in the spirit of the
// session statusIcon/statusColor palette in internal/session/notifications.go.
func askKindGlyph(kind string) (string, lipgloss.Color) {
	switch kind {
	case "permission":
		return "◆", ColorYellow
	case "question":
		return "?", ColorCyan
	case "error":
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

	header := askPanelHeaderStyle.Render(fmt.Sprintf("⚡ Asks (%d)", len(ap.rows)))
	b.WriteString(header + "\n\n")

	if len(ap.rows) == 0 {
		b.WriteString(askPanelEmptyStyle.Render("No open requests — you're caught up."))
		b.WriteString("\n")
	} else {
		for i, row := range ap.rows {
			b.WriteString(ap.renderRow(i, row, innerWidth) + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(askPanelHintStyle.Render(
		"↑/↓ move · enter jump to session · r dismiss · esc close"))

	boxStyle := lipgloss.NewStyle().
		Width(totalWidth).
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(0, 1)

	return centerInScreen(boxStyle.Render(b.String()), ap.width, ap.height)
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
