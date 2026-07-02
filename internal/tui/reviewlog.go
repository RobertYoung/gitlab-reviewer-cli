package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
)

type (
	historyEntriesMsg struct {
		ref     string
		entries []historyEntry
		err     error
	}
	recordLoadedMsg struct {
		path string
		rec  *resultstore.Record
		err  error
	}
	logContentMsg struct {
		path    string
		content string
		err     error
	}
)

// historyEntry is one past review of an MR: its stored result, reopenable in
// the findings screen, and/or its run log. Older runs may have only a log.
type historyEntry struct {
	started    string // pre-formatted; entries come from two stores
	title      string
	recordPath string // "" when only the log survived
	logPath    string // "" when the log is gone
	findings   int
	accepted   int
}

// reviewHistory browses the stored reviews of one MR, newest first. Enter
// reopens a review's findings — states included — so curation can continue
// across sessions; l shows the run's progress log.
type reviewHistory struct {
	deps    Deps
	detail  gitlabx.MRDetail
	diffs   []gitlabx.FileDiff
	entries []historyEntry
	cursor  int
	loaded  bool
	err     error
	width   int
	height  int
}

func newReviewHistory(deps Deps, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff) *reviewHistory {
	return &reviewHistory{deps: deps, detail: detail, diffs: diffs}
}

func (s *reviewHistory) Title() string { return "past reviews · " + s.detail.Ref() }
func (s *reviewHistory) Hints() string {
	return "↑/↓ move · enter open findings · l log · o browser · esc back"
}

const historyTimeFormat = "2006-01-02 15:04:05"

func (s *reviewHistory) Init() tea.Cmd {
	ref, results, logs := s.detail.Ref(), s.deps.Results, s.deps.Logs
	return func() tea.Msg {
		records, err := results.List(ref)
		if err != nil {
			return historyEntriesMsg{ref: ref, err: err}
		}
		runlogs, err := logs.List(ref)
		if err != nil {
			return historyEntriesMsg{ref: ref, err: err}
		}

		// One entry per run: records lead; logs whose run has a record are
		// folded into it, the rest (runs that predate result storage, or
		// failed before producing a result) get log-only entries.
		entries := make([]historyEntry, 0, len(records)+len(runlogs))
		covered := make(map[string]bool, len(records))
		for _, r := range records {
			covered[r.LogPath] = true
			entries = append(entries, historyEntry{
				started:    r.Started.Format(historyTimeFormat),
				title:      r.Title,
				recordPath: r.Path,
				logPath:    r.LogPath,
				findings:   r.Findings,
				accepted:   r.Accepted,
			})
		}
		for _, l := range runlogs {
			if covered[l.Path] {
				continue
			}
			entries = append(entries, historyEntry{
				started: l.Started.Format(historyTimeFormat),
				title:   l.Title,
				logPath: l.Path,
			})
		}
		// The formatted timestamp sorts lexicographically; newest first.
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].started > entries[j].started })
		return historyEntriesMsg{ref: ref, entries: entries}
	}
}

func (s *reviewHistory) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height

	case historyEntriesMsg:
		if msg.ref != s.detail.Ref() {
			return s, nil
		}
		s.loaded = true
		s.entries, s.err = msg.entries, msg.err

	case recordLoadedMsg:
		if msg.err != nil {
			s.err = msg.err
			return s, nil
		}
		return s, pushScreen(newFindingsFromRecord(s.deps, s.detail, s.diffs, msg.rec))

	case tea.KeyPressMsg:
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
			if s.cursor < len(s.entries)-1 {
				s.cursor++
			}
		case "enter":
			if s.cursor >= len(s.entries) {
				return s, nil
			}
			e := s.entries[s.cursor]
			if e.recordPath == "" {
				if e.logPath != "" {
					return s, pushScreen(newLogView(s.deps, s.detail.Ref(), s.detail.WebURL, e.logPath))
				}
				return s, nil
			}
			results, path := s.deps.Results, e.recordPath
			return s, func() tea.Msg {
				rec, err := results.Load(path)
				return recordLoadedMsg{path: path, rec: &rec, err: err}
			}
		case "l":
			if s.cursor < len(s.entries) && s.entries[s.cursor].logPath != "" {
				return s, pushScreen(newLogView(s.deps, s.detail.Ref(), s.detail.WebURL, s.entries[s.cursor].logPath))
			}
		case "o":
			return s, openURLCmd(s.deps, s.detail.WebURL)
		}
	}
	return s, nil
}

func (s *reviewHistory) View() string {
	switch {
	case s.err != nil:
		return errorStyle.Render(truncate("error: "+s.err.Error(), max(s.width*2, 20)))
	case !s.loaded:
		return subtleStyle.Render("loading…")
	case len(s.entries) == 0:
		return subtleStyle.Render("no stored reviews for this MR") + "\n\n" +
			subtleStyle.Render("run a review with r; its findings and log are kept here for reference")
	}

	var b strings.Builder
	visible := max(s.height, 3)
	start := 0
	if s.cursor >= visible {
		start = s.cursor - visible + 1
	}
	for i := start; i < min(start+visible, len(s.entries)); i++ {
		e := s.entries[i]
		prefix := "  "
		if i == s.cursor {
			prefix = "> "
		}
		meta := "log only"
		if e.recordPath != "" {
			meta = fmt.Sprintf("%d finding(s), %d accepted", e.findings, e.accepted)
		}
		line := fmt.Sprintf("%s%s  %-26s %s", prefix, e.started, meta,
			subtleStyle.Render(truncate(e.title, max(s.width-52, 10))))
		b.WriteString(truncate(line, s.width) + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// logView displays one stored review run log in a scrollable viewport.
type logView struct {
	deps   Deps
	ref    string
	webURL string
	path   string
	vp     viewport.Model
	loaded bool
	err    error
	width  int
	height int
}

func newLogView(deps Deps, ref, webURL, path string) *logView {
	return &logView{deps: deps, ref: ref, webURL: webURL, path: path, vp: viewport.New()}
}

func (s *logView) Title() string { return "review log · " + s.ref }
func (s *logView) Hints() string { return "↑/↓ scroll · g/G top/bottom · o browser · esc back" }

func (s *logView) Init() tea.Cmd {
	path := s.path
	return func() tea.Msg {
		data, err := os.ReadFile(path) //nolint:gosec // paths come from the run-log store
		return logContentMsg{path: path, content: string(data), err: err}
	}
}

func (s *logView) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		s.vp.SetWidth(s.width)
		s.vp.SetHeight(max(s.height, 1))
		return s, nil

	case logContentMsg:
		if msg.path != s.path {
			return s, nil
		}
		s.loaded = true
		s.err = msg.err
		s.vp.SetContent(msg.content)
		return s, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			return s, popScreen
		case "q":
			return s, tea.Quit
		case "g":
			s.vp.GotoTop()
			return s, nil
		case "G":
			s.vp.GotoBottom()
			return s, nil
		case "o":
			return s, openURLCmd(s.deps, s.webURL)
		}
	}
	var cmd tea.Cmd
	s.vp, cmd = s.vp.Update(msg)
	return s, cmd
}

func (s *logView) View() string {
	switch {
	case s.err != nil:
		return errorStyle.Render(truncate("error: "+s.err.Error(), max(s.width*2, 20)))
	case !s.loaded:
		return subtleStyle.Render("loading…")
	}
	return s.vp.View()
}
