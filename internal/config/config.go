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
	Listen      string      `yaml:"listen"`
	Status      Status      `yaml:"status"`
	Backends    []Backend   `yaml:"backends"`
	Affinity    Affinity    `yaml:"affinity"`
	Health      Health      `yaml:"health"`
	Performance Performance `yaml:"performance"`
}

type Performance struct {
	AuthTimeout        time.Duration      `yaml:"auth_timeout"`
	Cache              Cache              `yaml:"cache"`
	Library            LibraryCache       `yaml:"library"`
	Coalesce           Toggle             `yaml:"coalesce"`
	WarmLogin          Toggle             `yaml:"warm_login"`
	LibraryConcurrency LibraryConcurrency `yaml:"library_concurrency"`
}

type Cache struct {
	Enabled    *bool         `yaml:"enabled"`
	MaxBytes   int64         `yaml:"max_bytes"`
	MaxObject  int64         `yaml:"max_object"`
	DefaultTTL time.Duration `yaml:"default_ttl"`
	MaxTTL     time.Duration `yaml:"max_ttl"`
}

type LibraryCache struct {
	Enabled   *bool         `yaml:"enabled"`
	TTL       time.Duration `yaml:"ttl"`
	MaxBytes  int64         `yaml:"max_bytes"`
	MaxObject int64         `yaml:"max_object"`
}

type Toggle struct {
	Enabled *bool `yaml:"enabled"`
}

type LibraryConcurrency struct {
	Enabled *bool `yaml:"enabled"`
	Max     int   `yaml:"max"`
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
	Disabled  bool              `yaml:"disabled"`
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
		c.Affinity.Store = "postgres"
	}
	if c.Affinity.SQLite.Path == "" {
		c.Affinity.SQLite.Path = "./data/affinity.db"
	}
	if c.Affinity.Store == "postgres" && c.Affinity.Postgres.URL == "" {
		c.Affinity.Postgres.URL = "postgres://hap:hap@localhost:5432/hap?sslmode=disable"
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
	if c.Performance.AuthTimeout == 0 {
		c.Performance.AuthTimeout = 60 * time.Second
	}
	if c.Performance.Cache.Enabled == nil {
		t := true
		c.Performance.Cache.Enabled = &t
	}
	if c.Performance.Cache.MaxBytes == 0 {
		c.Performance.Cache.MaxBytes = 256 << 20
	}
	if c.Performance.Cache.MaxObject == 0 {
		c.Performance.Cache.MaxObject = 2 << 20
	}
	if c.Performance.Cache.DefaultTTL == 0 {
		c.Performance.Cache.DefaultTTL = 15 * time.Minute
	}
	if c.Performance.Cache.MaxTTL == 0 {
		c.Performance.Cache.MaxTTL = 24 * time.Hour
	}
	if c.Performance.Library.Enabled == nil {
		t := true
		c.Performance.Library.Enabled = &t
	}
	if c.Performance.Library.TTL == 0 {
		c.Performance.Library.TTL = 30 * time.Second
	}
	if c.Performance.Library.MaxBytes == 0 {
		c.Performance.Library.MaxBytes = 64 << 20
	}
	if c.Performance.Library.MaxObject == 0 {
		c.Performance.Library.MaxObject = 4 << 20
	}
	if c.Performance.Coalesce.Enabled == nil {
		t := true
		c.Performance.Coalesce.Enabled = &t
	}
	if c.Performance.LibraryConcurrency.Max == 0 {
		c.Performance.LibraryConcurrency.Max = 3
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
	if v := os.Getenv("HAP_CACHE_ENABLED"); v != "" {
		t := envBool(v)
		c.Performance.Cache.Enabled = &t
	}
	if v := os.Getenv("HAP_LIBRARY_CACHE_ENABLED"); v != "" {
		t := envBool(v)
		c.Performance.Library.Enabled = &t
	}
	if v := os.Getenv("HAP_COALESCE_ENABLED"); v != "" {
		t := envBool(v)
		c.Performance.Coalesce.Enabled = &t
	}
	if v := os.Getenv("HAP_WARM_LOGIN_ENABLED"); v != "" {
		t := envBool(v)
		c.Performance.WarmLogin.Enabled = &t
	}
	if v := os.Getenv("HAP_LIBRARY_CONCURRENCY_ENABLED"); v != "" {
		t := envBool(v)
		c.Performance.LibraryConcurrency.Enabled = &t
	}
	if v := os.Getenv("HAP_AUTH_TIMEOUT"); v != "" {
		if d, ok := envDuration(v); ok {
			c.Performance.AuthTimeout = d
		}
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

func envBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envDuration(v string) (time.Duration, bool) {
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
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
	if c.Performance.CacheEnabled() && c.Performance.Cache.MaxBytes <= 0 {
		return fmt.Errorf("performance.cache.max_bytes must be > 0 when enabled")
	}
	if c.Performance.Cache.MaxObject > c.Performance.Cache.MaxBytes {
		return fmt.Errorf("performance.cache.max_object must be <= performance.cache.max_bytes")
	}
	if c.Performance.LibraryEnabled() && c.Performance.Library.MaxBytes <= 0 {
		return fmt.Errorf("performance.library.max_bytes must be > 0 when enabled")
	}
	if c.Performance.Library.MaxObject > c.Performance.Library.MaxBytes && c.Performance.Library.MaxBytes > 0 {
		return fmt.Errorf("performance.library.max_object must be <= performance.library.max_bytes")
	}
	if c.Performance.LibraryConcurrencyEnabled() && c.Performance.LibraryConcurrency.Max < 1 {
		return fmt.Errorf("performance.library_concurrency.max must be >= 1 when enabled")
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

func (p Performance) CacheEnabled() bool {
	return p.Cache.Enabled != nil && *p.Cache.Enabled
}

func (p Performance) LibraryEnabled() bool {
	return p.Library.Enabled != nil && *p.Library.Enabled
}

func (p Performance) CoalesceEnabled() bool {
	return p.Coalesce.Enabled != nil && *p.Coalesce.Enabled
}

func (p Performance) WarmLoginEnabled() bool {
	return p.WarmLogin.Enabled != nil && *p.WarmLogin.Enabled
}

func (p Performance) LibraryConcurrencyEnabled() bool {
	return p.LibraryConcurrency.Enabled != nil && *p.LibraryConcurrency.Enabled
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
