package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

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

	detail *gitlabx.MRDetail
	diffs  []gitlabx.FileDiff

	vp        viewport.Model
	spin      spinner.Model
	loading   int // outstanding requests
	err       error
	fileIdx   int
	hunkLines []int
	width     int
	height    int
}

func newMRDetail(deps Deps, mr gitlabx.MRSummary) *mrDetail {
	return &mrDetail{
		deps: deps,
		svc:  deps.Svc,
		mr:   mr,
		vp:   viewport.New(),
		spin: spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
}

func (s *mrDetail) Title() string {
	return fmt.Sprintf("%s · %s", s.mr.Ref(), truncate(s.mr.Title, 60))
}

func (s *mrDetail) Hints() string {
	return "↑/↓ scroll · n/p file · ]/[ hunk · r review · esc back · q quit"
}

func (s *mrDetail) Init() tea.Cmd {
	s.loading = 2
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
	return tea.Batch(s.spin.Tick, fetchDetail, fetchDiffs)
}

func (s *mrDetail) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
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
		s.setFile(0)
		return s, nil

	case mrDetailErrMsg:
		if msg.iid != s.mr.IID {
			return s, nil
		}
		s.loading = 0
		s.err = msg.err
		return s, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			return s, popScreen
		case "q":
			return s, tea.Quit
		case "r":
			if s.detail == nil || s.loading > 0 {
				return s, nil
			}
			return s, pushScreen(newReviewRun(s.deps, *s.detail, s.diffs))
		case "n", "right":
			s.setFile(s.fileIdx + 1)
			return s, nil
		case "p", "left":
			s.setFile(s.fileIdx - 1)
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
	s.fileIdx = (idx + len(s.diffs)) % len(s.diffs)
	content, hunks := renderDiff(s.diffs[s.fileIdx])
	s.hunkLines = hunks
	s.vp.SetContent(content)
	s.vp.GotoTop()
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
	fmt.Fprintf(&b, "%s  %s\n", headerStyle.Render(truncate(s.mr.Title, max(s.width-20, 20))), subtleStyle.Render(state))
	fmt.Fprintf(&b, "%s → %s · @%s", s.mr.SourceBranch, s.mr.TargetBranch, s.mr.Author)
	if s.detail != nil && s.detail.HasConflicts {
		b.WriteString(" · " + errorStyle.Render("has conflicts"))
	}
	b.WriteByte('\n')
	if len(s.diffs) > 0 {
		fmt.Fprintf(&b, "%s\n", subtleStyle.Render(fmt.Sprintf("file %d/%d · %s", s.fileIdx+1, len(s.diffs), truncate(s.diffs[s.fileIdx].Path(), max(s.width-14, 20)))))
	} else if s.loading > 0 {
		fmt.Fprintf(&b, "%s loading…\n", s.spin.View())
	} else {
		b.WriteString(subtleStyle.Render("no changes") + "\n")
	}
	return b.String()
}

// headerHeight is the number of lines header() renders.
const headerHeight = 3

func (s *mrDetail) layout() {
	if s.width == 0 {
		return
	}
	s.vp.SetWidth(s.width)
	s.vp.SetHeight(max(s.height-headerHeight, 1))
}

func (s *mrDetail) View() string {
	if s.err != nil {
		return s.header() + "\n" + errorStyle.Render(truncate("error: "+s.err.Error(), max(s.width*2, 20)))
	}
	return s.header() + s.vp.View()
}
