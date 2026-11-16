package cli

import (
	"testing"

	"github.com/spf13/pflag"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
)

// TestGlobalFlagsMatchConfigReference keeps the generated configuration
// reference honest about flags: every documented flag is registered, and
// every global flag except --config documents the setting it sets.
func TestGlobalFlagsMatchConfigReference(t *testing.T) {
	flags := New().PersistentFlags()
	documented := map[string]string{}
	for _, d := range config.Reference() {
		if d.Flag == "" {
			continue
		}
		documented[d.Flag] = d.Key
		if flags.Lookup(d.Flag) == nil {
			t.Errorf("%s: documented flag --%s is not registered", d.Key, d.Flag)
		}
	}
	flags.VisitAll(func(f *pflag.Flag) {
		if f.Name == "config" || f.Name == "help" {
			return
		}
		if _, ok := documented[f.Name]; !ok {
			t.Errorf("--%s is a global flag but no configuration option documents it", f.Name)
		}
	})
}
