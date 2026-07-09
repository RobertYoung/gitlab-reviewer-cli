package webui

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// pageData wraps every rendered page: shared chrome plus the page content.
type pageData struct {
	Title     string
	Instance  string
	Instances []string // >1 shows the instance name in the header
	Version   string
	Crumbs    []crumb // topbar breadcrumb trail; the Title alone when unset
	Content   any
}

// crumb is one step in the topbar breadcrumb trail; the current page has
// no URL and renders unlinked.
type crumb struct {
	Label string
	URL   string
}

var tmplFuncs = template.FuncMap{
	"navargs": func(nav mrNav, webURL string) map[string]any { return map[string]any{"Nav": nav, "WebURL": webURL} },
	"commentrow": func(c inlineComment, content diffContent) map[string]any {
		return map[string]any{"C": c, "Nav": content.Nav, "BackURL": content.BackURL}
	},
	"threadrow": func(t inlineThread, content diffContent) map[string]any {
		return map[string]any{"T": t, "Content": content}
	},
	"findingrow": func(f review.Finding, content diffContent) map[string]any {
		return map[string]any{
			"F": f, "Nav": content.Nav, "StateURL": content.StateURL,
			"RecordName": content.RecordName, "BackURL": content.BackURL,
			"FindingsURL": content.FindingsURL,
		}
	},
	"reviewformargs": func(nav mrNav, opts []agentOption, warnings []string, prevReviewHead string) map[string]any {
		return map[string]any{"Nav": nav, "AgentOptions": opts, "AgentWarnings": warnings, "PrevReviewHead": prevReviewHead}
	},
	"triagerow": func(f review.Finding, content findingsContent) map[string]any {
		return map[string]any{
			"F": f, "Nav": content.Nav, "StateURL": content.StateURL,
			"RecordName": content.RecordName, "BackURL": "",
		}
	},
	"blankinstance": func(index int) instanceRow { return instanceRow{Index: index} },
	"ftitle":        findingTitle,
	"floc":          findingLocation,
	"fstate":        func(s review.FindingState) string { return s.String() },
	"reltime":       relTime,
	"markdown":      renderMarkdown,
	"join":          strings.Join,
	"datetime":      func(t time.Time) string { return t.Local().Format("2006-01-02 15:04") },
	"query":         url.QueryEscape,
	"pathesc":       url.PathEscape,
}

// parseTemplates builds one template set per page, each sharing the layout
// and partials.
func parseTemplates() (map[string]*template.Template, error) {
	pageFiles, err := fs.Glob(assets, "templates/pages/*.tmpl")
	if err != nil {
		return nil, err
	}
	pages := make(map[string]*template.Template, len(pageFiles))
	for _, pf := range pageFiles {
		name := strings.TrimSuffix(path.Base(pf), ".tmpl")
		t, err := template.New("layout.tmpl").Funcs(tmplFuncs).ParseFS(assets,
			"templates/layout.tmpl", "templates/partials/*.tmpl", pf)
		if err != nil {
			return nil, fmt.Errorf("parsing template %s: %w", pf, err)
		}
		pages[name] = t
	}
	return pages, nil
}

// render writes one page. Template failures after the header is out are
// logged, not surfaced — the page is already half-written.
func (s *Server) render(w http.ResponseWriter, status int, page string, data pageData) {
	t, ok := s.pages[page]
	if !ok {
		http.Error(w, "unknown page "+page, http.StatusInternalServerError)
		return
	}
	if instances := s.instanceList(); len(instances) > 1 {
		data.Instances = instances
	}
	if len(data.Crumbs) == 0 {
		data.Crumbs = []crumb{{Label: data.Title}}
	}
	data.Version = s.opts.Version
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.Execute(w, data); err != nil {
		slog.Warn("rendering page failed", "page", page, "error", err)
	}
}

// renderError shows the shared error page.
func (s *Server) renderError(w http.ResponseWriter, status int, err error) {
	s.render(w, status, "error", pageData{
		Title:   "error",
		Content: err.Error(),
	})
}

// mdRenderer approximates GitLab-flavored markdown: GFM tables, strikethrough,
// task lists, autolinks, plus GitLab's single-newline line breaks. Raw HTML is
// omitted and dangerous URL schemes dropped (goldmark's safe defaults), so the
// output can be trusted as template.HTML.
var mdRenderer = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(html.WithHardWraps()),
)

// renderMarkdown converts untrusted markdown to HTML, falling back to
// escaped plain text if conversion fails.
func renderMarkdown(src string) template.HTML {
	var buf bytes.Buffer
	if err := mdRenderer.Convert([]byte(src), &buf); err != nil {
		return template.HTML("<pre class=\"prose\">" + template.HTMLEscapeString(src) + "</pre>") //nolint:gosec // escaped above
	}
	return template.HTML(buf.String()) //nolint:gosec // goldmark runs with raw HTML disabled
}

// findingTitle is the list label for a finding: its title, or the first
// line of the body for manual comments (which have no title).
func findingTitle(f review.Finding) string {
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
	switch {
	case f.Line.NewLine != nil:
		return fmt.Sprintf("%s:%d", f.File, *f.Line.NewLine)
	case f.Line.OldLine != nil:
		return fmt.Sprintf("%s:%d(old)", f.File, *f.Line.OldLine)
	default:
		return f.File
	}
}

// relTime renders a compact "3h ago" style timestamp for list views.
func relTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Local().Format("2006-01-02")
	}
}
