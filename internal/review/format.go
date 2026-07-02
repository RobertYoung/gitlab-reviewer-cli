package review

import (
	"fmt"
	"io"
	"strings"
	"text/template"
)

// AttributionFooter is appended to published comments when
// publish.attribution is enabled.
const AttributionFooter = "\n\n---\n*🤖 suggested by [gitlab-reviewer](https://github.com/RobertYoung/gitlab-reviewer-cli), reviewed and posted by a human*"

// DefaultBodyTemplate is the built-in comment layout (publish.template).
// Fields available to templates: severity, category, title, body, file.
const DefaultBodyTemplate = "**[{{.severity}} · {{.category}}] {{.title}}**\n\n{{.body}}"

var defaultBodyTmpl = template.Must(newBodyTemplate(DefaultBodyTemplate))

func newBodyTemplate(s string) (*template.Template, error) {
	return template.New("comment").Option("missingkey=error").Parse(s)
}

// ParseBodyTemplate parses a publish.template value; empty means the
// built-in layout. It trial-executes the template so unknown fields fail
// here, not at publish time.
func ParseBodyTemplate(s string) (*template.Template, error) {
	if s == "" {
		return defaultBodyTmpl, nil
	}
	tmpl, err := newBodyTemplate(s)
	if err != nil {
		return nil, fmt.Errorf("publish.template: %w", err)
	}
	if err := tmpl.Execute(io.Discard, Finding{}.templateData()); err != nil {
		return nil, fmt.Errorf("publish.template: %w", err)
	}
	return tmpl, nil
}

func (f Finding) templateData() map[string]any {
	return map[string]any{
		"severity": string(f.Severity),
		"category": string(f.Category),
		"title":    f.Title,
		"body":     f.Body,
		"file":     f.File,
	}
}

// RenderBody formats a finding as the GitLab comment body using tmpl (nil
// means the built-in layout). Suggestions become GitLab suggestion blocks
// only when anchored to a new-side line (GitLab applies suggestions to the
// commented line).
func (f Finding) RenderBody(tmpl *template.Template, attribution bool) string {
	if f.Manual {
		// A human wrote this comment: post it exactly as typed. The template
		// and attribution footer describe model-suggested findings.
		return strings.TrimSpace(f.Body)
	}
	if tmpl == nil {
		tmpl = defaultBodyTmpl
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, f.templateData()); err != nil {
		sb.Reset()
		_ = defaultBodyTmpl.Execute(&sb, f.templateData())
	}
	body := strings.TrimSpace(sb.String())
	if f.Suggestion != "" && f.Line.NewLine != nil {
		body += "\n\n```suggestion:-0+0\n" + f.Suggestion + "\n```"
	}
	if attribution {
		body += AttributionFooter
	}
	return body
}

// RenderFallbackBody formats a finding for a general MR note when no inline
// position could be resolved; blobURL may be empty.
func (f Finding) RenderFallbackBody(tmpl *template.Template, attribution bool, blobURL string) string {
	loc := f.File
	if f.Line.NewLine != nil {
		loc = fmt.Sprintf("%s:%d", f.File, *f.Line.NewLine)
	} else if f.Line.OldLine != nil {
		loc = fmt.Sprintf("%s:%d (old)", f.File, *f.Line.OldLine)
	}
	header := fmt.Sprintf("**`%s`** *(could not anchor this comment inline)*", loc)
	if blobURL != "" {
		header = fmt.Sprintf("**[`%s`](%s)** *(could not anchor this comment inline)*", loc, blobURL)
	}
	return header + "\n\n" + f.RenderBody(tmpl, attribution)
}
