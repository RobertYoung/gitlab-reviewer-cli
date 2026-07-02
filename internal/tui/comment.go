package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

// manualSeq numbers manual comments across the whole session so their IDs
// never collide with the reviewer's fNNN findings. Only touched on the UI
// goroutine.
var manualSeq int

func nextManualID() string {
	manualSeq++
	return fmt.Sprintf("m%03d", manualSeq)
}

// commentAnchor pins a manual comment to a diff line; nil means a general
// MR-level comment.
type commentAnchor struct {
	file string
	line review.LineRef
}

func (a *commentAnchor) label() string {
	if a == nil {
		return "general MR comment"
	}
	return fmt.Sprintf("%s:%s", a.file, lineLabel(a.line))
}

// commentComposer is a pushed screen with a textarea for writing one manual
// comment. On save it builds an accepted, Manual finding and hands it to
// onSave (called on the UI goroutine), then pops itself.
type commentComposer struct {
	anchor  *commentAnchor
	excerpt string // diff context rendered above the input; may be empty
	onSave  func(review.Finding)
	ta      textarea.Model
	width   int
	height  int
}

func newCommentComposer(anchor *commentAnchor, excerpt string, onSave func(review.Finding)) *commentComposer {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	return &commentComposer{anchor: anchor, excerpt: excerpt, onSave: onSave, ta: ta}
}

func (s *commentComposer) Title() string { return "new comment · " + s.anchor.label() }

// Typing reports that the textarea captures keystrokes, so "?" stays literal.
func (s *commentComposer) Typing() bool { return true }

func (s *commentComposer) Hints() string { return "ctrl+s save · esc discard" }

func (s *commentComposer) Init() tea.Cmd { return s.ta.Focus() }

func (s *commentComposer) excerptHeight() int {
	if s.excerpt == "" {
		return 0
	}
	return strings.Count(s.excerpt, "\n") + 2
}

func (s *commentComposer) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		s.ta.SetWidth(max(s.width-2, 20))
		s.ta.SetHeight(max(s.height-s.excerptHeight()-3, 3))
		return s, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+s":
			body := strings.TrimSpace(s.ta.Value())
			if body == "" {
				return s, popScreen
			}
			f := review.Finding{
				ID:     nextManualID(),
				Body:   body,
				State:  review.StateAccepted,
				Manual: true,
			}
			if s.anchor != nil {
				f.File = s.anchor.file
				f.Line = s.anchor.line
			}
			s.onSave(f)
			return s, popScreen
		case "esc":
			return s, popScreen
		}
	}
	var cmd tea.Cmd
	s.ta, cmd = s.ta.Update(msg)
	return s, cmd
}

func (s *commentComposer) View() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("comment on "+s.anchor.label()) + "\n")
	if s.excerpt != "" {
		b.WriteString(s.excerpt + "\n")
	}
	b.WriteString(s.ta.View())
	b.WriteString("\n" + subtleStyle.Render("posted verbatim as your own comment — markdown supported"))
	return b.String()
}

// manualTitle is the list/preview label for a finding: its title, or the
// first line of the body for manual comments (which have no title).
func manualTitle(f review.Finding) string {
	if f.Title != "" {
		return f.Title
	}
	first, _, _ := strings.Cut(strings.TrimSpace(f.Body), "\n")
	return first
}

// findingLocation labels where a finding lands: file:line, or the MR itself.
func findingLocation(f review.Finding) string {
	if f.File == "" {
		return "MR (general)"
	}
	return fmt.Sprintf("%s:%s", f.File, lineLabel(f.Line))
}
