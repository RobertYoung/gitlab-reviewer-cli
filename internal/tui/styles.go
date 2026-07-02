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

// renderDiff renders a file diff — syntax-highlighted code with existing
// discussion threads anchored inline, unified or side-by-side — and returns
// the content plus the indexes (in rendered lines) where hunks start.
func renderDiff(fd gitlabx.FileDiff, discussions []gitlabx.Discussion, width int, split bool) (string, []int) {
	if split {
		return renderSplitDiff(fd, discussions, width)
	}
	return renderUnifiedDiff(fd, discussions, width)
}

// writeDiffHeader emits the file name and status lines shared by both
// layouts; it returns false when GitLab returned no diff body.
func writeDiffHeader(write func(string), fd gitlabx.FileDiff) bool {
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
		return false
	}
	return true
}

func renderUnifiedDiff(fd gitlabx.FileDiff, discussions []gitlabx.Discussion, width int) (string, []int) {
	var (
		b         strings.Builder
		hunkLines []int
		lineNo    int
	)
	write := func(s string) {
		b.WriteString(s)
		b.WriteByte('\n')
		// Discussion blocks span several rendered lines; hunk offsets must
		// track what the viewport actually shows.
		lineNo += strings.Count(s, "\n") + 1
	}

	if !writeDiffHeader(write, fd) {
		return b.String(), hunkLines
	}

	hl := newHighlighter(fd.NewPath)
	oldLine, newLine := 0, 0
	writeThreads := func(useNew bool) {
		for _, block := range discussionBlocks(discussions, fd, oldLine, newLine, useNew, width) {
			write(block)
		}
	}

	for line := range strings.SplitSeq(strings.TrimSuffix(fd.Diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			hunkLines = append(hunkLines, lineNo)
			if o, n, ok := parseHunkStart(line); ok {
				oldLine, newLine = o, n
			}
			write(hunkStyle.Render(line))
		case strings.HasPrefix(line, "+"):
			write(addedStyle.Render("+") + hl.line(line[1:]))
			writeThreads(true)
			newLine++
		case strings.HasPrefix(line, "-"):
			write(removedStyle.Render("-") + hl.line(line[1:]))
			writeThreads(false)
			oldLine++
		case strings.HasPrefix(line, `\`):
			write(subtleStyle.Render(line))
		default:
			code := line
			if len(code) > 0 {
				code = code[1:]
			}
			write(" " + hl.line(code))
			writeThreads(true)
			oldLine++
			newLine++
		}
	}
	return b.String(), hunkLines
}

// splitSide is one half of a side-by-side row; kind 0 means the side is
// blank (an unpaired addition or removal).
type splitSide struct {
	no   int
	text string
	kind byte // '+', '-', ' ', or 0
}

// renderSplitDiff renders the diff side-by-side: old lines left, new lines
// right, removals paired with the additions that replaced them.
func renderSplitDiff(fd gitlabx.FileDiff, discussions []gitlabx.Discussion, width int) (string, []int) {
	var (
		b         strings.Builder
		hunkLines []int
		lineNo    int
	)
	write := func(s string) {
		b.WriteString(s)
		b.WriteByte('\n')
		lineNo += strings.Count(s, "\n") + 1
	}

	if !writeDiffHeader(write, fd) {
		return b.String(), hunkLines
	}

	hl := newHighlighter(fd.NewPath)
	colWidth := max((width-1)/2, 24)
	// "1234 " gutter + one marker cell in front of the code.
	codeWidth := colWidth - 6

	column := func(s splitSide) string {
		if s.kind == 0 {
			return strings.Repeat(" ", colWidth)
		}
		marker := " "
		switch s.kind {
		case '+':
			marker = addedStyle.Render("+")
		case '-':
			marker = removedStyle.Render("-")
		}
		cell := subtleStyle.Render(fmt.Sprintf("%4d ", s.no)) + marker + hl.line(truncate(s.text, codeWidth))
		if pad := colWidth - lipgloss.Width(cell); pad > 0 {
			cell += strings.Repeat(" ", pad)
		}
		return cell
	}

	emit := func(left, right splitSide) {
		write(column(left) + subtleStyle.Render("│") + column(right))
		if left.kind == '-' {
			for _, block := range discussionBlocks(discussions, fd, left.no, 0, false, width) {
				write(block)
			}
		}
		if right.kind != 0 {
			for _, block := range discussionBlocks(discussions, fd, 0, right.no, true, width) {
				write(block)
			}
		}
	}

	var removed, added []splitSide
	flush := func() {
		for i := range max(len(removed), len(added)) {
			var left, right splitSide
			if i < len(removed) {
				left = removed[i]
			}
			if i < len(added) {
				right = added[i]
			}
			emit(left, right)
		}
		removed, added = nil, nil
	}

	oldLine, newLine := 0, 0
	for line := range strings.SplitSeq(strings.TrimSuffix(fd.Diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			flush()
			hunkLines = append(hunkLines, lineNo)
			if o, n, ok := parseHunkStart(line); ok {
				oldLine, newLine = o, n
			}
			write(hunkStyle.Render(truncate(line, width)))
		case strings.HasPrefix(line, "+"):
			added = append(added, splitSide{no: newLine, text: line[1:], kind: '+'})
			newLine++
		case strings.HasPrefix(line, "-"):
			removed = append(removed, splitSide{no: oldLine, text: line[1:], kind: '-'})
			oldLine++
		case strings.HasPrefix(line, `\`):
			flush()
			write(subtleStyle.Render(line))
		default:
			flush()
			code := line
			if len(code) > 0 {
				code = code[1:]
			}
			emit(splitSide{no: oldLine, text: code, kind: ' '}, splitSide{no: newLine, text: code, kind: ' '})
			oldLine++
			newLine++
		}
	}
	flush()
	return b.String(), hunkLines
}

var discussionStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder(), false, false, false, true).
	BorderForeground(lipgloss.Color("6")).
	PaddingLeft(1)

// discussionBlocks renders threads anchored at the current diff line.
// useNew selects which side the just-rendered line lives on.
func discussionBlocks(discussions []gitlabx.Discussion, fd gitlabx.FileDiff, oldLine, newLine int, useNew bool, width int) []string {
	var out []string
	for _, d := range discussions {
		anchor := d.Anchor()
		if anchor == nil {
			continue
		}
		if anchor.NewPath != fd.NewPath && anchor.OldPath != fd.OldPath {
			continue
		}
		matches := false
		if useNew && anchor.NewLine != nil {
			matches = *anchor.NewLine == newLine
		} else if !useNew && anchor.OldLine != nil && anchor.NewLine == nil {
			matches = *anchor.OldLine == oldLine
		}
		if !matches {
			continue
		}
		out = append(out, renderThread(d, width))
	}
	return out
}

func renderThread(d gitlabx.Discussion, width int) string {
	var b strings.Builder
	for i, n := range d.Notes {
		if n.System {
			continue
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		head := "💬 @" + n.Author
		if n.Resolved {
			head += subtleStyle.Render(" (resolved)")
		}
		if !n.CreatedAt.IsZero() {
			head += subtleStyle.Render(" · " + relTime(n.CreatedAt))
		}
		b.WriteString(headerStyle.Render(head) + "\n")
		body := n.Body
		if lines := strings.Split(body, "\n"); len(lines) > 6 {
			body = strings.Join(lines[:6], "\n") + "\n…"
		}
		b.WriteString(wrap(body, max(width-6, 30)))
	}
	return discussionStyle.Width(max(width-4, 30)).Render(b.String())
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
