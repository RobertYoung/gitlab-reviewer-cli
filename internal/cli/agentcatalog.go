package cli

import (
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/agents"
)

// newAgentCatalog builds the user-scope agent catalog for cfg: builtins,
// then agents from the Claude Code plugins accepted in
// review.claude_plugins, then the user agent directories. Plugin discovery
// problems surface as catalog warnings, like definition-load problems.
func newAgentCatalog(cfg config.Config) *agents.Catalog {
	pluginDirs, warns := agents.PluginAgentDirs(config.DefaultClaudePluginsManifest(), cfg.Review.ClaudePlugins)
	return agents.NewCatalog(pluginDirs, config.UserAgentDirs()).WithWarnings(warns...)
}
