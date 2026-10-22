package webui

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// pageData wraps every rendered page: shared chrome plus the page content.
type pageData struct {
	Title     string
	Instance  string
	Host      string   // the instance's GitLab host, beside its name
	Instances []string // >1 turns the instance label into a switcher
	Version   string
	Section   string  // top-nav entry to mark current: mrs, browse or settings
	Band      *mrBand // MR header for MR-scoped pages
	Content   any
}

var tmplFuncs = template.FuncMap{
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
	"plural":        plural,
	"inc":           func(i int) int { return i + 1 },
	"triagegroups":  triageGroups,
	"cilabel":       ciLabel,
	"ciicon":        ciIcon,
	"pager": func(prev, next, prevLabel, nextLabel string) map[string]string {
		return map[string]string{"Prev": prev, "Next": next, "PrevLabel": prevLabel, "NextLabel": nextLabel}
	},
	"initials": initials,
	"hue":      hue,
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
	if data.Section == "" && data.Band != nil {
		data.Section = "mrs"
	}
	if instances := s.instanceList(); data.Instance == "" && len(instances) == 1 {
		data.Instance = instances[0] // pages outside an instance still offer its navigation
	}
	if data.Instance != "" {
		data.Host = instanceHost(s.currentConfig(), data.Instance)
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

// ciLabel and ciIcon present a GitLab pipeline status the same way on every
// page; app.js mirrors them for the MR list's lazily loaded rows.
func ciLabel(status string) string {
	switch status {
	case "success":
		return "Passed"
	case "waiting_for_resource":
		return "Waiting"
	case "":
		return ""
	}
	return strings.ToUpper(status[:1]) + strings.ReplaceAll(status[1:], "_", " ")
}

func ciIcon(status string) string {
	switch status {
	case "success":
		return "check-circle"
	case "failed":
		return "x-circle"
	case "running":
		return "sync"
	case "canceled", "canceling", "skipped":
		return "skip"
	}
	return "clock"
}

// triageGroup is one state section of the findings page's list.
type triageGroup struct {
	State string
	Label string
	Items []findingItem
}

// triageGroups sorts findings into the list's sections, every section
// present even when empty so the page script can move rows between them as
// findings are triaged. Notes that fell back count as published.
func triageGroups(items []findingItem) []triageGroup {
	groups := []triageGroup{
		{State: "pending", Label: "Pending"},
		{State: "accepted", Label: "Accepted"},
		{State: "published", Label: "Published"},
		{State: "rejected", Label: "Rejected"},
		{State: "below-threshold", Label: "Below threshold"},
	}
	for _, it := range items {
		key := it.F.State.String()
		if key == "note" {
			key = "published"
		}
		for i := range groups {
			if groups[i].State == key {
				groups[i].Items = append(groups[i].Items, it)
			}
		}
	}
	return groups
}

// instanceHost is the GitLab host an instance name points at, or "" when
// the name is unknown or its base URL does not parse.
func instanceHost(cfg config.Config, name string) string {
	base := cfg.GitLab.BaseURL
	for _, inst := range cfg.GitLab.Instances {
		if inst.Name == name {
			base = inst.BaseURL
		}
	}
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return u.Host
}

// initials is the two-letter avatar label for a person: the first letters
// of their first and last names, or of the username when no name is known.
func initials(name, username string) string {
	name, _, _ = strings.Cut(name, " (") // "Name (@user)" display strings
	words := strings.Fields(name)
	if len(words) == 0 {
		words = strings.FieldsFunc(username, func(r rune) bool { return r == '.' || r == '-' || r == '_' })
	}
	out := ""
	for i, w := range words {
		if i == 0 || i == len(words)-1 {
			out += strings.ToUpper(string([]rune(w)[:1]))
		}
	}
	return out
}

// hue picks one of the avatar tints from a stable hash of the username, so
// a person keeps their colour across pages.
func hue(username string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(username))
	return fmt.Sprintf("h%d", h.Sum32()%5+1)
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
