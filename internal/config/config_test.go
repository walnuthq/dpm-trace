package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/walnuthq/dpm-trace/internal/model"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

// The walk stops at the project boundary: a config in an unrelated parent
// workspace must never inject ledger settings into a subproject.
func TestFindStopsAtProjectBoundary(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ConfigFileName), `{"ledgerUrl": "http://parent"}`)
	project := filepath.Join(root, "project")
	write(t, filepath.Join(project, ".git", "HEAD"), "ref: refs/heads/main\n")
	if err := os.MkdirAll(filepath.Join(project, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	chdir(t, filepath.Join(project, "sub"))
	if found := Find(""); found != "" {
		t.Errorf("found %q; the parent config is outside the project boundary", found)
	}
}

func TestFindWalksUpToTheBoundary(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(root, ConfigFileName), `{"ledgerUrl": "http://project"}`)
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	chdir(t, filepath.Join(root, "sub"))
	found := Find("")
	if found == "" {
		t.Fatal("expected to find the project config")
	}
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := String("", cfg, "", "ledgerUrl"); got != "http://project" {
		t.Errorf("ledgerUrl = %q", got)
	}
}

// An explicit flag beats the environment, which beats the config file.
func TestPrecedence(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".git", "HEAD"), "ref\n")
	write(t, filepath.Join(root, ConfigFileName), `{"ledgerUrl": "http://from-config"}`)
	chdir(t, root)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := String("http://from-flag", cfg, "DPM_TRACE_LEDGER_URL", "ledgerUrl"); got != "http://from-flag" {
		t.Errorf("flag should win, got %q", got)
	}
	t.Setenv("DPM_TRACE_LEDGER_URL", "http://from-env")
	if got := String("", cfg, "DPM_TRACE_LEDGER_URL", "ledgerUrl"); got != "http://from-env" {
		t.Errorf("env should beat config, got %q", got)
	}
}

// A scalar in the config becomes a single-element list, matching config_values.
func TestStringsAcceptsScalarOrList(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".git", "HEAD"), "ref\n")
	write(t, filepath.Join(root, ConfigFileName), `{"readAs": "Alice::1220ab", "darPaths": ["a.dar", "b.dar"]}`)
	chdir(t, root)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := Strings(nil, cfg, "", "readAs"); len(got) != 1 || got[0] != "Alice::1220ab" {
		t.Errorf("readAs = %v", got)
	}
	if got := Strings(nil, cfg, "", "darPaths"); len(got) != 2 {
		t.Errorf("darPaths = %v", got)
	}
}

func TestExplicitMissingConfigIsAnError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Error("an explicit missing config must be an error")
	}
}

// Precedence for list options: an explicit flag wins, then the environment,
// then the config file. Getting this backwards would let a stale config
// override what the user just typed.
func TestStringsPrecedence(t *testing.T) {
	cfg, err := model.Decode([]byte(`{"darPaths":["/from/config.dar"]}`))
	if err != nil {
		t.Fatal(err)
	}

	if got := Strings([]string{"/explicit.dar"}, cfg, "DPM_TRACE_DAR", "darPaths"); got[0] != "/explicit.dar" {
		t.Errorf("explicit lost: %v", got)
	}

	t.Setenv("DPM_TRACE_DAR", "/from/env.dar")
	if got := Strings(nil, cfg, "DPM_TRACE_DAR", "darPaths"); got[0] != "/from/env.dar" {
		t.Errorf("env lost: %v", got)
	}

	os.Unsetenv("DPM_TRACE_DAR")
	if got := Strings(nil, cfg, "DPM_TRACE_DAR", "darPaths"); got[0] != "/from/config.dar" {
		t.Errorf("config lost: %v", got)
	}

	// An unknown key and no env means no value, not an empty string entry.
	if got := Strings(nil, cfg, "DPM_TRACE_DAR", "absent"); len(got) != 0 {
		t.Errorf("got %v, want nothing", got)
	}
	// Alternate key spellings are tried in order.
	if got := Strings(nil, cfg, "", "dar_paths", "darPaths"); got[0] != "/from/config.dar" {
		t.Errorf("alternate key not tried: %v", got)
	}
}

func TestPinnedConfigReplacesDiscoveredProjectConfig(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".git", "HEAD"), "ref\n")
	write(t, filepath.Join(root, ConfigFileName), `{"token": "leak", "tokenFile": "/leak", "ledgerUrl": "http://leak"}`)
	pinned := filepath.Join(t.TempDir(), "pinned.json")
	write(t, pinned, `{"damlYaml": "daml.yaml"}`)
	chdir(t, root)
	t.Setenv(EnvConfig, pinned)

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"token", "tokenFile", "ledgerUrl"} {
		if got := String("", cfg, "", key); got != "" {
			t.Errorf("%s = %q leaked from the discovered config", key, got)
		}
	}
	if got := String("", cfg, "", "damlYaml"); got != "daml.yaml" {
		t.Errorf("damlYaml = %q, want the pinned config's value", got)
	}
}

func TestPinnedConfigThatCannotBeReadIsAnError(t *testing.T) {
	t.Setenv(EnvConfig, filepath.Join(t.TempDir(), "absent.json"))
	if _, err := Load(""); err == nil {
		t.Error("a missing DPM_TRACE_CONFIG file must be an error")
	}
}

func TestExplicitConfigBeatsPinnedConfig(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "explicit.json")
	write(t, explicit, `{"damlYaml": "explicit.yaml"}`)
	t.Setenv(EnvConfig, filepath.Join(t.TempDir(), "absent.json"))
	cfg, err := Load(explicit)
	if err != nil || String("", cfg, "", "damlYaml") != "explicit.yaml" {
		t.Errorf("cfg=%v err=%v", cfg, err)
	}
}
