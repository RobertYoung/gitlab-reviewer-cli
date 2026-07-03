package webui

import (
	"fmt"
	"html/template"
	"sort"
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

// inlineThread is one collapsible discussion under a diff line: a GitLab
// discussion's notes, or a single pending manual comment.
type inlineThread struct {
	Resolved bool
	Comments []inlineComment
}

// First is the thread's opening comment, shown in the collapsed summary.
func (t inlineThread) First() inlineComment { return t.Comments[0] }

// diffLine is one rendered row of a unified diff.
type diffLine struct {
	Kind     string // add | del | ctx | hunk
	Old, New int    // 1-based; 0 when not applicable
	HTML     template.HTML
	Threads  []inlineThread
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
	Rows     []splitRow // side-by-side pairing; only built for the split layout
	Comments int        // anchored comment count, for the file explorer badge
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

// explorerNode is one entry of the file explorer tree: a directory with
// children, or a changed file. Directories render as <details> elements,
// so folding needs no script.
type explorerNode struct {
	Name     string
	File     *diffFile // nil for directories
	Children []*explorerNode
}

// buildExplorer groups the changed files into a directory tree, sorted the
// way the TUI's explorer is: directories first, then names.
func buildExplorer(files []diffFile) []*explorerNode {
	root := &explorerNode{}
	for i := range files {
		path := files[i].NewPath
		if path == "" {
			path = files[i].Path
		}
		parts := strings.Split(path, "/")
		node := root
		for _, dir := range parts[:len(parts)-1] {
			node = node.childDir(dir)
		}
		node.Children = append(node.Children, &explorerNode{Name: parts[len(parts)-1], File: &files[i]})
	}
	sortExplorer(root)
	return root.Children
}

// childDir returns the existing directory child called name, creating it on
// first use so sibling files share one node.
func (n *explorerNode) childDir(name string) *explorerNode {
	for _, c := range n.Children {
		if c.File == nil && c.Name == name {
			return c
		}
	}
	c := &explorerNode{Name: name}
	n.Children = append(n.Children, c)
	return c
}

func sortExplorer(n *explorerNode) {
	sort.SliceStable(n.Children, func(i, j int) bool {
		a, b := n.Children[i], n.Children[j]
		if (a.File == nil) != (b.File == nil) {
			return a.File == nil
		}
		return a.Name < b.Name
	})
	for _, c := range n.Children {
		if c.File == nil {
			sortExplorer(c)
		}
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
// With split set, each file also gets its side-by-side row pairing.
func buildDiffFiles(diffs []gitlabx.FileDiff, discussions []gitlabx.Discussion, pending []review.Finding, split bool) []diffFile {
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
			for _, t := range l.Threads {
				f.Comments += len(t.Comments)
			}
		}
		if split {
			f.Rows = splitLines(f.Lines)
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

// anchorComments indexes discussion threads and pending comments by file
// line. Each GitLab discussion becomes one thread, resolved when all of its
// notes are; each pending manual comment is its own (unresolved) thread.
func anchorComments(discussions []gitlabx.Discussion, pending []review.Finding) map[string][]inlineThread {
	out := map[string][]inlineThread{}
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
		t := inlineThread{Resolved: true}
		for _, n := range d.Notes {
			if n.System {
				continue
			}
			t.Comments = append(t.Comments, inlineComment{Author: n.Author, Body: n.Body, When: n.CreatedAt})
			t.Resolved = t.Resolved && n.Resolved
		}
		if len(t.Comments) == 0 {
			continue
		}
		out[key] = append(out[key], t)
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
		c := inlineComment{ID: f.ID, Body: f.Body, State: f.State.String(), Pending: true}
		out[key] = append(out[key], inlineThread{Comments: []inlineComment{c}})
	}
	return out
}

// parseDiffLines walks one unified diff, tracking old/new line numbers the
// same way the TUI and position resolution do.
func parseDiffLines(fd gitlabx.FileDiff, anchored map[string][]inlineThread) []diffLine {
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
			l.Threads = anchored[commentKey(fd.NewPath, "new", newLine)]
			lines = append(lines, l)
			newLine++
		case strings.HasPrefix(raw, "-"):
			l := diffLine{Kind: "del", Old: oldLine, HTML: h.line(strings.TrimPrefix(raw, "-"))}
			l.Threads = anchored[commentKey(fd.OldPath, "old", oldLine)]
			lines = append(lines, l)
			oldLine++
		case strings.HasPrefix(raw, `\`):
			lines = append(lines, diffLine{Kind: "hunk", HTML: template.HTML(template.HTMLEscapeString(raw))}) //nolint:gosec // escaped
		default:
			l := diffLine{Kind: "ctx", Old: oldLine, New: newLine, HTML: h.line(strings.TrimPrefix(raw, " "))}
			l.Threads = anchored[commentKey(fd.NewPath, "new", newLine)]
			lines = append(lines, l)
			oldLine++
			newLine++
		}
	}
	return lines
}

// splitCell is one side of a side-by-side diff row; Kind "" renders as the
// empty filler opposite an unpaired addition or deletion.
type splitCell struct {
	Kind    string // add | del | ctx | ""
	Num     int    // old line on the left, new line on the right
	HTML    template.HTML
	Threads []inlineThread
}

// splitRow is one row of the side-by-side layout: a hunk header spanning
// both sides, or an old/new cell pair.
type splitRow struct {
	Hunk        template.HTML // non-empty: header row, Left/Right unused
	Left, Right splitCell
}

// Threads merges both sides' threads; they render full-width underneath.
func (r splitRow) Threads() []inlineThread {
	if len(r.Left.Threads) == 0 {
		return r.Right.Threads
	}
	return append(append([]inlineThread{}, r.Left.Threads...), r.Right.Threads...)
}

// splitLines pairs a unified diff's lines side by side: context on both
// sides, and each run of deletions aligned row-by-row against the run of
// additions that follows it, the shorter side padded with empty cells.
func splitLines(lines []diffLine) []splitRow {
	var rows []splitRow
	for i := 0; i < len(lines); {
		switch l := lines[i]; l.Kind {
		case "hunk":
			rows = append(rows, splitRow{Hunk: l.HTML})
			i++
		case "ctx":
			rows = append(rows, splitRow{
				Left:  splitCell{Kind: "ctx", Num: l.Old, HTML: l.HTML},
				Right: splitCell{Kind: "ctx", Num: l.New, HTML: l.HTML, Threads: l.Threads},
			})
			i++
		default:
			var dels, adds []diffLine
			for ; i < len(lines) && lines[i].Kind == "del"; i++ {
				dels = append(dels, lines[i])
			}
			for ; i < len(lines) && lines[i].Kind == "add"; i++ {
				adds = append(adds, lines[i])
			}
			for j := range max(len(dels), len(adds)) {
				var row splitRow
				if j < len(dels) {
					row.Left = splitCell{Kind: "del", Num: dels[j].Old, HTML: dels[j].HTML, Threads: dels[j].Threads}
				}
				if j < len(adds) {
					row.Right = splitCell{Kind: "add", Num: adds[j].New, HTML: adds[j].HTML, Threads: adds[j].Threads}
				}
				rows = append(rows, row)
			}
		}
	}
	return rows
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
