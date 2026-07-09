package webui

import (
	"bytes"
	"fmt"
	"html/template"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

// chromaStyleName and chromaLightStyleName are the syntax themes for the
// dark and light UI themes; their stylesheets are generated at startup and
// served together as /static/chroma.css.
const (
	chromaStyleName      = "github-dark"
	chromaLightStyleName = "github"
)

// syntaxCSS builds the stylesheet for both syntax themes. Chroma emits bare
// token spans (PreventSurroundingPre), so its default ".chroma" scope never
// matches the rendered markup; rules are rescoped to the diff code cells,
// with the light theme's rules behind the data-theme attribute.
func syntaxCSS() ([]byte, error) {
	var out bytes.Buffer
	if err := writeSyntaxCSS(&out, chromaStyleName, ""); err != nil {
		return nil, err
	}
	if err := writeSyntaxCSS(&out, chromaLightStyleName, `:root[data-theme="light"] `); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeSyntaxCSS(out *bytes.Buffer, style, prefix string) error {
	var css bytes.Buffer
	if err := chromahtml.New(chromahtml.WithClasses(true)).WriteCSS(&css, chromastyles.Get(style)); err != nil {
		return err
	}
	for _, line := range strings.Split(css.String(), "\n") {
		i := strings.Index(line, ".chroma .")
		if i < 0 {
			continue // background and wrapper rules; app.css owns those
		}
		out.WriteString(prefix)
		out.WriteString("td.code")
		out.WriteString(strings.TrimPrefix(line[i:], ".chroma"))
		out.WriteByte('\n')
	}
	return nil
}

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

// hunkExpand describes the unchanged region a diff row can pull into view:
// the new-side line at the boundary, the old−new offset that holds across
// the region (context lines shift both sides equally), and, for upward
// expansion, the lowest new line reachable before the previous hunk. Down
// expansion (the file tail) leaves Min zero and stops at end of file.
type hunkExpand struct {
	New    int // new-side line at the boundary (first line of the hunk, or last+1 at the tail)
	Offset int // oldLine − newLine within the region
	Min    int // upward: lowest new line to reveal; 0 means unbounded (tail)
	Down   bool
}

// diffLine is one rendered row of a unified diff.
type diffLine struct {
	Kind     string // add | del | ctx | hunk
	Old, New int    // 1-based; 0 when not applicable
	HTML     template.HTML
	Threads  []inlineThread
	Findings []review.Finding // stored review findings anchored on this line
	Expand   *hunkExpand      // hunk rows with unchanged lines above them
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
	Findings int        // anchored review findings, for the file explorer badge
	TooLarge bool
	Tail     *hunkExpand // downward expander for lines past the last hunk
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
// anchored GitLab discussions, this session's pending manual comments, and
// a stored review's findings. With split set, each file also gets its
// side-by-side row pairing.
func buildDiffFiles(diffs []gitlabx.FileDiff, discussions []gitlabx.Discussion, pending, findings []review.Finding, split bool) []diffFile {
	anchored := anchorComments(discussions, pending)
	anchoredFindings := anchorFindings(findings)
	files := make([]diffFile, 0, len(diffs))
	for i, fd := range diffs {
		f := diffFile{
			Index:    i,
			Path:     fd.Path(),
			NewPath:  fd.NewPath,
			Status:   fileStatus(fd),
			TooLarge: fd.TooLarge,
		}
		f.Lines = parseDiffLines(fd, anchored, anchoredFindings)
		f.Tail = tailExpand(fd, f.Lines)
		for _, l := range f.Lines {
			for _, t := range l.Threads {
				f.Comments += len(t.Comments)
			}
			f.Findings += len(l.Findings)
		}
		if split {
			f.Rows = splitLines(f.Lines)
		}
		files = append(files, f)
	}
	return files
}

// tailExpand builds the downward expander for the region past the last
// hunk: the new-side line after the final consumed line, with the old−new
// offset the diff ends on. Returns nil when there is nothing to reveal (no
// new-side file, an oversized diff, or an empty diff).
func tailExpand(fd gitlabx.FileDiff, lines []diffLine) *hunkExpand {
	if fd.NewPath == "" || fd.NewFile || fd.DeletedFile || fd.TooLarge {
		return nil
	}
	lastOld, lastNew := 0, 0
	for _, l := range lines {
		lastOld = max(lastOld, l.Old)
		lastNew = max(lastNew, l.New)
	}
	if lastNew == 0 {
		return nil
	}
	return &hunkExpand{New: lastNew + 1, Offset: lastOld - lastNew, Down: true}
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
			t.Comments = append(t.Comments, inlineComment{Author: n.AuthorDisplay(), Body: n.Body, When: n.CreatedAt})
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

// anchorFindings indexes a stored review's line-anchored findings by diff
// line, using the same addressing as comment threads.
func anchorFindings(findings []review.Finding) map[string][]review.Finding {
	out := map[string][]review.Finding{}
	for _, f := range findings {
		if f.File == "" {
			continue
		}
		switch {
		case f.Line.NewLine != nil:
			key := commentKey(f.File, "new", *f.Line.NewLine)
			out[key] = append(out[key], f)
		case f.Line.OldLine != nil:
			key := commentKey(f.File, "old", *f.Line.OldLine)
			out[key] = append(out[key], f)
		}
	}
	return out
}

// rawDiffLine is one parsed, not yet highlighted, row of a unified diff.
type rawDiffLine struct {
	kind     string // add | del | ctx | hunk
	old, new int
	text     string // source text without the +/-/space prefix
	from, to int    // byte range emphasised as this line pair's changed span
}

// parseDiffLines walks one unified diff, tracking old/new line numbers the
// same way the TUI and position resolution do, then highlights each line
// with the changed span of paired del/add lines emphasised.
func parseDiffLines(fd gitlabx.FileDiff, anchored map[string][]inlineThread, findings map[string][]review.Finding) []diffLine {
	raw := parseRawLines(fd.Diff)
	if len(raw) == 0 {
		return nil
	}
	markLinePairs(raw)
	h := newLineHighlighter(fd.NewPath)
	// Context expansion only works when the new-side file exists to fetch
	// unchanged lines from; deleted files and oversized diffs get no
	// expanders.
	expandable := fd.NewPath != "" && !fd.NewFile && !fd.TooLarge
	lastNew := 0 // last new line consumed; the boundary for the next hunk
	lines := make([]diffLine, 0, len(raw))
	for _, rl := range raw {
		l := diffLine{Kind: rl.kind, Old: rl.old, New: rl.new}
		if rl.new > 0 {
			lastNew = rl.new
		}
		var key string
		switch rl.kind {
		case "hunk":
			l.HTML = template.HTML(template.HTMLEscapeString(rl.text)) //nolint:gosec // escaped
			if o, n, ok := parseHunkHeader(rl.text); ok && expandable && n > lastNew+1 {
				l.Expand = &hunkExpand{New: n, Offset: o - n, Min: lastNew + 1}
			}
			lines = append(lines, l)
			continue
		case "del":
			key = commentKey(fd.OldPath, "old", rl.old)
		default: // add | ctx anchor on the new side
			key = commentKey(fd.NewPath, "new", rl.new)
		}
		l.HTML = h.line(rl.text, rl.from, rl.to)
		l.Threads = anchored[key]
		l.Findings = findings[key]
		lines = append(lines, l)
	}
	return lines
}

// parseRawLines splits a unified diff into typed rows with line numbers.
func parseRawLines(diff string) []rawDiffLine {
	if strings.TrimSpace(diff) == "" {
		return nil
	}
	var lines []rawDiffLine
	oldLine, newLine := 0, 0
	for _, raw := range strings.Split(strings.TrimSuffix(diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(raw, "@@"):
			if o, n, ok := parseHunkHeader(raw); ok {
				oldLine, newLine = o, n
			}
			lines = append(lines, rawDiffLine{kind: "hunk", text: raw})
		case strings.HasPrefix(raw, "+"):
			lines = append(lines, rawDiffLine{kind: "add", new: newLine, text: raw[1:]})
			newLine++
		case strings.HasPrefix(raw, "-"):
			lines = append(lines, rawDiffLine{kind: "del", old: oldLine, text: strings.TrimPrefix(raw, "-")})
			oldLine++
		case strings.HasPrefix(raw, `\`):
			lines = append(lines, rawDiffLine{kind: "hunk", text: raw})
		default:
			lines = append(lines, rawDiffLine{kind: "ctx", old: oldLine, new: newLine, text: strings.TrimPrefix(raw, " ")})
			oldLine++
			newLine++
		}
	}
	return lines
}

// markLinePairs emphasises the changed span within paired del/add lines:
// each run of deletions aligns row-by-row against the run of additions
// that follows it (the same pairing the split layout uses), and each pair
// keeping a meaningful common prefix/suffix gets its differing middle
// marked for the word-level highlight.
func markLinePairs(lines []rawDiffLine) {
	for i := 0; i < len(lines); {
		if lines[i].kind != "del" {
			i++
			continue
		}
		start := i
		for i < len(lines) && lines[i].kind == "del" {
			i++
		}
		addStart := i
		for i < len(lines) && lines[i].kind == "add" {
			i++
		}
		for j := range min(addStart-start, i-addStart) {
			d, a := &lines[start+j], &lines[addStart+j]
			if dFrom, dTo, aFrom, aTo, ok := changedSpan(d.text, a.text); ok {
				d.from, d.to = dFrom, dTo
				a.from, a.to = aFrom, aTo
			}
		}
	}
}

// changedSpan returns the byte ranges of a and b left after trimming their
// common prefix and suffix — what actually changed between the two versions
// of a line. ok is false when the lines are equal or share too little for
// the emphasis to mean anything (whole-line emphasis is just noise).
func changedSpan(a, b string) (aFrom, aTo, bFrom, bTo int, ok bool) {
	if a == b {
		return 0, 0, 0, 0, false
	}
	p := commonPrefix(a, b)
	s := commonSuffix(a[p:], b[p:])
	// Require the shared ends to make up at least a quarter of the shorter
	// line, so unrelated replacement lines stay unmarked.
	if 4*(p+s) < min(len(a), len(b)) {
		return 0, 0, 0, 0, false
	}
	return p, len(a) - s, p, len(b) - s, true
}

// commonPrefix returns the length of the longest shared prefix of a and b,
// backed off to a rune boundary.
func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	for n > 0 && n < len(a) && !utf8.RuneStart(a[n]) {
		n--
	}
	return n
}

// commonSuffix returns the length of the longest shared suffix of a and b,
// backed off to a rune boundary.
func commonSuffix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[len(a)-1-n] == b[len(b)-1-n] {
		n++
	}
	for n > 0 && !utf8.RuneStart(a[len(a)-n]) {
		n--
	}
	return n
}

// splitCell is one side of a side-by-side diff row; Kind "" renders as the
// empty filler opposite an unpaired addition or deletion.
type splitCell struct {
	Kind     string // add | del | ctx | ""
	Num      int    // old line on the left, new line on the right
	HTML     template.HTML
	Threads  []inlineThread
	Findings []review.Finding
}

// splitRow is one row of the side-by-side layout: a hunk header spanning
// both sides, or an old/new cell pair.
type splitRow struct {
	Hunk        template.HTML // non-empty: header row, Left/Right unused
	Left, Right splitCell
	Expand      *hunkExpand // set on hunk rows with unchanged lines above them
}

// Threads merges both sides' threads; they render full-width underneath.
func (r splitRow) Threads() []inlineThread {
	if len(r.Left.Threads) == 0 {
		return r.Right.Threads
	}
	return append(append([]inlineThread{}, r.Left.Threads...), r.Right.Threads...)
}

// Findings merges both sides' findings; they render full-width underneath.
func (r splitRow) Findings() []review.Finding {
	if len(r.Left.Findings) == 0 {
		return r.Right.Findings
	}
	return append(append([]review.Finding{}, r.Left.Findings...), r.Right.Findings...)
}

// splitLines pairs a unified diff's lines side by side: context on both
// sides, and each run of deletions aligned row-by-row against the run of
// additions that follows it, the shorter side padded with empty cells.
func splitLines(lines []diffLine) []splitRow {
	var rows []splitRow
	for i := 0; i < len(lines); {
		switch l := lines[i]; l.Kind {
		case "hunk":
			rows = append(rows, splitRow{Hunk: l.HTML, Expand: l.Expand})
			i++
		case "ctx":
			rows = append(rows, splitRow{
				Left:  splitCell{Kind: "ctx", Num: l.Old, HTML: l.HTML},
				Right: splitCell{Kind: "ctx", Num: l.New, HTML: l.HTML, Threads: l.Threads, Findings: l.Findings},
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
					row.Left = splitCell{Kind: "del", Num: dels[j].Old, HTML: dels[j].HTML, Threads: dels[j].Threads, Findings: dels[j].Findings}
				}
				if j < len(adds) {
					row.Right = splitCell{Kind: "add", Num: adds[j].New, HTML: adds[j].HTML, Threads: adds[j].Threads, Findings: adds[j].Findings}
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
// escaped input if highlighting is unavailable. A non-empty [from, to) byte
// range is wrapped in the word-level change emphasis marker.
func (h *lineHighlighter) line(code string, from, to int) template.HTML {
	emph := from < to && to <= len(code)
	if h == nil || code == "" {
		return escapedLine(code, from, to, emph)
	}
	iter, err := h.lexer.Tokenise(nil, code)
	if err != nil {
		return escapedLine(code, from, to, emph)
	}
	tokens := iter.Tokens()
	// Some lexers append the newline the source line does not have; keep
	// offsets aligned with the input.
	if n := len(tokens); n > 0 {
		tokens[n-1].Value = strings.TrimSuffix(tokens[n-1].Value, "\n")
		if tokens[n-1].Value == "" {
			tokens = tokens[:n-1]
		}
	}
	if !emph {
		return template.HTML(h.format(tokens)) //nolint:gosec // chroma escapes token text
	}
	pre, mid, post := splitTokens(tokens, from, to)
	return template.HTML(h.format(pre) + `<span class="dchg">` + h.format(mid) + `</span>` + h.format(post)) //nolint:gosec // chroma escapes token text
}

// escapedLine renders one line as escaped plain text, with the optional
// change-emphasis wrapper.
func escapedLine(code string, from, to int, emph bool) template.HTML {
	if !emph {
		return template.HTML(template.HTMLEscapeString(code)) //nolint:gosec // escaped
	}
	return template.HTML(template.HTMLEscapeString(code[:from]) + //nolint:gosec // escaped
		`<span class="dchg">` + template.HTMLEscapeString(code[from:to]) + `</span>` +
		template.HTMLEscapeString(code[to:]))
}

// format renders tokens to HTML, falling back to escaped plain text.
func (h *lineHighlighter) format(tokens []chroma.Token) string {
	if len(tokens) == 0 {
		return ""
	}
	var b strings.Builder
	if err := h.formatter.Format(&b, h.style, chroma.Literator(tokens...)); err != nil {
		b.Reset()
		for _, t := range tokens {
			b.WriteString(template.HTMLEscapeString(t.Value))
		}
		return b.String()
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// splitTokens cuts a token stream at two byte offsets, splitting any token
// that straddles a boundary, so a range of the source line can be wrapped
// without breaking the highlight markup.
func splitTokens(tokens []chroma.Token, from, to int) (pre, mid, post []chroma.Token) {
	pos := 0
	for _, t := range tokens {
		start, end := pos, pos+len(t.Value)
		pos = end
		for _, seg := range []struct {
			lo, hi int
			dst    *[]chroma.Token
		}{
			{start, min(end, from), &pre},
			{max(start, from), min(end, to), &mid},
			{max(start, to), end, &post},
		} {
			if seg.hi > seg.lo {
				nt := t
				nt.Value = t.Value[seg.lo-start : seg.hi-start]
				*seg.dst = append(*seg.dst, nt)
			}
		}
	}
	return pre, mid, post
}

// ctxRow is one unchanged line revealed by expanding diff context: it
// carries both side's line numbers so it can host comments like any other
// context line.
type ctxRow struct {
	File     string
	Old, New int
	HTML     template.HTML
}

// buildContextRows renders count unchanged lines of newPath's content
// starting at new-side line startNew, mapping each to its old-side number
// via offset (oldLine = newLine + offset). It stops at end of file, so a
// request running past the last line simply returns fewer rows.
func buildContextRows(newPath string, content []byte, startNew, offset, count int) []ctxRow {
	if startNew < 1 || count < 1 {
		return nil
	}
	fileLines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	h := newLineHighlighter(newPath)
	rows := make([]ctxRow, 0, count)
	for n := startNew; n < startNew+count && n-1 < len(fileLines); n++ {
		rows = append(rows, ctxRow{
			File: newPath,
			Old:  n + offset,
			New:  n,
			HTML: h.line(fileLines[n-1], 0, 0),
		})
	}
	return rows
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
	lines := parseDiffLines(*fd, nil, nil)
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
