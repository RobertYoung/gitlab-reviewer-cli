package config

import (
	"strings"
	"testing"
)

func TestLoadMCPServers(t *testing.T) {
	file := writeFile(t, `
review:
  mcp_servers:
    aws-documentation:
      command: uvx
      args: [awslabs.aws-documentation-mcp-server@latest]
      env:
        AWS_DOCUMENTATION_PARTITION: aws
      tools: [search_documentation, read_documentation]
projects:
  mygroup/iam-roles:
    review:
      mcp_servers:
        corp-docs:
          type: http
          url: https://docs.corp.example/mcp
          headers:
            Authorization: Bearer s3cret
`)
	res, err := Load(Options{File: file, LookupEnv: envLookup(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Config.Validate(); err != nil {
		t.Fatalf("config must validate: %v", err)
	}

	s, ok := res.Config.Review.MCPServers["aws-documentation"]
	if !ok {
		t.Fatalf("aws-documentation missing: %+v", res.Config.Review.MCPServers)
	}
	if s.Command != "uvx" || len(s.Args) != 1 || s.Env["AWS_DOCUMENTATION_PARTITION"] != "aws" {
		t.Errorf("server = %+v", s)
	}
	if len(s.Tools) != 2 || s.Tools[0] != "search_documentation" {
		t.Errorf("tools = %v", s.Tools)
	}

	// per-project sections can grant additional servers for that project only
	over, err := res.ForProject("mygroup/iam-roles")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := over.Review.MCPServers["aws-documentation"]; !ok {
		t.Error("base server lost in project override")
	}
	if got := over.Review.MCPServers["corp-docs"]; got.URL != "https://docs.corp.example/mcp" {
		t.Errorf("project-granted server = %+v", got)
	}
	if _, ok := res.Config.Review.MCPServers["corp-docs"]; ok {
		t.Error("project-scoped server leaked into the base config")
	}
}

func TestValidateMCPServers(t *testing.T) {
	base := func() Config { return Default() }

	t.Run("valid stdio and http", func(t *testing.T) {
		cfg := base()
		cfg.Review.MCPServers = map[string]MCPServer{
			"aws-documentation": {Command: "uvx", Args: []string{"pkg"}},
			"remote":            {Type: "http", URL: "https://example.com/mcp"},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("want valid, got %v", err)
		}
	})

	t.Run("rejects bad definitions", func(t *testing.T) {
		cfg := base()
		cfg.Review.MCPServers = map[string]MCPServer{
			"bad name!":  {Command: "uvx"},
			"empty":      {},
			"both":       {Command: "uvx", URL: "https://x.example"},
			"badtype":    {Command: "uvx", Type: "websocket"},
			"typeurl":    {Command: "uvx", Type: "http"},
			"badurl":     {URL: "not-a-url"},
			"gitlab-env": {Command: "uvx", Env: map[string]string{"GITLAB_TOKEN": "x"}},
		}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected validation errors")
		}
		for _, want := range []string{
			`name "bad name!"`,
			"mcp_servers.empty: needs command",
			"mcp_servers.both: command and url are mutually exclusive",
			`mcp_servers.badtype.type: "websocket"`,
			"mcp_servers.typeurl: type http requires url",
			"mcp_servers.badurl.url",
			"mcp_servers.gitlab-env.env: GITLAB_TOKEN",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error missing %q:\n%v", want, err)
			}
		}
	})
}

func TestRedactedHidesMCPHeaders(t *testing.T) {
	file := writeFile(t, `
review:
  mcp_servers:
    remote:
      type: http
      url: https://docs.corp.example/mcp
      headers:
        Authorization: Bearer s3cret
projects:
  mygroup/iam-roles:
    review:
      mcp_servers:
        remote2:
          type: http
          url: https://other.corp.example/mcp
          headers:
            X-Api-Key: also-s3cret
`)
	res, err := Load(Options{File: file, LookupEnv: envLookup(nil)})
	if err != nil {
		t.Fatal(err)
	}
	var flat strings.Builder
	dump(&flat, res.Redacted())
	if s := flat.String(); strings.Contains(s, "s3cret") {
		t.Errorf("header secret visible in redacted config:\n%s", s)
	}
	// the loaded config keeps the real header for the subprocess
	if got := res.Config.Review.MCPServers["remote"].Headers["Authorization"]; got != "Bearer s3cret" {
		t.Errorf("config header clobbered: %q", got)
	}
}

// dump flattens a raw config tree into text for containment checks.
func dump(b *strings.Builder, node map[string]any) {
	for k, v := range node {
		if m, ok := v.(map[string]any); ok {
			dump(b, m)
			continue
		}
		b.WriteString(k)
		b.WriteString("=")
		if s, ok := v.(string); ok {
			b.WriteString(s)
		}
		b.WriteString("\n")
	}
}
