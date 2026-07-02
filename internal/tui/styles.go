package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("15")).Background(lipgloss.Color("62")).Padding(0, 1)
	statusStyle  = lipgloss.NewStyle().Faint(true)
	errorStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
	subtleStyle  = lipgloss.NewStyle().Faint(true)
	draftStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	headerStyle  = lipgloss.NewStyle().Bold(true)
	addedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	removedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	hunkStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	fileStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
)

// relTime renders a compact relative timestamp for list views.
func relTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

// renderDiff colours a unified diff and returns the content plus the indexes
// (in rendered lines) where hunks start, for hunk navigation.
func renderDiff(fd gitlabx.FileDiff) (string, []int) {
	var (
		b         strings.Builder
		hunkLines []int
		lineNo    int
	)
	write := func(s string) {
		b.WriteString(s)
		b.WriteByte('\n')
		lineNo++
	}

	write(fileStyle.Render(fd.Path()))
	switch {
	case fd.NewFile:
		write(subtleStyle.Render("(new file)"))
	case fd.DeletedFile:
		write(subtleStyle.Render("(deleted)"))
	case fd.RenamedFile:
		write(subtleStyle.Render("(renamed)"))
	}
	if fd.TooLarge {
		write(errorStyle.Render("diff too large — not returned by GitLab"))
		return b.String(), hunkLines
	}

	for line := range strings.SplitSeq(strings.TrimSuffix(fd.Diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			hunkLines = append(hunkLines, lineNo)
			write(hunkStyle.Render(line))
		case strings.HasPrefix(line, "+"):
			write(addedStyle.Render(line))
		case strings.HasPrefix(line, "-"):
			write(removedStyle.Render(line))
		default:
			write(line)
		}
	}
	return b.String(), hunkLines
}

// truncate shortens s to max display cells, appending an ellipsis.
func truncate(s string, max int) string {
	if max <= 0 || lipgloss.Width(s) <= max {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}
