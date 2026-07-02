package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

type (
	mrDetailLoadedMsg struct {
		iid    int64
		detail *gitlabx.MRDetail
	}
	mrDiffsLoadedMsg struct {
		iid   int64
		diffs []gitlabx.FileDiff
	}
	mrDiscussionsLoadedMsg struct {
		iid         int64
		discussions []gitlabx.Discussion
	}
	mrCommitsLoadedMsg struct {
		iid     int64
		commits []gitlabx.Commit
	}
	mrDetailErrMsg struct {
		iid int64
		err error
	}
)

// mrDetail shows one MR: metadata header plus a navigable, coloured diff.
type mrDetail struct {
	deps Deps
	svc  gitlabx.Service
	mr   gitlabx.MRSummary

	detail      *gitlabx.MRDetail
	diffs       []gitlabx.FileDiff
	commits     []gitlabx.Commit
	discussions []gitlabx.Discussion

	vp        viewport.Model
	spin      spinner.Model
	loading   int // outstanding requests
	err       error
	fileIdx   int
	hunkLines []int
	split     bool // side-by-side diff layout
	tree      *fileTree
	showTree  bool // file explorer sidebar visible
	treeFocus bool // keys drive the explorer instead of the diff
	width     int
	height    int

	// rendered caches chroma-highlighted diffs per file; re-rendering on
	// every n/p toggle would be wasteful on large files.
	rendered map[int]renderedDiff
}

type renderedDiff struct {
	content string
	hunks   []int
}

func newMRDetail(deps Deps, mr gitlabx.MRSummary) *mrDetail {
	return &mrDetail{
		deps:     deps,
		svc:      deps.Svc,
		mr:       mr,
		vp:       viewport.New(),
		spin:     spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		split:    deps.Cfg.UI.DiffView == "split",
		showTree: deps.Cfg.UI.FileExplorer == "open",
	}
}

func (s *mrDetail) Title() string {
	return fmt.Sprintf("%s · %s", s.mr.Ref(), truncate(s.mr.Title, 60))
}

func (s *mrDetail) Hints() string {
	if s.treeFocus {
		return "↑/↓ move · enter open · h/l fold/unfold · tab diff · e hide · esc back · q quit"
	}
	if s.treeWidth() > 0 {
		return "↑/↓ scroll · n/p file · ]/[ hunk · tab explorer · e hide · v layout · r review · L logs · o browser · esc back"
	}
	return "↑/↓ scroll · n/p file · ]/[ hunk · e explorer · v layout · r review · L logs · o browser · esc back · q quit"
}

func (s *mrDetail) Init() tea.Cmd {
	s.loading = 3
	svc, mr := s.svc, s.mr
	fetchDetail := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), listRequestTimeout)
		defer cancel()
		detail, err := svc.GetMergeRequest(ctx, mr.Project(), mr.IID)
		if err != nil {
			return mrDetailErrMsg{iid: mr.IID, err: err}
		}
		return mrDetailLoadedMsg{iid: mr.IID, detail: detail}
	}
	fetchDiffs := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), listRequestTimeout)
		defer cancel()
		diffs, err := svc.ListDiffs(ctx, mr.Project(), mr.IID)
		if err != nil {
			return mrDetailErrMsg{iid: mr.IID, err: err}
		}
		return mrDiffsLoadedMsg{iid: mr.IID, diffs: diffs}
	}
	fetchCommits := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), listRequestTimeout)
		defer cancel()
		commits, err := svc.ListCommits(ctx, mr.Project(), mr.IID)
		if err != nil {
			// Commit context is best-effort; the review runs without it.
			return mrCommitsLoadedMsg{iid: mr.IID}
		}
		return mrCommitsLoadedMsg{iid: mr.IID, commits: commits}
	}
	fetchDiscussions := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), listRequestTimeout)
		defer cancel()
		discussions, err := svc.ListDiscussions(ctx, mr.Project(), mr.IID)
		if err != nil {
			// Discussions are decoration; the diff view works without them.
			return mrDiscussionsLoadedMsg{iid: mr.IID}
		}
		return mrDiscussionsLoadedMsg{iid: mr.IID, discussions: discussions}
	}
	return tea.Batch(s.spin.Tick, fetchDetail, fetchDiffs, fetchCommits, fetchDiscussions)
}

func (s *mrDetail) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		widthChanged := msg.Width != s.width
		s.width, s.height = msg.Width, msg.Height
		if widthChanged {
			s.invalidateRender()
			if len(s.diffs) > 0 {
				s.setFile(s.fileIdx)
			}
		}
		s.layout()
		return s, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		s.spin, cmd = s.spin.Update(msg)
		return s, cmd

	case mrDetailLoadedMsg:
		if msg.iid != s.mr.IID {
			return s, nil
		}
		s.loading--
		s.detail = msg.detail
		s.layout()
		return s, nil

	case mrDiffsLoadedMsg:
		if msg.iid != s.mr.IID {
			return s, nil
		}
		s.loading--
		s.diffs = msg.diffs
		if len(msg.diffs) > 0 {
			s.tree = newFileTree(msg.diffs)
			s.tree.setDiscussions(msg.diffs, s.discussions)
		}
		s.setFile(0)
		s.layout()
		return s, nil

	case mrCommitsLoadedMsg:
		if msg.iid != s.mr.IID {
			return s, nil
		}
		s.loading--
		s.commits = msg.commits
		return s, nil

	case mrDiscussionsLoadedMsg:
		if msg.iid != s.mr.IID {
			return s, nil
		}
		s.discussions = msg.discussions
		s.invalidateRender()
		if s.tree != nil {
			s.tree.setDiscussions(s.diffs, msg.discussions)
		}
		if len(s.diffs) > 0 {
			s.setFile(s.fileIdx) // re-render with threads anchored
		}
		return s, nil

	case mrDetailErrMsg:
		if msg.iid != s.mr.IID {
			return s, nil
		}
		s.loading = 0
		s.err = msg.err
		return s, nil

	case tea.KeyPressMsg:
		if s.treeFocus && s.treeWidth() > 0 {
			switch msg.String() {
			case "up", "k":
				s.tree.move(-1)
				return s, nil
			case "down", "j":
				s.tree.move(1)
				return s, nil
			case "enter", "l", "right":
				if n := s.tree.selected(); n != nil {
					if n.isDir() {
						s.tree.toggle()
					} else {
						s.setFile(n.diffIdx)
					}
				}
				return s, nil
			case "h", "left":
				s.tree.collapseOrUp()
				return s, nil
			case "g":
				s.tree.first()
				return s, nil
			case "G":
				s.tree.last()
				return s, nil
			case "esc":
				s.treeFocus = false
				return s, nil
			}
		}
		switch msg.String() {
		case "esc":
			return s, popScreen
		case "e":
			if s.tree == nil {
				return s, nil
			}
			s.showTree = !s.showTree
			if !s.showTree {
				s.treeFocus = false
			}
			s.invalidateRender()
			s.setFile(s.fileIdx)
			s.layout()
			return s, nil
		case "tab":
			if s.treeWidth() > 0 {
				s.treeFocus = !s.treeFocus
			}
			return s, nil
		case "q":
			return s, tea.Quit
		case "r":
			if s.detail == nil || s.loading > 0 {
				return s, nil
			}
			return s, pushScreen(newReviewRun(s.deps, *s.detail, s.diffs, s.commits))
		case "L":
			return s, pushScreen(newLogList(s.deps, s.mr.Ref()))
		case "n", "right":
			s.setFile(s.fileIdx + 1)
			return s, nil
		case "p", "left":
			s.setFile(s.fileIdx - 1)
			return s, nil
		case "o":
			return s, openURLCmd(s.deps, s.mr.WebURL)
		case "v":
			s.split = !s.split
			s.invalidateRender()
			s.setFile(s.fileIdx)
			return s, nil
		case "]":
			s.jumpHunk(1)
			return s, nil
		case "[":
			s.jumpHunk(-1)
			return s, nil
		case "g":
			s.vp.GotoTop()
			return s, nil
		case "G":
			s.vp.GotoBottom()
			return s, nil
		}
	}

	var cmd tea.Cmd
	s.vp, cmd = s.vp.Update(msg)
	return s, cmd
}

func (s *mrDetail) setFile(idx int) {
	if len(s.diffs) == 0 {
		return
	}
	keepOffset := idx == s.fileIdx
	offset := s.vp.YOffset()
	s.fileIdx = (idx + len(s.diffs)) % len(s.diffs)

	if s.rendered == nil {
		s.rendered = map[int]renderedDiff{}
	}
	r, ok := s.rendered[s.fileIdx]
	if !ok {
		content, hunks := renderDiff(s.diffs[s.fileIdx], s.discussions, max(s.mainWidth(), 60), s.split)
		r = renderedDiff{content: content, hunks: hunks}
		s.rendered[s.fileIdx] = r
	}
	s.hunkLines = r.hunks
	if s.tree != nil {
		s.tree.reveal(s.fileIdx)
	}
	s.vp.SetContent(r.content)
	if keepOffset {
		s.vp.SetYOffset(offset)
	} else {
		s.vp.GotoTop()
	}
}

// invalidateRender drops cached renders after inputs change (new
// discussions, resize affecting thread wrapping).
func (s *mrDetail) invalidateRender() {
	s.rendered = nil
}

func (s *mrDetail) jumpHunk(dir int) {
	if len(s.hunkLines) == 0 {
		return
	}
	cur := s.vp.YOffset()
	if dir > 0 {
		for _, l := range s.hunkLines {
			if l > cur {
				s.vp.SetYOffset(l)
				return
			}
		}
	} else {
		for i := len(s.hunkLines) - 1; i >= 0; i-- {
			if s.hunkLines[i] < cur {
				s.vp.SetYOffset(s.hunkLines[i])
				return
			}
		}
		s.vp.GotoTop()
	}
}

func (s *mrDetail) header() string {
	var b strings.Builder
	state := s.mr.State
	if s.mr.Draft {
		state += " · " + draftStyle.Render("draft")
	}
	fmt.Fprintf(&b, "%s  %s\n", headerStyle.Render(truncate(s.mr.Title, max(s.mainWidth()-20, 20))), subtleStyle.Render(state))
	fmt.Fprintf(&b, "%s → %s · @%s", s.mr.SourceBranch, s.mr.TargetBranch, s.mr.Author)
	if s.detail != nil && s.detail.HasConflicts {
		b.WriteString(" · " + errorStyle.Render("has conflicts"))
	}
	b.WriteByte('\n')
	b.WriteString(subtleStyle.Render(truncate(s.mr.WebURL, max(s.width-2, 20))) + "\n")
	if len(s.diffs) > 0 {
		fmt.Fprintf(&b, "%s\n", subtleStyle.Render(fmt.Sprintf("file %d/%d · %s", s.fileIdx+1, len(s.diffs), truncate(s.diffs[s.fileIdx].Path(), max(s.mainWidth()-14, 20)))))
	} else if s.loading > 0 {
		fmt.Fprintf(&b, "%s loading…\n", s.spin.View())
	} else {
		b.WriteString(subtleStyle.Render("no changes") + "\n")
	}
	return b.String()
}

// headerHeight is the number of lines header() renders.
const headerHeight = 4

// treeWidth is the columns given to the file explorer, 0 when it is hidden
// or the terminal is too narrow to split.
func (s *mrDetail) treeWidth() int {
	if !s.showTree || s.tree == nil || s.width < 80 {
		return 0
	}
	return min(max(s.width/4, 20), 36)
}

// mainWidth is what remains for the header and diff pane after the explorer
// and its separator column.
func (s *mrDetail) mainWidth() int {
	if tw := s.treeWidth(); tw > 0 {
		return s.width - tw - 1
	}
	return s.width
}

func (s *mrDetail) layout() {
	if s.width == 0 {
		return
	}
	s.vp.SetWidth(s.mainWidth())
	s.vp.SetHeight(max(s.height-headerHeight, 1))
}

func (s *mrDetail) View() string {
	if s.err != nil {
		return s.header() + "\n" + errorStyle.Render(truncate("error: "+s.err.Error(), max(s.width*2, 20)))
	}
	main := s.header() + s.vp.View()
	tw := s.treeWidth()
	if tw == 0 {
		return main
	}
	h := max(s.height, 1)
	sidebar := s.tree.view(s.diffs, tw, h, s.treeFocus, s.fileIdx)
	sep := strings.TrimSuffix(strings.Repeat(subtleStyle.Render("│")+"\n", h), "\n")
	return lipgloss.JoinHorizontal(lipgloss.Top, sidebar, sep, main)
}
