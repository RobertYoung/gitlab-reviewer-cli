package webui

import (
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
)

// pageData wraps every rendered page: shared chrome plus the page content.
type pageData struct {
	Title     string
	Instance  string
	Instances []string // >1 shows the instance name in the header
	Version   string
	Content   any
}

var tmplFuncs = template.FuncMap{
	"navargs": func(nav mrNav, webURL string) map[string]any { return map[string]any{"Nav": nav, "WebURL": webURL} },
	"commentrow": func(c inlineComment, content diffContent) map[string]any {
		return map[string]any{"C": c, "Nav": content.Nav, "BackURL": content.BackURL}
	},
	"threadrow": func(t inlineThread, content diffContent) map[string]any {
		return map[string]any{"T": t, "Content": content}
	},
	"ftitle":   findingTitle,
	"floc":     findingLocation,
	"fstate":   func(s review.FindingState) string { return s.String() },
	"reltime":  relTime,
	"join":     strings.Join,
	"datetime": func(t time.Time) string { return t.Local().Format("2006-01-02 15:04") },
	"query":    url.QueryEscape,
	"pathesc":  url.PathEscape,
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
	if len(s.instances) > 1 {
		data.Instances = s.instances
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
