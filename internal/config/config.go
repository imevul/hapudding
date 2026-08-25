package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var reservedHopHeaders = map[string]struct{}{
	"authorization":        {},
	"x-emby-authorization": {},
	"x-emby-token":         {},
	"x-mediabrowser-token": {},
}

type Config struct {
	Listen   string    `yaml:"listen"`
	Status   Status    `yaml:"status"`
	Backends []Backend `yaml:"backends"`
	Affinity Affinity  `yaml:"affinity"`
	Health   Health    `yaml:"health"`
}

type Status struct {
	Listen string `yaml:"listen"`
}

type Backend struct {
	Name      string            `yaml:"name"`
	URL       string            `yaml:"url"`
	Headers   map[string]string `yaml:"headers"`
	Host      string            `yaml:"host"`
	TLS       TLS               `yaml:"tls"`
	Timeout   time.Duration     `yaml:"timeout"`
	HealthURL string            `yaml:"health_url"`
}

type TLS struct {
	CAFile             string `yaml:"ca_file"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
	ClientCert         string `yaml:"client_cert"`
	ClientKey          string `yaml:"client_key"`
	ServerName         string `yaml:"server_name"`
}

type Affinity struct {
	Policy            string        `yaml:"policy"`
	NewClientsRequire string        `yaml:"new_clients_require"`
	Graylist          Graylist      `yaml:"graylist"`
	Store             string        `yaml:"store"`
	SQLite            SQLite        `yaml:"sqlite"`
	Postgres          Postgres      `yaml:"postgres"`
	TokenTTL          time.Duration `yaml:"token_ttl"`
	DeviceTTL         time.Duration `yaml:"device_ttl"`
	AnonTTL           time.Duration `yaml:"anon_ttl"`
}

// Graylist applies a different affinity policy to matching clients (Infuse by default).
// Clients nil means default ["Infuse"]; empty slice disables built-in names.
type Graylist struct {
	Policy       string    `yaml:"policy"`
	Clients      *[]string `yaml:"clients"`
	PathPrefixes []string  `yaml:"path_prefixes"`
}

type SQLite struct {
	Path string `yaml:"path"`
}

type Postgres struct {
	URL string `yaml:"url"`
}

type Health struct {
	Interval            time.Duration       `yaml:"interval"`
	PublicInfo          *bool               `yaml:"public_info"`
	JellyfinHealth      *bool               `yaml:"jellyfin_health"`
	PassiveAuthFailures PassiveAuthFailures `yaml:"passive_auth_failures"`
	AuthProbe           AuthProbe           `yaml:"auth_probe"`
}

type PassiveAuthFailures struct {
	Enabled   *bool         `yaml:"enabled"`
	Threshold int           `yaml:"threshold"`
	Window    time.Duration `yaml:"window"`
}

type AuthProbe struct {
	Enabled  bool   `yaml:"enabled"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	applyDefaults(&c)
	applyEnv(&c)
	if err := validate(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

func applyDefaults(c *Config) {
	if c.Listen == "" {
		c.Listen = ":8096"
	}
	if c.Status.Listen == "" {
		c.Status.Listen = "127.0.0.1:9100"
	}
	if c.Affinity.Policy == "" {
		c.Affinity.Policy = "force_reauth"
	}
	if c.Affinity.NewClientsRequire == "" {
		c.Affinity.NewClientsRequire = "healthy"
	}
	if c.Affinity.Store == "" {
		c.Affinity.Store = "sqlite"
	}
	if c.Affinity.SQLite.Path == "" {
		c.Affinity.SQLite.Path = "./data/affinity.db"
	}
	if c.Affinity.TokenTTL == 0 {
		c.Affinity.TokenTTL = 720 * time.Hour
	}
	if c.Affinity.DeviceTTL == 0 {
		c.Affinity.DeviceTTL = 720 * time.Hour
	}
	if c.Affinity.AnonTTL == 0 {
		c.Affinity.AnonTTL = 15 * time.Minute
	}
	if c.Affinity.Graylist.Policy == "" {
		c.Affinity.Graylist.Policy = "fail_closed"
	}
	if c.Health.Interval == 0 {
		c.Health.Interval = 10 * time.Second
	}
	if c.Health.PublicInfo == nil {
		t := true
		c.Health.PublicInfo = &t
	}
	if c.Health.JellyfinHealth == nil {
		t := true
		c.Health.JellyfinHealth = &t
	}
	if c.Health.PassiveAuthFailures.Enabled == nil {
		t := true
		c.Health.PassiveAuthFailures.Enabled = &t
	}
	if c.Health.PassiveAuthFailures.Threshold == 0 {
		c.Health.PassiveAuthFailures.Threshold = 3
	}
	if c.Health.PassiveAuthFailures.Window == 0 {
		c.Health.PassiveAuthFailures.Window = 60 * time.Second
	}
	for i := range c.Backends {
		if c.Backends[i].Timeout == 0 {
			c.Backends[i].Timeout = 60 * time.Second
		}
		if c.Backends[i].Headers == nil {
			c.Backends[i].Headers = map[string]string{}
		}
	}
}

func applyEnv(c *Config) {
	if v := os.Getenv("HAP_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("HAP_STATUS_LISTEN"); v != "" {
		c.Status.Listen = v
	}
	if v := os.Getenv("HAP_STORE"); v != "" {
		c.Affinity.Store = v
	}
	if v := os.Getenv("HAP_SQLITE_PATH"); v != "" {
		c.Affinity.SQLite.Path = v
	}
	if v := os.Getenv("HAP_DATABASE_URL"); v != "" {
		c.Affinity.Postgres.URL = v
	}
	if v := os.Getenv("HAP_AFFINITY_POLICY"); v != "" {
		c.Affinity.Policy = v
	}
	if v := os.Getenv("HAP_NEW_CLIENTS_REQUIRE"); v != "" {
		c.Affinity.NewClientsRequire = v
	}
	if v := os.Getenv("HAP_GRAYLIST_POLICY"); v != "" {
		c.Affinity.Graylist.Policy = v
	}
	for i := range c.Backends {
		prefix := "HAP_BACKEND_" + envName(c.Backends[i].Name) + "_HEADER_"
		for _, e := range os.Environ() {
			k, val, ok := strings.Cut(e, "=")
			if !ok || !strings.HasPrefix(k, prefix) {
				continue
			}
			hname := strings.ReplaceAll(strings.TrimPrefix(k, prefix), "_", "-")
			c.Backends[i].Headers[hname] = val
		}
	}
}

func envName(s string) string {
	s = strings.ToUpper(s)
	s = strings.ReplaceAll(s, "-", "_")
	return s
}

func validate(c *Config) error {
	if len(c.Backends) < 1 {
		return fmt.Errorf("at least one backend is required")
	}
	switch c.Affinity.Policy {
	case "force_reauth", "fail_closed", "pin_unhealthy":
	default:
		return fmt.Errorf("unknown affinity.policy %q", c.Affinity.Policy)
	}
	switch c.Affinity.NewClientsRequire {
	case "healthy", "healthy_or_degraded":
	default:
		return fmt.Errorf("unknown affinity.new_clients_require %q", c.Affinity.NewClientsRequire)
	}
	switch c.Affinity.Graylist.Policy {
	case "force_reauth", "fail_closed", "pin_unhealthy":
	default:
		return fmt.Errorf("unknown affinity.graylist.policy %q", c.Affinity.Graylist.Policy)
	}
	switch c.Affinity.Store {
	case "sqlite", "postgres":
	default:
		return fmt.Errorf("unknown affinity.store %q", c.Affinity.Store)
	}
	if c.Affinity.Store == "postgres" && c.Affinity.Postgres.URL == "" {
		return fmt.Errorf("postgres store requires HAP_DATABASE_URL or affinity.postgres.url")
	}
	seen := map[string]struct{}{}
	for _, b := range c.Backends {
		if b.Name == "" || b.URL == "" {
			return fmt.Errorf("backend name and url are required")
		}
		if _, ok := seen[b.Name]; ok {
			return fmt.Errorf("duplicate backend name %q", b.Name)
		}
		seen[b.Name] = struct{}{}
		for h := range b.Headers {
			if _, bad := reservedHopHeaders[strings.ToLower(h)]; bad {
				return fmt.Errorf("backend %q header %q is reserved (conflicts with Jellyfin auth)", b.Name, h)
			}
		}
	}
	return nil
}

func (h Health) PublicInfoEnabled() bool {
	return h.PublicInfo == nil || *h.PublicInfo
}

func (h Health) JellyfinHealthEnabled() bool {
	return h.JellyfinHealth == nil || *h.JellyfinHealth
}

func (h Health) PassiveEnabled() bool {
	return h.PassiveAuthFailures.Enabled == nil || *h.PassiveAuthFailures.Enabled
}

// ClientNames is the configured gray-list Client= substrings. Nil Clients defaults to Infuse.
func (g Graylist) ClientNames() []string {
	if g.Clients == nil {
		return []string{"Infuse"}
	}
	return *g.Clients
}

// MatchPaths is path prefixes that classify a request as gray-listed.
// /InfuseSync is implied while Infuse is in ClientNames.
func (g Graylist) MatchPaths() []string {
	out := append([]string{}, g.PathPrefixes...)
	for _, name := range g.ClientNames() {
		if strings.EqualFold(name, "Infuse") {
			out = append(out, "/InfuseSync")
			break
		}
	}
	return out
}

func (g Graylist) MatchesClient(client string) bool {
	if client == "" {
		return false
	}
	cl := strings.ToLower(client)
	for _, name := range g.ClientNames() {
		if name != "" && strings.Contains(cl, strings.ToLower(name)) {
			return true
		}
	}
	return false
}

func (g Graylist) MatchesPath(path string) bool {
	if path == "" {
		return false
	}
	p := strings.ToLower(path)
	for _, prefix := range g.MatchPaths() {
		if prefix != "" && strings.HasPrefix(p, strings.ToLower(prefix)) {
			return true
		}
	}
	return false
}
