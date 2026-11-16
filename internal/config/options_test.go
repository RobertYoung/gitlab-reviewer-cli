package config

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
)

func TestSettingsCoverEveryKey(t *testing.T) {
	documented := map[string]bool{}
	for _, o := range Settings {
		if documented[o.Key] {
			t.Errorf("%s: documented twice", o.Key)
		}
		documented[o.Key] = true
		if strings.TrimSpace(o.Description) == "" {
			t.Errorf("%s: missing description", o.Key)
		}
	}
	leaves := LeafKeys()
	for _, key := range slices.Sorted(maps.Keys(leaves)) {
		if !documented[key] {
			t.Errorf("%s: Config field has no entry in Settings", key)
		}
	}
	for key := range documented {
		if _, ok := leaves[key]; !ok {
			t.Errorf("%s: documented but not a Config field", key)
		}
	}
	tables := map[string]map[string]string{"envToKey": envToKey, "flagToKey": flagToKey, "envFallbacks": envFallbacks}
	for name, table := range tables {
		for from, key := range table {
			if !documented[key] {
				t.Errorf("%s[%s] = %s: not a documented key", name, from, key)
			}
		}
	}
	for key := range listKeys {
		if !documented[key] {
			t.Errorf("listKeys[%s]: not a documented key", key)
		}
	}
}

func TestSettingsDefaultText(t *testing.T) {
	k := koanf.New(delim)
	if err := k.Load(structs.Provider(Default(), "koanf"), nil); err != nil {
		t.Fatal(err)
	}
	for _, o := range Settings {
		if o.DefaultText != "" && formatDefault(k.Get(o.Key)) != "" {
			t.Errorf("%s: DefaultText %q hides the real default %q", o.Key, o.DefaultText, formatDefault(k.Get(o.Key)))
		}
	}
}

// TestSettingsAllowedMatchValidate checks Allowed against Validate: the
// rejection message for an unknown value lists exactly the documented
// values, each of which passes, and a key Validate treats as an enum is
// documented as one.
func TestSettingsAllowedMatchValidate(t *testing.T) {
	const bogus = "not-a-valid-value"
	leaves := LeafKeys()
	for _, o := range Settings {
		typ := leaves[o.Key]
		if typ.Kind() != reflect.String && typ != reflect.TypeFor[[]string]() {
			continue
		}
		validate := func(v string) error {
			var val any = v
			if typ.Kind() == reflect.Slice {
				val = []string{v}
			}
			return validateWith(t, o.Key, val)
		}
		enumErr := fmt.Sprintf("%s: %q is not one of", o.Key, bogus)
		err := validate(bogus)
		if len(o.Allowed) == 0 {
			if err != nil && strings.Contains(err.Error(), enumErr) {
				t.Errorf("%s: Validate restricts the values but Settings lists no Allowed values", o.Key)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), enumErr) {
			t.Errorf("%s: %q passed validation, want it rejected (err: %v)", o.Key, bogus, err)
			continue
		}
		_, rest, _ := strings.Cut(err.Error(), enumErr+" ")
		accepted, _, _ := strings.Cut(rest, "\n")
		if got := strings.Split(accepted, "|"); !slices.Equal(got, o.Allowed) {
			t.Errorf("%s: Validate accepts %v, Settings documents %v", o.Key, got, o.Allowed)
		}
		for _, v := range o.Allowed {
			if err := validate(v); err != nil {
				t.Errorf("%s: documented value %q fails validation: %v", o.Key, v, err)
			}
		}
	}
}

func validateWith(t *testing.T, key string, val any) error {
	t.Helper()
	k := koanf.New(delim)
	if err := k.Load(structs.Provider(Default(), "koanf"), nil); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{{"checkout.path", "/repo"}, {"checkout.root", "/git"}, {"bedrock.region", "eu-west-2"}} {
		_ = k.Set(kv[0], kv[1])
	}
	_ = k.Set(key, val)
	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.Validate()
}

func TestReferenceDerivesFromCode(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	t.Setenv("XDG_CACHE_HOME", "")
	byKey := map[string]OptionDoc{}
	for _, d := range Reference() {
		byKey[d.Key] = d
	}
	for key, want := range map[string]OptionDoc{
		"gitlab.token":           {Env: "GITLAB_REVIEWER_GITLAB_TOKEN", EnvFallback: "GITLAB_TOKEN", Flag: "gitlab-token", Type: "string"},
		"review.timeout":         {Env: "GITLAB_REVIEWER_REVIEW_TIMEOUT", Flag: "review-timeout", Default: "10m", Type: "duration"},
		"checkout.clone_missing": {Env: "GITLAB_REVIEWER_CHECKOUT_CLONE_MISSING", Default: "false", Type: "boolean"},
		"checkout.cache_dir":     {Env: "GITLAB_REVIEWER_CHECKOUT_CACHE_DIR", Flag: "cache-dir", Default: "/home/u/.cache/gitlab-reviewer", Type: "path"},
		"review.mcp_servers":     {FileOnly: true, Type: "map of objects"},
		"gate.min_severity":      {Env: "GITLAB_REVIEWER_GATE_MIN_SEVERITY", Flag: "gate-min-severity", Default: "unset (gate off)", Type: "severity"},
	} {
		got := byKey[key]
		if got.Env != want.Env || got.EnvFallback != want.EnvFallback || got.Flag != want.Flag || got.Default != want.Default || got.Type != want.Type || got.FileOnly != want.FileOnly {
			t.Errorf("%s:\n got  env=%q fallback=%q flag=%q default=%q type=%q fileOnly=%v\n want env=%q fallback=%q flag=%q default=%q type=%q fileOnly=%v",
				key, got.Env, got.EnvFallback, got.Flag, got.Default, got.Type, got.FileOnly,
				want.Env, want.EnvFallback, want.Flag, want.Default, want.Type, want.FileOnly)
		}
	}
	if !byKey["review.exclude"].EnvList || byKey["review.provider"].EnvList {
		t.Error("EnvList should follow listKeys")
	}
}
