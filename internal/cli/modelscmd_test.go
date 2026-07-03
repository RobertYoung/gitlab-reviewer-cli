package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/secret"
)

// runModels executes `gitlab-reviewer models` against a temp settings file
// and returns the command output.
func runModels(t *testing.T, settings string, extraArgs ...string) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	root := newRoot(&state{redactor: secret.NewRedactor()})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	args := append([]string{"models", "--config", cfgPath, "--log-file", filepath.Join(dir, "log")}, extraArgs...)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("models command failed: %v\n%s", err, out.String())
	}
	return out.String()
}

func TestModelsCommand(t *testing.T) {
	t.Run("curated list with unset model", func(t *testing.T) {
		out := runModels(t, "")
		if !strings.Contains(out, "claude-opus-4-8") {
			t.Errorf("missing curated model:\n%s", out)
		}
		if !strings.Contains(out, "claude CLI's own default") {
			t.Errorf("missing unset-model note:\n%s", out)
		}
	})

	t.Run("configured model is marked", func(t *testing.T) {
		out := runModels(t, "review:\n  model: claude-sonnet-5\n")
		if !strings.Contains(out, "* claude-sonnet-5") {
			t.Errorf("current model not marked:\n%s", out)
		}
	})

	t.Run("review.models replaces the list", func(t *testing.T) {
		out := runModels(t, "review:\n  models: [team-model-a, team-model-b]\n")
		if !strings.Contains(out, "team-model-a") || !strings.Contains(out, "team-model-b") {
			t.Errorf("configured models missing:\n%s", out)
		}
		if strings.Contains(out, "claude-opus-4-8") {
			t.Errorf("curated list leaked past review.models:\n%s", out)
		}
	})

	t.Run("model outside the list is reported", func(t *testing.T) {
		out := runModels(t, "review:\n  model: something-custom\n")
		if !strings.Contains(out, "something-custom (not in the list above)") {
			t.Errorf("missing out-of-list note:\n%s", out)
		}
	})

	t.Run("bedrock provider switches the curated list", func(t *testing.T) {
		out := runModels(t, "review:\n  provider: bedrock\nbedrock:\n  region: eu-west-1\n")
		if !strings.Contains(out, "eu.anthropic.claude-sonnet-4-6") {
			t.Errorf("missing bedrock model:\n%s", out)
		}
	})
}
