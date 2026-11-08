// Package config loads the agent configuration from a YAML file, environment
// variables and command line flags. Precedence: flags > environment > file.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// TunnelPath is appended to the instance URL to reach the tunnel endpoint.
const TunnelPath = "/api/v1/connectors/tunnel"

// Config is the resolved agent configuration.
type Config struct {
	URL            string   `yaml:"url"`
	Token          string   `yaml:"token"`
	TokenFile      string   `yaml:"token_file"`
	Name           string   `yaml:"name"`
	Targets        []string `yaml:"targets"`
	CAFile         string   `yaml:"ca_file"`
	LogFormat      string   `yaml:"log_format"`
	LogLevel       string   `yaml:"log_level"`
	DialTimeout    Duration `yaml:"dial_timeout"`
	MaxStreams     int      `yaml:"max_streams"`
	HeartbeatEvery Duration `yaml:"heartbeat_interval"`
}

// Duration wraps time.Duration so it can be read from YAML as "10s".
type Duration time.Duration

// UnmarshalYAML parses a duration string.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// Std returns the standard library duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

func defaults() Config {
	return Config{
		LogFormat:      "text",
		LogLevel:       "info",
		DialTimeout:    Duration(10 * time.Second),
		MaxStreams:     256,
		HeartbeatEvery: Duration(30 * time.Second),
	}
}

// Load resolves the configuration for the given arguments (without the
// subcommand) and environment lookup function.
func Load(args []string, getenv func(string) string) (Config, error) {
	fs := flag.NewFlagSet("dsui-agent", flag.ContinueOnError)
	var (
		file      = fs.String("config", getenv("DSUI_CONFIG"), "path to a YAML config file")
		urlFlag   = fs.String("url", "", "DSUI instance URL, for example https://dsui.example.com")
		tokenFlag = fs.String("token", "", "agent token (prefer --token-file or DSUI_TOKEN)")
		tokenFile = fs.String("token-file", "", "file containing the agent token")
		name      = fs.String("name", "", "display name shown in DSUI")
		targets   = fs.String("targets", "", "comma separated allowlist, host:port")
		caFile    = fs.String("ca-file", "", "extra PEM CA bundle for connecting to DSUI")
		logFormat = fs.String("log-format", "", "text or json")
		logLevel  = fs.String("log-level", "", "debug, info, warn or error")
	)
	fs.Usage = func() {}
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	cfg := defaults()
	if *file != "" {
		raw, err := os.ReadFile(*file)
		if err != nil {
			return Config{}, fmt.Errorf("read config file: %w", err)
		}
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return Config{}, fmt.Errorf("parse config file %s: %w", *file, err)
		}
	}

	applyEnv(&cfg, getenv)

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["url"] {
		cfg.URL = *urlFlag
	}
	if set["token"] {
		cfg.Token = *tokenFlag
	}
	if set["token-file"] {
		cfg.TokenFile = *tokenFile
	}
	if set["name"] {
		cfg.Name = *name
	}
	if set["targets"] {
		cfg.Targets = splitList(*targets)
	}
	if set["ca-file"] {
		cfg.CAFile = *caFile
	}
	if set["log-format"] {
		cfg.LogFormat = *logFormat
	}
	if set["log-level"] {
		cfg.LogLevel = *logLevel
	}

	if cfg.Token == "" && cfg.TokenFile != "" {
		raw, err := os.ReadFile(cfg.TokenFile)
		if err != nil {
			return Config{}, fmt.Errorf("read token file: %w", err)
		}
		cfg.Token = strings.TrimSpace(string(raw))
	}
	return cfg, cfg.Validate()
}

func applyEnv(cfg *Config, getenv func(string) string) {
	if v := getenv("DSUI_URL"); v != "" {
		cfg.URL = v
	}
	if v := getenv("DSUI_TOKEN"); v != "" {
		cfg.Token = v
	}
	if v := getenv("DSUI_TOKEN_FILE"); v != "" {
		cfg.TokenFile = v
	}
	if v := getenv("DSUI_AGENT_NAME"); v != "" {
		cfg.Name = v
	}
	if v := getenv("DSUI_TARGETS"); v != "" {
		cfg.Targets = splitList(v)
	}
	if v := getenv("DSUI_CA_FILE"); v != "" {
		cfg.CAFile = v
	}
	if v := getenv("DSUI_LOG_FORMAT"); v != "" {
		cfg.LogFormat = v
	}
	if v := getenv("DSUI_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := getenv("DSUI_MAX_STREAMS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxStreams = n
		}
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Validate checks required fields.
func (c Config) Validate() error {
	if c.URL == "" {
		return errors.New("missing DSUI URL (--url or DSUI_URL)")
	}
	if _, err := c.TunnelURL(); err != nil {
		return err
	}
	if c.Token == "" {
		return errors.New("missing agent token (--token-file, DSUI_TOKEN or DSUI_TOKEN_FILE)")
	}
	if len(c.Targets) == 0 {
		return errors.New("empty allowlist: set targets (--targets or DSUI_TARGETS), for example airflow.internal:8080")
	}
	if c.MaxStreams < 1 {
		return errors.New("max_streams must be at least 1")
	}
	switch c.LogFormat {
	case "text", "json":
	default:
		return fmt.Errorf("log format %q must be text or json", c.LogFormat)
	}
	return nil
}

// TunnelURL turns the instance URL into the WebSocket tunnel URL.
func (c Config) TunnelURL() (string, error) {
	u, err := url.Parse(strings.TrimSpace(c.URL))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid DSUI URL %q", c.URL)
	}
	switch u.Scheme {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("DSUI URL %q must start with https:// or http://", c.URL)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + TunnelPath
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}
