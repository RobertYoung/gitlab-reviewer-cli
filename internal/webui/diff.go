package webui

import (
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

// chromaStyleName is the syntax theme; its stylesheet is generated at
// startup and served as /static/chroma.css.
const chromaStyleName = "github-dark"

// inlineComment is a comment rendered under a diff line: an existing GitLab
// discussion note, or a pending manual comment composed in this session.
type inlineComment struct {
	ID      string // pending-comment ID; empty for GitLab notes
	Author  string
	Body    string
	When    time.Time
	State   string // pending comments: accepted/published/note
	Pending bool
}

// diffLine is one rendered row of a unified diff.
type diffLine struct {
	Kind     string // add | del | ctx | hunk
	Old, New int    // 1-based; 0 when not applicable
	HTML     template.HTML
	Comments []inlineComment
}

// CanComment reports whether a manual comment can anchor on this line.
func (l diffLine) CanComment() bool { return l.Kind != "hunk" }

// diffFile is one file of the MR diff, parsed and highlighted for HTML.
type diffFile struct {
	Index    int
	Path     string // display path (old → new when renamed)
	NewPath  string
	Status   string // added | deleted | renamed | modified
	Lines    []diffLine
	Comments int // anchored comment count, for the file explorer badge
	TooLarge bool
}

// Letter is the file explorer's status glyph.
func (f diffFile) Letter() string {
	switch f.Status {
	case "added":
		return "A"
	case "deleted":
		return "D"
	case "renamed":
		return "R"
	default:
		return "M"
	}
}

// commentKey addresses a diff line for anchoring comments: side is "new"
// for added/context lines and "old" for removed ones, matching how
// positions resolve when publishing.
func commentKey(path, side string, line int) string {
	return fmt.Sprintf("%s\x00%s\x00%d", path, side, line)
}

// buildDiffFiles parses and highlights every file diff and attaches the
// anchored GitLab discussions plus this session's pending manual comments.
func buildDiffFiles(diffs []gitlabx.FileDiff, discussions []gitlabx.Discussion, pending []review.Finding) []diffFile {
	anchored := anchorComments(discussions, pending)
	files := make([]diffFile, 0, len(diffs))
	for i, fd := range diffs {
		f := diffFile{
			Index:    i,
			Path:     fd.Path(),
			NewPath:  fd.NewPath,
			Status:   fileStatus(fd),
			TooLarge: fd.TooLarge,
		}
		f.Lines = parseDiffLines(fd, anchored)
		for _, l := range f.Lines {
			f.Comments += len(l.Comments)
		}
		files = append(files, f)
	}
	return files
}

func fileStatus(fd gitlabx.FileDiff) string {
	switch {
	case fd.NewFile:
		return "added"
	case fd.DeletedFile:
		return "deleted"
	case fd.RenamedFile:
		return "renamed"
	default:
		return "modified"
	}
}

// anchorComments indexes discussions and pending comments by file line.
func anchorComments(discussions []gitlabx.Discussion, pending []review.Finding) map[string][]inlineComment {
	out := map[string][]inlineComment{}
	for _, d := range discussions {
		pos := d.Anchor()
		if pos == nil {
			continue
		}
		var key string
		switch {
		case pos.NewLine != nil:
			key = commentKey(pos.NewPath, "new", *pos.NewLine)
		case pos.OldLine != nil:
			key = commentKey(pos.OldPath, "old", *pos.OldLine)
		default:
			continue
		}
		for _, n := range d.Notes {
			if n.System {
				continue
			}
			out[key] = append(out[key], inlineComment{Author: n.Author, Body: n.Body, When: n.CreatedAt})
		}
	}
	for _, f := range pending {
		if f.File == "" {
			continue
		}
		var key string
		switch {
		case f.Line.NewLine != nil:
			key = commentKey(f.File, "new", *f.Line.NewLine)
		case f.Line.OldLine != nil:
			key = commentKey(f.File, "old", *f.Line.OldLine)
		default:
			continue
		}
		out[key] = append(out[key], inlineComment{ID: f.ID, Body: f.Body, State: f.State.String(), Pending: true})
	}
	return out
}

// parseDiffLines walks one unified diff, tracking old/new line numbers the
// same way the TUI and position resolution do.
func parseDiffLines(fd gitlabx.FileDiff, anchored map[string][]inlineComment) []diffLine {
	if strings.TrimSpace(fd.Diff) == "" {
		return nil
	}
	h := newLineHighlighter(fd.NewPath)
	var lines []diffLine
	oldLine, newLine := 0, 0
	for _, raw := range strings.Split(strings.TrimSuffix(fd.Diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(raw, "@@"):
			if o, n, ok := parseHunkHeader(raw); ok {
				oldLine, newLine = o, n
			}
			lines = append(lines, diffLine{Kind: "hunk", HTML: template.HTML(template.HTMLEscapeString(raw))}) //nolint:gosec // escaped
		case strings.HasPrefix(raw, "+"):
			l := diffLine{Kind: "add", New: newLine, HTML: h.line(raw[1:])}
			l.Comments = anchored[commentKey(fd.NewPath, "new", newLine)]
			lines = append(lines, l)
			newLine++
		case strings.HasPrefix(raw, "-"):
			l := diffLine{Kind: "del", Old: oldLine, HTML: h.line(strings.TrimPrefix(raw, "-"))}
			l.Comments = anchored[commentKey(fd.OldPath, "old", oldLine)]
			lines = append(lines, l)
			oldLine++
		case strings.HasPrefix(raw, `\`):
			lines = append(lines, diffLine{Kind: "hunk", HTML: template.HTML(template.HTMLEscapeString(raw))}) //nolint:gosec // escaped
		default:
			l := diffLine{Kind: "ctx", Old: oldLine, New: newLine, HTML: h.line(strings.TrimPrefix(raw, " "))}
			l.Comments = anchored[commentKey(fd.NewPath, "new", newLine)]
			lines = append(lines, l)
			oldLine++
			newLine++
		}
	}
	return lines
}

// parseHunkHeader extracts starting line numbers from "@@ -a,b +c,d @@".
func parseHunkHeader(line string) (oldStart, newStart int, ok bool) {
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

// lineHighlighter colours one file's source lines for HTML. Lines are
// tokenised individually — multi-line constructs (block comments, raw
// strings) may lose state across lines, the usual trade-off in diff viewers.
type lineHighlighter struct {
	lexer     chroma.Lexer
	formatter *chromahtml.Formatter
	style     *chroma.Style
}

func newLineHighlighter(filename string) *lineHighlighter {
	lexer := lexers.Match(filename)
	if lexer == nil {
		return nil
	}
	return &lineHighlighter{
		lexer:     chroma.Coalesce(lexer),
		formatter: chromahtml.New(chromahtml.WithClasses(true), chromahtml.PreventSurroundingPre(true)),
		style:     chromastyles.Get(chromaStyleName),
	}
}

// line returns the syntax-highlighted rendering of one source line, or the
// escaped input if highlighting is unavailable.
func (h *lineHighlighter) line(code string) template.HTML {
	if h == nil || code == "" {
		return template.HTML(template.HTMLEscapeString(code)) //nolint:gosec // escaped
	}
	iter, err := h.lexer.Tokenise(nil, code)
	if err != nil {
		return template.HTML(template.HTMLEscapeString(code)) //nolint:gosec // escaped
	}
	var b strings.Builder
	if err := h.formatter.Format(&b, h.style, iter); err != nil {
		return template.HTML(template.HTMLEscapeString(code)) //nolint:gosec // escaped
	}
	return template.HTML(strings.TrimSuffix(b.String(), "\n")) //nolint:gosec // chroma escapes token text
}

// hunkExcerptHTML renders the diff lines around a finding's location so a
// suggestion can be judged in context on the findings page.
func hunkExcerptHTML(diffs []gitlabx.FileDiff, f review.Finding, radius int) []diffLine {
	var fd *gitlabx.FileDiff
	for i := range diffs {
		if diffs[i].NewPath == f.File || diffs[i].OldPath == f.File {
			fd = &diffs[i]
			break
		}
	}
	if fd == nil {
		return nil
	}
	lines := parseDiffLines(*fd, nil)
	target := -1
	for i, l := range lines {
		switch {
		case f.Line.NewLine != nil && l.New == *f.Line.NewLine && l.Kind != "del":
			target = i
		case f.Line.NewLine == nil && f.Line.OldLine != nil && l.Old == *f.Line.OldLine && l.Kind == "del":
			target = i
		}
		if target == i {
			break
		}
	}
	if target < 0 {
		return nil
	}
	from := max(target-radius, 0)
	to := min(target+radius+1, len(lines))
	return lines[from:to]
}
