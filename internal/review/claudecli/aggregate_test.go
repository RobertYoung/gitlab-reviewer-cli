package claudecli

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

func TestAggregateReturnsStructuredOutput(t *testing.T) {
	reply, err := backend(t, "aggregate.jsonl").Aggregate(context.Background(), review.AggregateRequest{Findings: []byte(`[]`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reply.CostUSD != 0.03 || !strings.Contains(string(reply.Output), `"members":["s1","s2"]`) {
		t.Errorf("reply = %s, $%v", reply.Output, reply.CostUSD)
	}
}

func TestAggregateErrorKeepsCost(t *testing.T) {
	reply, err := backend(t, "error.jsonl").Aggregate(context.Background(), review.AggregateRequest{}, nil)
	if err == nil || reply == nil {
		t.Fatalf("want an error with the reply's cost, got %v / %v", reply, err)
	}
}

// The aggregation session must stay sandboxed even when the review session
// is granted MCP servers, web domains, or shell commands.
func TestBuildAggregateArgsGrantsNothing(t *testing.T) {
	b := &Backend{
		Model:           "m",
		UseAgents:       true,
		MCPServers:      map[string]config.MCPServer{"docs": {Command: "docs-mcp"}},
		AllowedDomains:  []string{"example.com"},
		AllowedCommands: []string{"npm test:*"},
	}
	args := b.buildAggregateArgs(review.AggregateRequest{MaxBudgetUSD: 0.2})
	find := func(flag string) (string, bool) {
		i := slices.Index(args, flag)
		if i < 0 || i+1 >= len(args) {
			return "", false
		}
		return args[i+1], true
	}
	if tools, ok := find("--tools"); !ok || tools != "" {
		t.Errorf("--tools = %q (present %v), want an empty list", tools, ok)
	}
	for _, flag := range []string{"--mcp-config", "--allowedTools"} {
		if _, ok := find(flag); ok {
			t.Errorf("aggregation must not get %s", flag)
		}
	}
	disallowed, _ := find("--disallowedTools")
	for _, tool := range []string{"Bash", "WebFetch", "WebSearch", "Read", "Task", "Edit"} {
		if !strings.Contains(disallowed, tool) {
			t.Errorf("%s not denied: %q", tool, disallowed)
		}
	}
	if !slices.Contains(args, "--strict-mcp-config") {
		t.Error("missing --strict-mcp-config")
	}
	if schema, _ := find("--json-schema"); schema != review.AggregateSchema {
		t.Error("missing the aggregation schema")
	}
	if budget, _ := find("--max-budget-usd"); budget != "0.2" {
		t.Errorf("budget = %q", budget)
	}
	if model, _ := find("--model"); model != "m" {
		t.Errorf("model = %q", model)
	}
}
