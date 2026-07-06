package webui

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
)

// settingsForm returns a form pre-populated with every schema field at its
// current effective value, so a test can change one field and post a
// complete, valid form the way the browser would.
func settingsForm(cfg config.Config) url.Values {
	values := effectiveValues(cfg)
	form := url.Values{}
	for _, sec := range settingsSchema() {
		for _, f := range sec.Fields {
			raw, _ := mapGet(values, f.Key)
			switch f.Kind {
			case kindBool:
				if toBool(raw) {
					form.Set(f.Key, "on")
				}
			case kindList:
				form.Set(f.Key, strings.Join(toStringSlice(raw), "\n"))
			case kindMap:
				form.Set(f.Key, mapLines(raw))
			case kindSecret:
				// left blank: preserve
			default:
				form.Set(f.Key, toString(raw))
			}
		}
	}
	return form
}

func TestSettingsPageRenders(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})
	code, body := env.get("/settings")
	if code != http.StatusOK {
		t.Fatalf("GET /settings: got %d, want 200", code)
	}
	for _, want := range []string{
		`name="gitlab.base_url"`,
		`name="review.provider"`,
		`name="review.timeout"`,
		`name="checkout.mode"`,
		`name="log.level"`,
		"Save settings",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page missing %q", want)
		}
	}
	// The token must never be rendered into a value attribute.
	if strings.Contains(body, `value="glpat`) {
		t.Errorf("token value leaked into the page")
	}
}

func TestSettingsSaveWritesFile(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})

	form := settingsForm(env.srv.opts.BaseConfig)
	form.Set("gitlab.base_url", "https://gitlab.example.com")
	form.Set("review.timeout", "3m")
	form.Set("review.use_agents", "on")

	code, _ := env.post("/settings", form)
	if code != http.StatusOK { // PostForm follows the redirect to GET /settings
		t.Fatalf("POST /settings: got %d, want 200", code)
	}

	res, err := config.Load(config.Options{
		File:      env.srv.settingsFile(),
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatalf("reloading saved config: %v", err)
	}
	if res.Config.GitLab.BaseURL != "https://gitlab.example.com" {
		t.Errorf("base_url = %q", res.Config.GitLab.BaseURL)
	}
	if res.Config.Review.Timeout.String() != "3m0s" {
		t.Errorf("timeout = %s", res.Config.Review.Timeout)
	}
	if !res.Config.Review.UseAgents {
		t.Errorf("use_agents not saved")
	}
}

func TestSettingsSaveRejectsInvalid(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})

	form := settingsForm(env.srv.opts.BaseConfig)
	form.Set("gitlab.per_page", "999") // out of range 1..100

	code, body := env.post("/settings", form)
	if code != http.StatusBadRequest {
		t.Fatalf("POST invalid: got %d, want 400", code)
	}
	if !strings.Contains(body, "per_page") {
		t.Errorf("error page should mention per_page, got: %s", body)
	}
	// Nothing should have been written on a rejected save.
	if _, err := os.Stat(env.srv.settingsFile()); !os.IsNotExist(err) {
		t.Errorf("settings file should not exist after a rejected save (err=%v)", err)
	}
}

func TestSettingsHotReload(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})

	// Wire a reload that re-reads the file the save just wrote, the way the
	// gui command does.
	reloads := 0
	env.srv.opts.Reload = func() (config.Config, error) {
		reloads++
		res, err := config.Load(config.Options{
			File:      env.srv.settingsFile(),
			LookupEnv: func(string) (string, bool) { return "", false },
		})
		if err != nil {
			return config.Config{}, err
		}
		return res.Config, nil
	}

	// Build deps for an instance so we can prove the cache is invalidated.
	if code, _ := env.get("/i/default/"); code != http.StatusOK {
		t.Fatalf("priming deps: got %d", code)
	}
	if len(env.srv.deps) != 1 {
		t.Fatalf("expected 1 cached deps, got %d", len(env.srv.deps))
	}

	form := settingsForm(env.srv.opts.BaseConfig)
	form.Set("gitlab.base_url", "https://gitlab.hot.example")

	code, body := env.post("/settings", form)
	if code != http.StatusOK {
		t.Fatalf("POST /settings: got %d", code)
	}
	if reloads != 1 {
		t.Errorf("reload called %d times, want 1", reloads)
	}
	if !strings.Contains(body, "and applied") {
		t.Errorf("page should confirm the change was applied, got: %s", body)
	}
	// The live config now reflects the change, and the deps cache was cleared.
	if got := env.srv.currentConfig().GitLab.BaseURL; got != "https://gitlab.hot.example" {
		t.Errorf("currentConfig base_url = %q, want the new value", got)
	}
	if len(env.srv.deps) != 0 {
		t.Errorf("deps cache should be cleared after reload, got %d entries", len(env.srv.deps))
	}
	// The freshly rendered form shows the new value.
	if !strings.Contains(body, `value="https://gitlab.hot.example"`) {
		t.Errorf("settings form should show the reloaded base_url")
	}
}

func TestSettingsHotReloadFailureStillSaves(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})
	env.srv.opts.Reload = func() (config.Config, error) {
		return config.Config{}, fmt.Errorf("boom")
	}

	form := settingsForm(env.srv.opts.BaseConfig)
	form.Set("gitlab.base_url", "https://gitlab.saved.example")
	code, body := env.post("/settings", form)
	if code != http.StatusOK {
		t.Fatalf("POST /settings: got %d", code)
	}
	// Saved, but not applied: the page tells the user to restart.
	if !strings.Contains(body, "restart the GUI to apply") {
		t.Errorf("page should ask for a restart on reload failure, got: %s", body)
	}
	res, err := config.Load(config.Options{
		File:      env.srv.settingsFile(),
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.GitLab.BaseURL != "https://gitlab.saved.example" {
		t.Errorf("file should still be written despite reload failure")
	}
}

// setInstance fills one gitlab.instances row in a settings form.
func setInstance(form url.Values, i int, name, baseURL, token, tokenEnv, origName string) {
	p := fmt.Sprintf("instance.%d.", i)
	form.Set(p+"name", name)
	form.Set(p+"base_url", baseURL)
	form.Set(p+"token", token)
	form.Set(p+"token_env", tokenEnv)
	form.Set(p+"orig_name", origName)
}

// reloadFromFile wires a hot reload that re-reads the settings file, ignoring
// the environment, the way the gui command does.
func reloadFromFile(env *testEnv) {
	env.srv.opts.Reload = func() (config.Config, error) {
		res, err := config.Load(config.Options{
			File:      env.srv.settingsFile(),
			LookupEnv: func(string) (string, bool) { return "", false },
		})
		if err != nil {
			return config.Config{}, err
		}
		return res.Config, nil
	}
}

func TestSettingsInstancesRender(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()}, func(c *config.Config) {
		c.GitLab.Instances = []config.Instance{
			{Name: "work", BaseURL: "https://work.example", Token: "work-token"},
			{Name: "personal", BaseURL: "https://personal.example", TokenEnv: "PERSONAL_TOKEN"},
		}
	})
	code, body := env.get("/settings")
	if code != http.StatusOK {
		t.Fatalf("GET /settings: got %d, want 200", code)
	}
	for _, want := range []string{
		`name="instance.0.name"`, `value="work"`,
		`name="instance.0.base_url"`, `value="https://work.example"`,
		`name="instance.1.name"`, `value="personal"`,
		`name="instance.1.token_env"`, `value="PERSONAL_TOKEN"`,
		`id="add-instance"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("instances editor missing %q", want)
		}
	}
	// Instance tokens are write-only, like the top-level token.
	if strings.Contains(body, "work-token") {
		t.Errorf("instance token leaked into the page")
	}
}

func TestSettingsAddInstance(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})
	reloadFromFile(env)

	form := settingsForm(env.srv.opts.BaseConfig)
	setInstance(form, 0, "work", "https://work.example", "work-token", "", "")

	if code, _ := env.post("/settings", form); code != http.StatusOK {
		t.Fatalf("POST /settings: got %d", code)
	}

	res, err := config.Load(config.Options{
		File:      env.srv.settingsFile(),
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatalf("reloading saved config: %v", err)
	}
	if len(res.Config.GitLab.Instances) != 1 {
		t.Fatalf("instances = %+v", res.Config.GitLab.Instances)
	}
	got := res.Config.GitLab.Instances[0]
	if got.Name != "work" || got.BaseURL != "https://work.example" || got.Token != "work-token" {
		t.Errorf("saved instance = %+v", got)
	}
	// Hot reload refreshed the selectable instance set, so the new instance is
	// routable without a restart.
	if names := env.srv.instanceList(); len(names) != 1 || names[0] != "work" {
		t.Errorf("instanceList = %v, want [work]", names)
	}
}

func TestSettingsInstanceTokenPreserved(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()}, func(c *config.Config) {
		c.GitLab.Instances = []config.Instance{
			{Name: "work", BaseURL: "https://work.example", Token: "work-token"},
		}
	})
	if err := os.WriteFile(env.srv.settingsFile(),
		[]byte("gitlab:\n  instances:\n    - name: work\n      base_url: https://work.example\n      token: work-token\n"),
		0o600); err != nil {
		t.Fatal(err)
	}

	form := settingsForm(env.srv.opts.BaseConfig)
	// Same instance, blank token (unchanged), new base URL.
	setInstance(form, 0, "work", "https://work.new.example", "", "", "work")

	if code, _ := env.post("/settings", form); code != http.StatusOK {
		t.Fatalf("POST /settings: got %d", code)
	}
	data, err := os.ReadFile(env.srv.settingsFile())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "work-token") {
		t.Errorf("blank instance token should preserve the stored value; file:\n%s", data)
	}
	if !strings.Contains(string(data), "https://work.new.example") {
		t.Errorf("instance base URL was not updated; file:\n%s", data)
	}
}

func TestSettingsRemoveInstance(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()}, func(c *config.Config) {
		c.GitLab.Instances = []config.Instance{
			{Name: "work", BaseURL: "https://work.example", Token: "work-token"},
			{Name: "personal", BaseURL: "https://personal.example", Token: "personal-token"},
		}
	})
	if err := os.WriteFile(env.srv.settingsFile(),
		[]byte("gitlab:\n  instances:\n    - name: work\n      base_url: https://work.example\n      token: work-token\n    - name: personal\n      base_url: https://personal.example\n      token: personal-token\n"),
		0o600); err != nil {
		t.Fatal(err)
	}

	form := settingsForm(env.srv.opts.BaseConfig)
	setInstance(form, 0, "work", "https://work.example", "", "", "work")
	// Row 1 removed: cleared name (the browser deletes the row entirely).
	setInstance(form, 1, "", "", "", "", "personal")

	if code, _ := env.post("/settings", form); code != http.StatusOK {
		t.Fatalf("POST /settings: got %d", code)
	}
	res, err := config.Load(config.Options{
		File:      env.srv.settingsFile(),
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Config.GitLab.Instances) != 1 || res.Config.GitLab.Instances[0].Name != "work" {
		t.Fatalf("instances after removal = %+v", res.Config.GitLab.Instances)
	}
	// The surviving instance kept its preserved token.
	if res.Config.GitLab.Instances[0].Token != "work-token" {
		t.Errorf("surviving instance token = %q", res.Config.GitLab.Instances[0].Token)
	}
}

func TestSettingsInstanceRejectsDuplicate(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})

	form := settingsForm(env.srv.opts.BaseConfig)
	setInstance(form, 0, "work", "https://a.example", "t1", "", "")
	setInstance(form, 1, "work", "https://b.example", "t2", "", "")

	code, body := env.post("/settings", form)
	if code != http.StatusBadRequest {
		t.Fatalf("POST duplicate instances: got %d, want 400", code)
	}
	if !strings.Contains(body, "duplicate") {
		t.Errorf("error page should mention the duplicate name, got: %s", body)
	}
	if _, err := os.Stat(env.srv.settingsFile()); !os.IsNotExist(err) {
		t.Errorf("nothing should be written on a rejected save (err=%v)", err)
	}
}

func TestSettingsSaveKeepsToken(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()}, func(c *config.Config) {
		c.GitLab.Token = "secret-token"
	})
	// Seed a file that already holds the token.
	if err := os.WriteFile(env.srv.settingsFile(),
		[]byte("gitlab:\n  token: secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	form := settingsForm(env.srv.opts.BaseConfig) // token field left blank
	form.Set("gitlab.base_url", "https://gitlab.example.com")
	if code, _ := env.post("/settings", form); code != http.StatusOK {
		t.Fatalf("POST /settings: got %d", code)
	}

	data, err := os.ReadFile(env.srv.settingsFile())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "secret-token") {
		t.Errorf("blank token field should preserve the existing token; file:\n%s", data)
	}
}
