package tui

import (
	"fmt"
	"log/slog"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
)

var severityStyles = map[review.Severity]lipgloss.Style{
	review.SeverityCritical: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("1")).Padding(0, 1),
	review.SeverityMajor:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9")).Padding(0, 1),
	review.SeverityMinor:    lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Padding(0, 1),
	review.SeverityInfo:     lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Padding(0, 1),
}

// manualStyle badges comments the reviewer wrote by hand, where model
// findings show their severity.
var manualStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Padding(0, 1)

var stateStyles = map[review.FindingState]lipgloss.Style{
	review.StateAccepted:       lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
	review.StateRejected:       lipgloss.NewStyle().Faint(true).Strikethrough(true),
	review.StateBelowThreshold: lipgloss.NewStyle().Faint(true),
	review.StatePending:        lipgloss.NewStyle(),
}

// findings lets the engineer curate the review result: view each finding
// against its diff hunk, edit the body, accept or reject, then publish.
type findings struct {
	deps   Deps
	detail gitlabx.MRDetail
	diffs  []gitlabx.FileDiff
	cfg    config.Config

	result  *review.Result
	items   []review.Finding
	cursor  int
	logPath string // this run's stored progress log ("" when not stored)

	// rec is this review's stored record; every curation change re-saves it
	// so the review can be reopened after the session ends. Nil when result
	// storage is disabled.
	rec *resultstore.Record
	// reopened marks a screen restored from a stored record: curation
	// continues where it left off, without re-running auto-publish.
	reopened bool

	// manualReport forwards publish outcomes for manual comments that were
	// composed in the diff view, so that screen's copies stay in sync.
	manualReport func(id string, state review.FindingState)

	editing bool
	editor  textarea.Model

	width  int
	height int
}

func newFindings(deps Deps, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, result *review.Result, rec *resultstore.Record, manual []review.Finding, manualReport func(string, review.FindingState)) *findings {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	cfg := deps.cfgFor(detail.ProjectPath)
	items := make([]review.Finding, len(result.Findings))
	copy(items, result.Findings)

	// The publish floor: findings below publish.min_severity are marked up
	// front so triage shows they will never reach GitLab; the publisher
	// enforces the floor again at publish time.
	floor := review.Severity(cfg.Publish.MinSeverity)
	for i := range items {
		if items[i].Severity.Valid() && !items[i].Severity.AtLeast(floor) {
			items[i].State = review.StateBelowThreshold
		}
	}

	// auto_comment: findings at or above the severity threshold are
	// accepted up front and published without confirmation; weaker ones
	// still go through interactive curation.
	if cfg.Publish.AutoComment {
		for i := range items {
			if items[i].State == review.StatePending &&
				items[i].Severity.AtLeast(review.Severity(cfg.Publish.AutoMinSeverity)) {
				items[i].State = review.StateAccepted
			}
		}
	}

	// Manual comments composed in the diff view are curated and published
	// alongside the review's findings; they arrive already accepted.
	items = append(items, manual...)

	s := &findings{
		deps:         deps,
		detail:       detail,
		diffs:        diffs,
		cfg:          cfg,
		result:       result,
		items:        items,
		rec:          rec,
		manualReport: manualReport,
		editor:       ta,
	}
	if rec != nil {
		s.logPath = rec.LogPath
		// The stored record has the model's findings; bring it up to date
		// with the curated set (auto-accepts, manual comments).
		s.persist()
	}
	return s
}

// newFindingsFromRecord reopens a stored review: the same curation screen,
// with findings and their states restored from the record. Further curation
// re-saves the same file.
func newFindingsFromRecord(deps Deps, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, rec *resultstore.Record) *findings {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	items := make([]review.Finding, len(rec.Findings))
	copy(items, rec.Findings)
	return &findings{
		deps:     deps,
		detail:   detail,
		diffs:    diffs,
		cfg:      deps.cfgFor(detail.ProjectPath),
		result:   &review.Result{Summary: rec.Summary, Warnings: rec.Warnings},
		items:    items,
		logPath:  rec.LogPath,
		rec:      rec,
		reopened: true,
		editor:   ta,
	}
}

// persist re-saves the record with the current curation states. Best-effort:
// a failed write must never block curation.
func (s *findings) persist() {
	if s.rec == nil {
		return
	}
	s.rec.Findings = append([]review.Finding(nil), s.items...)
	if err := s.deps.Results.Save(*s.rec); err != nil {
		slog.Warn("storing review result failed", "error", err)
	}
}

// setState updates a finding by ID; used by the publish screen to report
// results back (runs on the UI goroutine).
func (s *findings) setState(id string, state review.FindingState) {
	for i := range s.items {
		if s.items[i].ID == id {
			s.items[i].State = state
			if s.items[i].Manual && s.manualReport != nil {
				s.manualReport(id, state)
			}
			s.persist()
			return
		}
	}
}

// addComment appends a manual MR-level comment composed from this screen.
func (s *findings) addComment(f review.Finding) {
	s.items = append(s.items, f)
	s.cursor = len(s.items) - 1
	s.persist()
}

func (s *findings) Title() string {
	return fmt.Sprintf("findings · %s · %d suggestion(s)", s.detail.Ref(), len(s.items))
}

// Typing reports whether the body editor currently captures keystrokes.
func (s *findings) Typing() bool { return s.editing }

func (s *findings) Hints() string {
	if s.editing {
		return "ctrl+s save · esc discard edit"
	}
	hints := "↑/↓ move · a accept · x reject · A accept all · e edit · c new comment · p publish accepted"
	if s.logPath != "" {
		hints += " · l log"
	}
	return hints + " · o browser · esc back"
}

func (s *findings) Init() tea.Cmd {
	if s.cfg.Publish.AutoComment && !s.reopened {
		if auto := s.accepted(); len(auto) > 0 {
			// Publish the auto-accepted findings straight away; the
			// publish screen pops back here for the remaining ones.
			return pushScreen(newPublish(s.deps, s.detail, s.diffs, auto,
				publishOpts{auto: true, popCount: 1, report: s.setState}))
		}
	}
	return nil
}

func (s *findings) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		s.editor.SetWidth(max(s.width-4, 20))
		s.editor.SetHeight(max(s.detailHeight()-2, 3))
		return s, nil

	case tea.KeyPressMsg:
		if s.editing {
			return s.updateEditor(msg)
		}
		return s.updateList(msg)
	}
	return s, nil
}

func (s *findings) updateEditor(msg tea.KeyPressMsg) (Screen, tea.Cmd) {
	switch msg.String() {
	case "ctrl+s":
		s.items[s.cursor].Body = strings.TrimSpace(s.editor.Value())
		s.editing = false
		s.persist()
		return s, nil
	case "esc":
		s.editing = false
		return s, nil
	}
	var cmd tea.Cmd
	s.editor, cmd = s.editor.Update(msg)
	return s, cmd
}

func (s *findings) updateList(msg tea.KeyPressMsg) (Screen, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return s, popScreen
	case "q":
		return s, tea.Quit
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j":
		if s.cursor < len(s.items)-1 {
			s.cursor++
		}
	case "a":
		if len(s.items) > 0 {
			s.items[s.cursor].State = review.StateAccepted
			if s.cursor < len(s.items)-1 {
				s.cursor++
			}
			s.persist()
		}
	case "x":
		if len(s.items) > 0 {
			s.items[s.cursor].State = review.StateRejected
			if s.cursor < len(s.items)-1 {
				s.cursor++
			}
			s.persist()
		}
	case "A":
		for i := range s.items {
			if s.items[i].State == review.StatePending {
				s.items[i].State = review.StateAccepted
			}
		}
		s.persist()
	case "e":
		if len(s.items) > 0 {
			s.editing = true
			s.editor.SetValue(s.items[s.cursor].Body)
			return s, s.editor.Focus()
		}
	case "l":
		if s.logPath != "" {
			return s, pushScreen(newLogView(s.deps, s.detail.Ref(), s.detail.WebURL, s.logPath))
		}
	case "c":
		// A manual MR-level comment, published with the accepted findings.
		return s, pushScreen(newCommentComposer(nil, "", s.addComment))
	case "o":
		return s, openURLCmd(s.deps, s.detail.WebURL)
	case "p":
		accepted := s.accepted()
		if len(accepted) == 0 {
			return s, nil
		}
		return s, pushScreen(newPublish(s.deps, s.detail, s.diffs, accepted, publishOpts{report: s.setState}))
	}
	return s, nil
}

func (s *findings) accepted() []review.Finding {
	var out []review.Finding
	for _, f := range s.items {
		if f.State == review.StateAccepted {
			out = append(out, f)
		}
	}
	return out
}

func (s *findings) listHeight() int {
	// list gets up to 40% of the screen, at least 4 lines
	return max(min(len(s.items)+1, s.height*2/5), 4)
}

func (s *findings) detailHeight() int {
	return max(s.height-s.listHeight()-2, 5)
}

func (s *findings) View() string {
	if len(s.items) == 0 {
		summary := s.result.Summary
		if summary == "" {
			summary = "The reviewer found nothing to flag."
		}
		return headerStyle.Render("no findings") + "\n\n" + wrap(summary, s.width) + "\n\n" +
			warningsView(s.result.Warnings, s.width) + subtleStyle.Render("esc to go back")
	}

	var b strings.Builder

	// warnings banner (rebase status, truncation, failed passes) — shown
	// above the findings list, not only on the empty-review screen.
	b.WriteString(warningsView(s.result.Warnings, s.width))

	// list pane
	listH := s.listHeight()
	start := 0
	if s.cursor >= listH {
		start = s.cursor - listH + 1
	}
	for i := start; i < min(start+listH, len(s.items)); i++ {
		f := s.items[i]
		prefix := "  "
		if i == s.cursor {
			prefix = "> "
		}
		sev := severityStyles[f.Severity].Render(string(f.Severity))
		if f.Manual {
			sev = manualStyle.Render("manual")
		}
		state := f.State.String()
		if st, ok := stateStyles[f.State]; ok && f.State != review.StatePending {
			state = st.Render(state)
		} else if f.State == review.StatePending {
			state = subtleStyle.Render(state)
		}
		line := fmt.Sprintf("%s%s %-30s %s  %s", prefix, sev,
			truncate(findingLocation(f), 30),
			truncate(manualTitle(f), max(s.width-55, 15)), state)
		b.WriteString(truncate(line, s.width) + "\n")
	}
	b.WriteString(strings.Repeat("─", max(s.width, 1)) + "\n")

	// detail pane
	if s.editing {
		b.WriteString(headerStyle.Render("editing comment body") + "\n")
		b.WriteString(s.editor.View())
		return b.String()
	}

	f := s.items[s.cursor]
	if f.Manual {
		fmt.Fprintf(&b, "%s %s  %s\n\n", manualStyle.Render("manual"),
			headerStyle.Render("your comment"), subtleStyle.Render(findingLocation(f)))
	} else {
		meta := string(f.Severity) + " · " + string(f.Category)
		// The agent badge only adds signal when it isn't the category's
		// builtin agent (custom agents, shadowed builtins).
		if f.Agent != "" && f.Agent != string(f.Category) {
			meta += " · " + f.Agent
		}
		fmt.Fprintf(&b, "%s %s  %s\n\n", severityStyles[f.Severity].Render(meta),
			headerStyle.Render(f.Title), subtleStyle.Render(findingLocation(f)))
	}

	detailLines := s.detailHeight() - 3 - warningsHeight(s.result.Warnings)
	body := wrap(f.Body, s.width-2)
	if f.Suggestion != "" {
		body += "\n\n" + subtleStyle.Render("suggested replacement:") + "\n" + addedStyle.Render("+"+f.Suggestion)
	}
	if hunk := hunkExcerpt(s.diffs, f, 4); hunk != "" {
		body += "\n\n" + hunk
	}
	lines := strings.Split(body, "\n")
	if len(lines) > detailLines {
		lines = lines[:detailLines]
	}
	b.WriteString(strings.Join(lines, "\n"))
	return b.String()
}

// warningsHeight is the number of screen lines warningsView renders, so
// panes below the banner can reserve space for it.
func warningsHeight(warnings []string) int {
	if len(warnings) == 0 {
		return 0
	}
	return len(warnings) + 1 // one line per warning plus a trailing blank
}

func warningsView(warnings []string, width int) string {
	if len(warnings) == 0 {
		return ""
	}
	var b strings.Builder
	for _, w := range warnings {
		b.WriteString(draftStyle.Render("⚠ "+truncate(w, max(width-3, 20))) + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

func lineLabel(l review.LineRef) string {
	switch {
	case l.NewLine != nil:
		return fmt.Sprintf("%d", *l.NewLine)
	case l.OldLine != nil:
		return fmt.Sprintf("%d(old)", *l.OldLine)
	default:
		return "?"
	}
}

// hunkExcerpt renders the diff lines around the finding's location so the
// suggestion can be judged in context without leaving the screen.
func hunkExcerpt(diffs []gitlabx.FileDiff, f review.Finding, radius int) string {
	var fd *gitlabx.FileDiff
	for i := range diffs {
		if diffs[i].NewPath == f.File || diffs[i].OldPath == f.File {
			fd = &diffs[i]
			break
		}
	}
	if fd == nil {
		return ""
	}

	type numbered struct {
		text    string
		oldLine int
		newLine int
	}
	var lines []numbered
	oldLine, newLine := 0, 0
	target := -1
	for _, raw := range strings.Split(strings.TrimSuffix(fd.Diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(raw, "@@"):
			if o, n, ok := parseHunkStart(raw); ok {
				oldLine, newLine = o, n
			}
			lines = append(lines, numbered{text: hunkStyle.Render(raw)})
			continue
		case strings.HasPrefix(raw, "+"):
			lines = append(lines, numbered{text: addedStyle.Render(raw), newLine: newLine})
			if f.Line.NewLine != nil && newLine == *f.Line.NewLine {
				target = len(lines) - 1
			}
			newLine++
		case strings.HasPrefix(raw, "-"):
			lines = append(lines, numbered{text: removedStyle.Render(raw), oldLine: oldLine})
			if f.Line.NewLine == nil && f.Line.OldLine != nil && oldLine == *f.Line.OldLine {
				target = len(lines) - 1
			}
			oldLine++
		default:
			lines = append(lines, numbered{text: raw, oldLine: oldLine, newLine: newLine})
			if f.Line.NewLine != nil && newLine == *f.Line.NewLine {
				target = len(lines) - 1
			}
			oldLine++
			newLine++
		}
	}
	if target < 0 {
		return ""
	}

	from := max(target-radius, 0)
	to := min(target+radius+1, len(lines))
	var b strings.Builder
	b.WriteString(subtleStyle.Render("diff context:") + "\n")
	for i := from; i < to; i++ {
		marker := "  "
		if i == target {
			marker = "→ "
		}
		b.WriteString(marker + lines[i].text + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// parseHunkStart extracts starting line numbers from a hunk header.
func parseHunkStart(line string) (oldStart, newStart int, ok bool) {
	var o, n int
	if _, err := fmt.Sscanf(line, "@@ -%d", &o); err != nil {
		return 0, 0, false
	}
	plus := strings.Index(line, "+")
	if plus < 0 {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(line[plus:], "+%d", &n); err != nil {
		return 0, 0, false
	}
	return max(o, 1), max(n, 1), true
}
