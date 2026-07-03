package checkout

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
)

func TestLocalRepoDir(t *testing.T) {
	root := t.TempDir()
	clone := filepath.Join(root, "gitlab.example.com", "group", "app")
	if err := os.MkdirAll(clone, 0o750); err != nil {
		t.Fatal(err)
	}

	// Root mode resolves host/project under the git root.
	dir, ok := LocalRepoDir(config.Checkout{Mode: "root", Root: root}, "https://gitlab.example.com", "group/app")
	if !ok || dir != clone {
		t.Errorf("root mode: %q, %v", dir, ok)
	}

	// A project without a clone reports false.
	if _, ok := LocalRepoDir(config.Checkout{Mode: "root", Root: root}, "https://gitlab.example.com", "group/other"); ok {
		t.Error("missing clone must not resolve")
	}

	// Path mode uses the configured path for any project.
	dir, ok = LocalRepoDir(config.Checkout{Mode: "path", Path: clone}, "https://gitlab.example.com", "group/app")
	if !ok || dir != clone {
		t.Errorf("path mode: %q, %v", dir, ok)
	}

	// Clone mode never resolves locally.
	if _, ok := LocalRepoDir(config.Checkout{Mode: "clone"}, "https://gitlab.example.com", "group/app"); ok {
		t.Error("clone mode must not resolve")
	}
}
