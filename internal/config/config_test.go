package config

import (
	"os"
	"path/filepath"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestPrecedenceFlagsOverEnvOverFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(file, []byte("url: https://file.example\ntoken: file-token\ntargets: [a.internal:1]\nname: from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(
		[]string{"--config", file, "--name", "from-flag"},
		env(map[string]string{"DSUI_URL": "https://env.example", "DSUI_TARGETS": "b.internal:2,c.internal:3"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "from-flag" || cfg.URL != "https://env.example" || cfg.Token != "file-token" {
		t.Fatalf("unexpected precedence: %+v", cfg)
	}
	if len(cfg.Targets) != 2 || cfg.Targets[0] != "b.internal:2" {
		t.Fatalf("env targets should replace file targets: %v", cfg.Targets)
	}
}

func TestTokenFile(t *testing.T) {
	dir := t.TempDir()
	tf := filepath.Join(dir, "token")
	if err := os.WriteFile(tf, []byte("  secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(nil, env(map[string]string{"DSUI_URL": "https://x.example", "DSUI_TOKEN_FILE": tf, "DSUI_TARGETS": "a:1"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "secret" {
		t.Fatalf("token = %q", cfg.Token)
	}
}

func TestValidation(t *testing.T) {
	good := map[string]string{"DSUI_URL": "https://x.example", "DSUI_TOKEN": "t", "DSUI_TARGETS": "a:1"}
	cases := map[string]func(map[string]string){
		"missing url":     func(m map[string]string) { delete(m, "DSUI_URL") },
		"missing token":   func(m map[string]string) { delete(m, "DSUI_TOKEN") },
		"missing targets": func(m map[string]string) { delete(m, "DSUI_TARGETS") },
		"bad scheme":      func(m map[string]string) { m["DSUI_URL"] = "ftp://x.example" },
		"bad log format":  func(m map[string]string) { m["DSUI_LOG_FORMAT"] = "xml" },
	}
	for name, mutate := range cases {
		m := map[string]string{}
		for k, v := range good {
			m[k] = v
		}
		mutate(m)
		if _, err := Load(nil, env(m)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := Load(nil, env(good)); err != nil {
		t.Fatalf("good config rejected: %v", err)
	}
}

func TestTunnelURL(t *testing.T) {
	cases := map[string]string{
		"https://dsui.example.com":       "wss://dsui.example.com/api/v1/connectors/tunnel",
		"https://dsui.example.com/":      "wss://dsui.example.com/api/v1/connectors/tunnel",
		"https://dsui.example.com/dsui/": "wss://dsui.example.com/dsui/api/v1/connectors/tunnel",
		"http://localhost:3000":          "ws://localhost:3000/api/v1/connectors/tunnel",
	}
	for in, want := range cases {
		got, err := Config{URL: in}.TunnelURL()
		if err != nil || got != want {
			t.Errorf("TunnelURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestUnknownYAMLFieldRejected(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(file, []byte("url: https://x\nbogus: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load([]string{"--config", file}, env(nil)); err == nil {
		t.Fatal("unknown field must be rejected")
	}
}
