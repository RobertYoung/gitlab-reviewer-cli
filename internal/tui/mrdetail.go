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
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
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
	// mrApprovalsMsg carries the approval state after the initial fetch or
	// an approve/unapprove action; err is only set for failed actions.
	mrApprovalsMsg struct {
		iid       int64
		approvals *gitlabx.Approvals
		err       error
	}
	// approvalGateMsg reports that the severity gate stopped an approval:
	// warn asks for a confirming second press, block refuses outright.
	approvalGateMsg struct {
		iid      int64
		blocking int
		min      string
		block    bool
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

	// approvals is nil until fetched (or when the instance exposes none);
	// approvalBusy guards against double-firing while a toggle is in flight.
	approvals    *gitlabx.Approvals
	approvalBusy bool
	approvalErr  error
	// gateNotice is the severity gate's message in the header; gateConfirmed
	// lets the next approval press through a warn-mode gate.
	gateNotice    string
	gateConfirmed bool

	vp        viewport.Model
	spin      spinner.Model
	loading   int // outstanding requests
	err       error
	fileIdx   int
	hunkLines []int
	split     bool // side-by-side diff layout
	overview  bool // viewport shows description + commits instead of the diff
	tree      *fileTree
	showTree  bool // file explorer sidebar visible
	treeFocus bool // keys drive the explorer instead of the diff
	width     int
	height    int

	// cursor is the selected rendered line of the current file (-1 when the
	// file has no commentable line); lineRefs maps rendered lines back to
	// diff line numbers so manual comments can anchor.
	cursor   int
	lineRefs []diffLineRef

	// comments are manual comments composed in this view, waiting to be
	// published (directly with P, or alongside a review's findings via r).
	comments []review.Finding

	// rendered caches chroma-highlighted diffs per file; re-rendering on
	// every n/p toggle would be wasteful on large files.
	rendered map[int]renderedDiff
}

type renderedDiff struct {
	lines []string
	hunks []int
	refs  []diffLineRef
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
	if s.overview {
		return "↑/↓ scroll · d/esc back to diff · o browser · q quit"
	}
	if s.treeFocus {
		return "↑/↓ move · enter open · h/l fold/unfold · tab diff · e hide · esc back · q quit"
	}
	explorer := "e explorer"
	if s.treeWidth() > 0 {
		explorer = "tab explorer · e hide"
	}
	approve := "a approve"
	if s.approvals != nil && s.approvals.UserHasApproved {
		approve = "a unapprove"
	}
	chat := ""
	if s.deps.Chatter != nil {
		chat = "t/T chat · "
	}
	hints := "↑/↓ move · n/p file · ]/[ hunk · c comment · C MR comment · " + chat + explorer + " · v layout · d overview · r review · L past reviews · " + approve + " · o browser · esc back"
	if len(s.pendingComments()) > 0 {
		hints = "P publish comments · " + hints
	}
	return hints
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
	fetchApprovals := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), listRequestTimeout)
		defer cancel()
		approvals, err := svc.GetApprovals(ctx, mr.Project(), mr.IID)
		if err != nil {
			// Approval state is decoration; the view works without it.
			return mrApprovalsMsg{iid: mr.IID}
		}
		return mrApprovalsMsg{iid: mr.IID, approvals: approvals}
	}
	return tea.Batch(s.spin.Tick, fetchDetail, fetchDiffs, fetchCommits, fetchDiscussions, fetchApprovals)
}

// toggleApproval approves the MR, or removes the user's approval when one
// is already recorded, then refetches the approval state. Approving first
// consults the severity gate: with blocking findings in the MR's last stored
// review, warn requires a confirming second press and block refuses.
func (s *mrDetail) toggleApproval() tea.Cmd {
	svc, mr, sha := s.svc, s.mr, s.detail.HeadSHA
	unapprove := s.approvals != nil && s.approvals.UserHasApproved
	gate := s.deps.cfgFor(s.detail.ProjectPath).Gate
	checkGate := !unapprove && gate.Enabled() && gate.Approvals != "off" && !s.gateConfirmed
	results, ref := s.deps.Results, s.detail.Ref()
	s.gateConfirmed = false
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), listRequestTimeout)
		defer cancel()
		if checkGate {
			// Best-effort: an unreadable store must not wedge approvals.
			if n, err := results.LatestBlocking(ref, review.Severity(gate.MinSeverity)); err == nil && n > 0 {
				return approvalGateMsg{iid: mr.IID, blocking: n, min: gate.MinSeverity, block: gate.Approvals == "block"}
			}
		}
		var err error
		if unapprove {
			err = svc.Unapprove(ctx, mr.Project(), mr.IID)
		} else {
			err = svc.Approve(ctx, mr.Project(), mr.IID, sha)
		}
		if err != nil {
			return mrApprovalsMsg{iid: mr.IID, err: err}
		}
		approvals, err := svc.GetApprovals(ctx, mr.Project(), mr.IID)
		if err != nil {
			return mrApprovalsMsg{iid: mr.IID, err: err}
		}
		return mrApprovalsMsg{iid: mr.IID, approvals: approvals}
	}
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
			if s.overview {
				s.vp.SetContent(s.overviewContent())
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
		if s.overview {
			s.vp.SetContent(s.overviewContent())
		}
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

	case mrApprovalsMsg:
		if msg.iid != s.mr.IID {
			return s, nil
		}
		s.approvalBusy = false
		s.approvalErr = msg.err
		if msg.err == nil {
			s.gateNotice = ""
		}
		if msg.approvals != nil {
			s.approvals = msg.approvals
		}
		return s, nil

	case approvalGateMsg:
		if msg.iid != s.mr.IID {
			return s, nil
		}
		s.approvalBusy = false
		if msg.block {
			s.gateNotice = fmt.Sprintf("approval blocked: %d finding(s) ≥ %s in the last review", msg.blocking, msg.min)
		} else {
			s.gateNotice = fmt.Sprintf("%d finding(s) ≥ %s in the last review — press a again to approve anyway", msg.blocking, msg.min)
			s.gateConfirmed = true
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
		if s.overview {
			switch msg.String() {
			case "d", "esc":
				s.overview = false
				s.setFile(s.fileIdx)
				return s, nil
			case "q":
				return s, tea.Quit
			case "o":
				return s, openURLCmd(s.deps, s.mr.WebURL)
			}
			var cmd tea.Cmd
			s.vp, cmd = s.vp.Update(msg)
			return s, cmd
		}
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
			return s, pushScreen(newAgentPicker(s.deps, *s.detail, s.diffs, s.commits, s.pendingComments(), s.setCommentState))
		case "L":
			if s.detail == nil || s.loading > 0 {
				return s, nil
			}
			return s, pushScreen(newReviewHistory(s.deps, *s.detail, s.diffs))
		case "n", "right":
			s.setFile(s.fileIdx + 1)
			return s, nil
		case "p", "left":
			s.setFile(s.fileIdx - 1)
			return s, nil
		case "o":
			return s, openURLCmd(s.deps, s.mr.WebURL)
		case "a":
			if s.detail == nil || s.loading > 0 || s.approvalBusy {
				return s, nil
			}
			s.approvalBusy = true
			return s, s.toggleApproval()
		case "v":
			s.split = !s.split
			s.invalidateRender()
			s.setFile(s.fileIdx)
			return s, nil
		case "d":
			s.overview = true
			s.vp.SetContent(s.overviewContent())
			s.vp.GotoTop()
			return s, nil
		case "]":
			s.jumpHunk(1)
			return s, nil
		case "[":
			s.jumpHunk(-1)
			return s, nil
		case "up", "k":
			if len(s.lineRefs) > 0 {
				s.moveCursor(-1)
				return s, nil
			}
		case "down", "j":
			if len(s.lineRefs) > 0 {
				s.moveCursor(1)
				return s, nil
			}
		case "g":
			s.vp.GotoTop()
			s.snapCursor(0)
			s.refresh()
			return s, nil
		case "G":
			s.vp.GotoBottom()
			s.snapCursor(len(s.lineRefs) - 1)
			s.refresh()
			return s, nil
		case "c":
			ref, excerpt, ok := s.cursorLine()
			if !ok {
				return s, nil
			}
			anchor := &commentAnchor{file: s.diffs[s.fileIdx].NewPath, line: ref}
			return s, pushScreen(newCommentComposer(anchor, excerpt, s.addComment))
		case "C":
			return s, pushScreen(newCommentComposer(nil, "", s.addComment))
		case "t":
			// Needs the detail (metadata) and a rendered diff line; other
			// fetches (commits, approvals) may still be in flight.
			if s.deps.Chatter == nil || s.detail == nil {
				return s, nil
			}
			ref, excerpt, ok := s.cursorLine()
			if !ok {
				return s, nil
			}
			focus := &review.ChatFocus{File: s.diffs[s.fileIdx].NewPath, Line: ref}
			return s, pushScreen(newChatScreen(s.deps, *s.detail, s.diffs, focus, excerpt))
		case "T":
			if s.deps.Chatter == nil || s.detail == nil {
				return s, nil
			}
			return s, pushScreen(newChatScreen(s.deps, *s.detail, s.diffs, nil, ""))
		case "P":
			pending := s.pendingComments()
			if len(pending) == 0 || s.detail == nil {
				return s, nil
			}
			return s, pushScreen(newPublish(s.deps, *s.detail, s.diffs, pending,
				publishOpts{popCount: 1, report: s.setCommentState}))
		}
	}

	var cmd tea.Cmd
	s.vp, cmd = s.vp.Update(msg)
	return s, cmd
}

// cursorLine resolves the selected diff line into a LineRef plus its
// rendered excerpt; ok is false when no commentable line is selected.
func (s *mrDetail) cursorLine() (line review.LineRef, excerpt string, ok bool) {
	if s.cursor < 0 || s.cursor >= len(s.lineRefs) || !s.lineRefs[s.cursor].commentable() {
		return line, "", false
	}
	ref := s.lineRefs[s.cursor]
	if ref.old > 0 {
		old := ref.old
		line.OldLine = &old
	}
	if ref.new > 0 {
		newL := ref.new
		line.NewLine = &newL
	}
	if r, cached := s.rendered[s.fileIdx]; cached && s.cursor < len(r.lines) {
		excerpt = r.lines[s.cursor]
	}
	return line, excerpt, true
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
		// Two cells are reserved for the cursor gutter added in refresh.
		content, hunks, refs := renderDiff(s.diffs[s.fileIdx], s.allDiscussions(s.fileIdx), max(s.mainWidth()-2, 58), s.split)
		r = renderedDiff{lines: strings.Split(content, "\n"), hunks: hunks, refs: refs}
		s.rendered[s.fileIdx] = r
	}
	s.hunkLines = r.hunks
	s.lineRefs = r.refs
	if s.tree != nil {
		s.tree.reveal(s.fileIdx)
	}
	if keepOffset {
		s.snapCursor(min(s.cursor, len(s.lineRefs)-1))
	} else {
		s.snapCursor(0)
	}
	s.refresh()
	if keepOffset {
		s.vp.SetYOffset(offset)
	} else {
		s.vp.GotoTop()
	}
}

// allDiscussions merges the MR's fetched discussions with synthetic threads
// for manual comments on the given file, so a comment shows up in the diff
// the moment it is written.
func (s *mrDetail) allDiscussions(fileIdx int) []gitlabx.Discussion {
	fd := s.diffs[fileIdx]
	out := s.discussions
	for _, c := range s.comments {
		if c.File == "" || (c.File != fd.NewPath && c.File != fd.OldPath) {
			continue
		}
		author := "you (pending)"
		if c.State == review.StatePublished || c.State == review.StateFellBack {
			author = "you"
		}
		out = append(out, gitlabx.Discussion{
			ID: c.ID,
			Notes: []gitlabx.Note{{
				Author: author,
				Body:   c.Body,
				Position: &gitlabx.Position{
					OldPath: fd.OldPath,
					NewPath: fd.NewPath,
					OldLine: c.Line.OldLine,
					NewLine: c.Line.NewLine,
				},
			}},
		})
	}
	return out
}

// refresh rebuilds the viewport content with the cursor gutter; the cached
// render itself is untouched. The overview owns the viewport while it is
// open, so async re-renders must not clobber it.
func (s *mrDetail) refresh() {
	if s.overview {
		return
	}
	r, ok := s.rendered[s.fileIdx]
	if !ok {
		return
	}
	var b strings.Builder
	for i, line := range r.lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		if i == s.cursor {
			b.WriteString(cursorGutterStyle.Render("▌") + " ")
		} else {
			b.WriteString("  ")
		}
		b.WriteString(line)
	}
	s.vp.SetContent(b.String())
}

// snapCursor puts the cursor on the nearest commentable line at or after
// from, falling back to earlier lines, or -1 when the file has none.
func (s *mrDetail) snapCursor(from int) {
	from = max(from, 0)
	for i := from; i < len(s.lineRefs); i++ {
		if s.lineRefs[i].commentable() {
			s.cursor = i
			return
		}
	}
	for i := min(from, len(s.lineRefs)-1); i >= 0; i-- {
		if s.lineRefs[i].commentable() {
			s.cursor = i
			return
		}
	}
	s.cursor = -1
}

// moveCursor advances the cursor to the next commentable line in dir,
// scrolling the viewport to keep it visible.
func (s *mrDetail) moveCursor(dir int) {
	if s.cursor < 0 {
		return
	}
	for i := s.cursor + dir; i >= 0 && i < len(s.lineRefs); i += dir {
		if s.lineRefs[i].commentable() {
			s.cursor = i
			s.refresh()
			s.scrollToCursor()
			return
		}
	}
}

func (s *mrDetail) scrollToCursor() {
	if s.cursor < 0 {
		return
	}
	if s.cursor < s.vp.YOffset() {
		s.vp.SetYOffset(s.cursor)
	} else if bottom := s.vp.YOffset() + s.vp.Height() - 1; s.cursor > bottom {
		s.vp.SetYOffset(s.cursor - s.vp.Height() + 1)
	}
}

// invalidateRender drops cached renders after inputs change (new
// discussions, resize affecting thread wrapping).
func (s *mrDetail) invalidateRender() {
	s.rendered = nil
}

// addComment stores a manual comment composed in this view and re-renders
// so it shows inline immediately. Runs on the UI goroutine.
func (s *mrDetail) addComment(f review.Finding) {
	s.comments = append(s.comments, f)
	s.invalidateRender()
	s.setFile(s.fileIdx)
}

// pendingComments are the manual comments not yet posted to GitLab.
func (s *mrDetail) pendingComments() []review.Finding {
	var out []review.Finding
	for _, c := range s.comments {
		if c.State == review.StateAccepted {
			out = append(out, c)
		}
	}
	return out
}

// setCommentState records a publish outcome for a manual comment; reported
// by the publish screen directly, or forwarded by the findings screen when
// the comment was published alongside a review. Runs on the UI goroutine.
func (s *mrDetail) setCommentState(id string, state review.FindingState) {
	for i := range s.comments {
		if s.comments[i].ID == id {
			s.comments[i].State = state
			s.invalidateRender()
			s.setFile(s.fileIdx)
			return
		}
	}
}

func (s *mrDetail) jumpHunk(dir int) {
	if len(s.hunkLines) == 0 {
		return
	}
	jump := func(line int) {
		s.snapCursor(line)
		s.refresh()
		s.vp.SetYOffset(line)
	}
	cur := s.vp.YOffset()
	if dir > 0 {
		for _, l := range s.hunkLines {
			if l > cur {
				jump(l)
				return
			}
		}
	} else {
		for i := len(s.hunkLines) - 1; i >= 0; i-- {
			if s.hunkLines[i] < cur {
				jump(s.hunkLines[i])
				return
			}
		}
		s.vp.GotoTop()
		s.snapCursor(0)
		s.refresh()
	}
}

func (s *mrDetail) header() string {
	var b strings.Builder
	state := s.mr.State
	if s.mr.Draft {
		state += " · " + draftStyle.Render("draft")
	}
	fmt.Fprintf(&b, "%s  %s\n", headerStyle.Render(truncate(s.mr.Title, max(s.mainWidth()-20, 20))), subtleStyle.Render(state))
	fmt.Fprintf(&b, "%s → %s · %s", s.mr.SourceBranch, s.mr.TargetBranch, s.mr.AuthorDisplay())
	if s.detail != nil && s.detail.HasConflicts {
		b.WriteString(" · " + errorStyle.Render("has conflicts"))
	} else if s.detail != nil && s.detail.NeedsRebase() {
		b.WriteString(" · " + draftStyle.Render("needs rebase"))
	}
	b.WriteString(s.approvalStatus())
	b.WriteByte('\n')
	b.WriteString(subtleStyle.Render(truncate(s.mr.WebURL, max(s.width-2, 20))) + "\n")
	if s.overview {
		b.WriteString(subtleStyle.Render("overview — description & commits") + "\n")
	} else if len(s.diffs) > 0 {
		info := fmt.Sprintf("file %d/%d · %s", s.fileIdx+1, len(s.diffs), truncate(s.diffs[s.fileIdx].Path(), max(s.mainWidth()-14, 20)))
		if pending := len(s.pendingComments()); pending > 0 {
			info += fmt.Sprintf(" · %d pending comment(s)", pending)
		}
		fmt.Fprintf(&b, "%s\n", subtleStyle.Render(info))
	} else if s.loading > 0 {
		fmt.Fprintf(&b, "%s loading…\n", s.spin.View())
	} else {
		b.WriteString(subtleStyle.Render("no changes") + "\n")
	}
	return b.String()
}

// overviewContent renders the MR description and commit list shown when the
// overview is toggled over the diff (the same metadata the GUI's MR detail
// page shows).
func (s *mrDetail) overviewContent() string {
	width := max(s.mainWidth()-2, 40)
	desc := s.mr.Description
	if s.detail != nil {
		desc = s.detail.Description
	}
	var b strings.Builder
	b.WriteString(headerStyle.Render("Description") + "\n")
	if desc = strings.TrimSpace(desc); desc != "" {
		b.WriteString(wrap(desc, width) + "\n")
	} else {
		b.WriteString(subtleStyle.Render("no description") + "\n")
	}
	b.WriteByte('\n')
	b.WriteString(headerStyle.Render("Commits") + "\n")
	if len(s.commits) == 0 {
		b.WriteString(subtleStyle.Render("none loaded"))
	}
	for _, c := range s.commits {
		b.WriteString(fileStyle.Render(c.ShortID) + " " + truncate(c.Title, max(width-10, 20)) + "\n")
	}
	return b.String()
}

// approvalStatus renders the approval segment of the header's branch line:
// in-flight progress, the last action's failure, or who has approved.
func (s *mrDetail) approvalStatus() string {
	switch {
	case s.approvalBusy:
		return " · " + subtleStyle.Render("updating approval…")
	case s.approvalErr != nil:
		return " · " + errorStyle.Render(truncate("approval failed: "+s.approvalErr.Error(), 60))
	case s.gateNotice != "":
		return " · " + errorStyle.Render(truncate(s.gateNotice, 80))
	case s.approvals != nil && s.approvals.UserHasApproved:
		others := len(s.approvals.ApprovedBy) - 1
		status := "✓ approved by you"
		if others > 0 {
			status += fmt.Sprintf(" +%d", others)
		}
		return " · " + addedStyle.Render(status)
	case s.approvals != nil && len(s.approvals.ApprovedBy) > 0:
		return " · " + addedStyle.Render(truncate("✓ approved by "+strings.Join(s.approvals.ApprovedBy, ", "), 60))
	default:
		return ""
	}
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
