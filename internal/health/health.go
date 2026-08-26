package health

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/imevul/hapudding/internal/config"
)

type State string

const (
	StateUnknown   State = "unknown"
	StateHealthy   State = "healthy"
	StateDegraded  State = "degraded"
	StateUnhealthy State = "unhealthy"
)

type Layer struct {
	OK      bool      `json:"ok"`
	Error   string    `json:"error,omitempty"`
	Status  int       `json:"status,omitempty"`
	Checked time.Time `json:"checked,omitempty"`
}

type Snapshot struct {
	Name             string `json:"name"`
	URL              string `json:"url"`
	State            State  `json:"state"`
	Reachability     Layer  `json:"reachability"`
	PublicInfo       Layer  `json:"public_info"`
	JellyfinHealth   Layer  `json:"jellyfin_health"`
	AuthPlane        Layer  `json:"auth_plane"`
	PublicID         string `json:"public_id,omitempty"`
	PublicName       string `json:"public_name,omitempty"`
	Version          string `json:"version,omitempty"`
	AuthFailures     int    `json:"auth_failures"`
	IneligibleReason string `json:"ineligible_reason,omitempty"`
}

type Monitor struct {
	cfg     *config.Config
	clients map[string]*http.Client
	noka    map[string]http.RoundTripper
	mu      sync.RWMutex
	snaps   map[string]*Snapshot
	fails   map[string][]time.Time
}

func New(cfg *config.Config) (*Monitor, error) {
	m := &Monitor{
		cfg:     cfg,
		clients: map[string]*http.Client{},
		noka:    map[string]http.RoundTripper{},
		snaps:   map[string]*Snapshot{},
		fails:   map[string][]time.Time{},
	}
	for _, b := range cfg.Backends {
		c, err := hopClient(b)
		if err != nil {
			return nil, err
		}
		m.clients[b.Name] = c
		noka, err := HopTransport(b)
		if err != nil {
			return nil, err
		}
		noka.DisableKeepAlives = true
		m.noka[b.Name] = noka
		m.snaps[b.Name] = &Snapshot{Name: b.Name, URL: publicURL(b), State: StateUnknown}
	}
	return m, nil
}

func hopClient(b config.Backend) (*http.Client, error) {
	tr, err := HopTransport(b)
	if err != nil {
		return nil, err
	}
	return &http.Client{Timeout: b.Timeout, Transport: tr}, nil
}

// HopTransport builds the per-backend TLS hop (keep-alives enabled).
func HopTransport(b config.Backend) (*http.Transport, error) {
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	tr := &http.Transport{
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		IdleConnTimeout:       30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if b.TLS.ServerName != "" {
		tr.TLSClientConfig.ServerName = b.TLS.ServerName
	} else if b.Host != "" {
		tr.TLSClientConfig.ServerName = b.Host
	}
	tr.TLSClientConfig.InsecureSkipVerify = b.TLS.InsecureSkipVerify
	if b.TLS.CAFile != "" {
		pem, err := os.ReadFile(b.TLS.CAFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(pem)
		tr.TLSClientConfig.RootCAs = pool
	}
	if b.TLS.ClientCert != "" && b.TLS.ClientKey != "" {
		cert, err := tls.LoadX509KeyPair(b.TLS.ClientCert, b.TLS.ClientKey)
		if err != nil {
			return nil, err
		}
		tr.TLSClientConfig.Certificates = []tls.Certificate{cert}
	}
	return tr, nil
}

func publicURL(b config.Backend) string {
	return strings.TrimRight(b.URL, "/")
}

func (m *Monitor) Client(name string) *http.Client {
	return m.clients[name]
}

// RoundTripper returns the hop transport. disableKeepAlive is for gray-listed clients.
func (m *Monitor) RoundTripper(name string, disableKeepAlive bool) http.RoundTripper {
	if disableKeepAlive {
		return m.noka[name]
	}
	if c := m.clients[name]; c != nil {
		return c.Transport
	}
	return nil
}

func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(m.cfg.Health.Interval)
	defer t.Stop()
	m.ProbeAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.ProbeAll(ctx)
		}
	}
}

func (m *Monitor) ProbeAll(ctx context.Context) {
	for _, b := range m.cfg.Backends {
		m.probe(ctx, b)
	}
}

func (m *Monitor) probe(ctx context.Context, b config.Backend) {
	base := strings.TrimRight(b.URL, "/")
	if b.HealthURL != "" {
		base = strings.TrimRight(b.HealthURL, "/")
	}
	c := m.clients[b.Name]
	snap := &Snapshot{Name: b.Name, URL: publicURL(b)}

	snap.Reachability = m.get(ctx, c, b, base+"/System/Info/Public")
	if !snap.Reachability.OK {
		// try bare connect via same URL
		snap.State = StateUnhealthy
		m.set(b.Name, snap)
		return
	}

	if m.cfg.Health.PublicInfoEnabled() {
		snap.PublicInfo = m.getJSON(ctx, c, b, base+"/System/Info/Public", func(body []byte) {
			var info struct {
				ID      string `json:"Id"`
				Name    string `json:"ServerName"`
				Version string `json:"Version"`
			}
			if json.Unmarshal(body, &info) == nil {
				snap.PublicID = info.ID
				snap.PublicName = info.Name
				snap.Version = info.Version
			}
		})
	} else {
		snap.PublicInfo = Layer{OK: true}
	}

	if m.cfg.Health.JellyfinHealthEnabled() {
		snap.JellyfinHealth = m.get(ctx, c, b, base+"/health")
	} else {
		snap.JellyfinHealth = Layer{OK: true}
	}

	authOK := true
	authErr := ""
	if m.cfg.Health.PassiveEnabled() {
		n := m.recentFails(b.Name)
		snap.AuthFailures = n
		if n >= m.cfg.Health.PassiveAuthFailures.Threshold {
			authOK = false
			authErr = "passive auth-plane failures"
		}
	}
	if m.cfg.Health.AuthProbe.Enabled && m.cfg.Health.AuthProbe.Username != "" {
		layer := m.probeAuth(ctx, c, b, base)
		if !layer.OK {
			authOK = false
			authErr = layer.Error
		}
	}
	snap.AuthPlane = Layer{OK: authOK, Error: authErr, Checked: time.Now()}

	switch {
	case !snap.JellyfinHealth.OK || !snap.PublicInfo.OK:
		snap.State = StateUnhealthy
	case !authOK:
		snap.State = StateDegraded
	default:
		snap.State = StateHealthy
	}
	m.set(b.Name, snap)
}

func (m *Monitor) get(ctx context.Context, c *http.Client, b config.Backend, url string) Layer {
	return m.getJSON(ctx, c, b, url, nil)
}

func (m *Monitor) getJSON(ctx context.Context, c *http.Client, b config.Backend, url string, fn func([]byte)) Layer {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Layer{Error: err.Error(), Checked: time.Now()}
	}
	applyHop(req, b)
	res, err := c.Do(req)
	if err != nil {
		return Layer{Error: err.Error(), Checked: time.Now()}
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	ok := res.StatusCode >= 200 && res.StatusCode < 300
	layer := Layer{OK: ok, Status: res.StatusCode, Checked: time.Now()}
	if !ok {
		layer.Error = res.Status
	}
	if ok && fn != nil {
		fn(body)
	}
	return layer
}

func (m *Monitor) probeAuth(ctx context.Context, c *http.Client, b config.Backend, base string) Layer {
	body := `{"Username":` + jsonString(m.cfg.Health.AuthProbe.Username) + `,"Pw":` + jsonString(m.cfg.Health.AuthProbe.Password) + `}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/Users/AuthenticateByName", strings.NewReader(body))
	if err != nil {
		return Layer{Error: err.Error(), Checked: time.Now()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", `MediaBrowser Client="HAP", Device="hapudding", DeviceId="hap-probe", Version="0.1.0"`)
	applyHop(req, b)
	res, err := c.Do(req)
	if err != nil {
		return Layer{Error: err.Error(), Checked: time.Now()}
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	ok := res.StatusCode >= 200 && res.StatusCode < 300
	layer := Layer{OK: ok, Status: res.StatusCode, Checked: time.Now()}
	if !ok {
		layer.Error = res.Status
	}
	return layer
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func applyHop(req *http.Request, b config.Backend) {
	if b.Host != "" {
		req.Host = b.Host
		req.Header.Set("Host", b.Host)
	}
	for k, v := range b.Headers {
		req.Header.Set(k, v)
	}
}

func (m *Monitor) RecordAuthFailure(backend string) {
	if !m.cfg.Health.PassiveEnabled() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fails[backend] = append(m.fails[backend], time.Now())
}

func (m *Monitor) recentFails(backend string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	cut := time.Now().Add(-m.cfg.Health.PassiveAuthFailures.Window)
	var keep []time.Time
	for _, t := range m.fails[backend] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	m.fails[backend] = keep
	return len(keep)
}

func (m *Monitor) set(name string, snap *Snapshot) {
	m.mu.Lock()
	m.snaps[name] = snap
	m.mu.Unlock()
}

func (m *Monitor) Snapshot(name string) *Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.snaps[name]
	if s == nil {
		return &Snapshot{Name: name, State: StateUnknown}
	}
	cp := *s
	return &cp
}

func (m *Monitor) All() []Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Snapshot, 0, len(m.snaps))
	for _, b := range m.cfg.Backends {
		if s, ok := m.snaps[b.Name]; ok {
			out = append(out, *s)
		}
	}
	return out
}

func (m *Monitor) State(name string) State {
	return m.Snapshot(name).State
}

// SetState overrides the last probe result (tests and ops tooling).
func (m *Monitor) SetState(name string, st State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.snaps[name]; ok {
		s.State = st
		return
	}
	m.snaps[name] = &Snapshot{Name: name, State: st}
}

// IneligibleReason explains why a backend is excluded from new-client picks.
func IneligibleReason(s Snapshot, newClientsRequire string) string {
	switch s.State {
	case StateHealthy, StateUnknown:
		return ""
	case StateDegraded:
		if newClientsRequire == "healthy" {
			return "degraded_auth_plane"
		}
		return ""
	case StateUnhealthy:
		if !s.Reachability.OK && s.Reachability.Error != "" {
			return "unreachable"
		}
		if !s.JellyfinHealth.OK {
			return "jellyfin_health"
		}
		if !s.PublicInfo.OK {
			return "public_info"
		}
		return "unhealthy"
	default:
		return string(s.State)
	}
}
