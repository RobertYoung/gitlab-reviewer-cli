package tui

import (
	"strings"
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
)

func TestInstancePicker(t *testing.T) {
	instances := []config.Instance{
		{Name: "work", BaseURL: "https://gitlab.example.com"},
		{Name: "personal", BaseURL: "https://gitlab.com"},
	}

	t.Run("enter picks the highlighted instance", func(t *testing.T) {
		p := &instancePicker{instances: instances}
		p.Update(key("j"))
		p.Update(key("enter"))
		if p.choice != "personal" {
			t.Errorf("choice = %q", p.choice)
		}
	})

	t.Run("cursor stays in bounds", func(t *testing.T) {
		p := &instancePicker{instances: instances}
		p.Update(key("k"))
		p.Update(key("j"))
		p.Update(key("j"))
		p.Update(key("j"))
		if p.cursor != 1 {
			t.Errorf("cursor = %d", p.cursor)
		}
	})

	t.Run("quit leaves no choice", func(t *testing.T) {
		p := &instancePicker{instances: instances}
		p.Update(key("q"))
		if p.choice != "" {
			t.Errorf("choice = %q", p.choice)
		}
	})

	t.Run("view lists instances", func(t *testing.T) {
		p := &instancePicker{instances: instances}
		view := p.View().Content
		for _, want := range []string{"work", "https://gitlab.com", "> "} {
			if !strings.Contains(view, want) {
				t.Errorf("view missing %q:\n%s", want, view)
			}
		}
	})
}
