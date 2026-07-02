package review

import (
	"strings"
	"testing"
)

func intPtr(n int) *int { return &n }

func sampleFinding() Finding {
	return Finding{
		File:     "internal/app/server.go",
		Line:     LineRef{NewLine: intPtr(42)},
		Severity: SeverityMajor,
		Category: Category("design"),
		Title:    "Handler owns too much",
		Body:     "This handler builds its own client; inject it instead.",
	}
}

func TestRenderBodyDefaultTemplate(t *testing.T) {
	got := sampleFinding().RenderBody(nil, false)
	want := "**[major · design] Handler owns too much**\n\nThis handler builds its own client; inject it instead."
	if got != want {
		t.Errorf("RenderBody(nil) = %q, want %q", got, want)
	}
}

func TestRenderBodyCustomTemplate(t *testing.T) {
	tests := []struct {
		name, tmpl, want string
	}{
		{"body only", "{{.body}}", "This handler builds its own client; inject it instead."},
		{"title and body", "{{.title}} — {{.body}}", "Handler owns too much — This handler builds its own client; inject it instead."},
		{"file reference", "{{.body}} ({{.file}})", "This handler builds its own client; inject it instead. (internal/app/server.go)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpl, err := ParseBodyTemplate(tt.tmpl)
			if err != nil {
				t.Fatalf("ParseBodyTemplate(%q): %v", tt.tmpl, err)
			}
			if got := sampleFinding().RenderBody(tmpl, false); got != tt.want {
				t.Errorf("RenderBody = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseBodyTemplateEmptyIsDefault(t *testing.T) {
	tmpl, err := ParseBodyTemplate("")
	if err != nil {
		t.Fatalf("ParseBodyTemplate(\"\"): %v", err)
	}
	if got, want := sampleFinding().RenderBody(tmpl, false), sampleFinding().RenderBody(nil, false); got != want {
		t.Errorf("empty template rendered %q, default rendered %q", got, want)
	}
}

func TestParseBodyTemplateErrors(t *testing.T) {
	for _, tmpl := range []string{"{{.body", "{{.nonsense}}"} {
		if _, err := ParseBodyTemplate(tmpl); err == nil {
			t.Errorf("ParseBodyTemplate(%q): expected error", tmpl)
		}
	}
}

func TestRenderBodyKeepsSuggestionAndAttribution(t *testing.T) {
	f := sampleFinding()
	f.Suggestion = "client := s.client"
	tmpl, err := ParseBodyTemplate("{{.body}}")
	if err != nil {
		t.Fatal(err)
	}
	got := f.RenderBody(tmpl, true)
	if !strings.Contains(got, "```suggestion:-0+0\nclient := s.client\n```") {
		t.Errorf("suggestion block missing from %q", got)
	}
	if !strings.HasSuffix(got, AttributionFooter) {
		t.Errorf("attribution footer missing from %q", got)
	}
}

func TestRenderFallbackBodyUsesTemplate(t *testing.T) {
	tmpl, err := ParseBodyTemplate("{{.body}}")
	if err != nil {
		t.Fatal(err)
	}
	got := sampleFinding().RenderFallbackBody(tmpl, false, "")
	if !strings.Contains(got, "internal/app/server.go:42") {
		t.Errorf("location header missing from %q", got)
	}
	if strings.Contains(got, "[major") {
		t.Errorf("badge leaked into templated fallback body %q", got)
	}
}
