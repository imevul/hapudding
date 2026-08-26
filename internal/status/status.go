package status

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/imevul/hapudding/internal/authheader"
	"github.com/imevul/hapudding/internal/config"
	"github.com/imevul/hapudding/internal/health"
	"github.com/imevul/hapudding/internal/imgcache"
	"github.com/imevul/hapudding/internal/libcache"
	"github.com/imevul/hapudding/internal/router"
	"github.com/imevul/hapudding/internal/store"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Server struct {
	cfg   *config.Config
	st    store.Store
	mon   *health.Monitor
	rt    *router.Router
	cache *imgcache.Cache
	lib   *libcache.Cache
	perf  func() any
}

func New(cfg *config.Config, st store.Store, mon *health.Monitor, rt *router.Router, cache *imgcache.Cache, lib *libcache.Cache, perf func() any) *Server {
	return &Server{cfg: cfg, st: st, mon: mon, rt: rt, cache: cache, lib: lib, perf: perf}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.uiIndex)
	mux.HandleFunc("GET /ui/{file}", s.uiFile)
	mux.HandleFunc("/hap/health", s.health)
	mux.HandleFunc("/hap/ready", s.ready)
	mux.HandleFunc("GET /hap/backends", s.backends)
	mux.HandleFunc("POST /hap/backends/{name}/disable", s.disableBackend)
	mux.HandleFunc("POST /hap/backends/{name}/enable", s.enableBackend)
	mux.HandleFunc("GET /hap/user-affinity", s.userAffinity)
	mux.HandleFunc("POST /hap/users/by-name/{username}/unpin", s.unpinUser)
	mux.HandleFunc("/hap/affinity", s.affinity)
	mux.HandleFunc("/hap/cache", s.cacheStatus)
	mux.HandleFunc("/hap/performance", s.performance)
	mux.HandleFunc("/hap/users", s.users)
	mux.HandleFunc("/hap/users/", s.user)
	mux.Handle("/metrics", promhttp.Handler())
	return mux
}

func (s *Server) cacheStatus(w http.ResponseWriter, _ *http.Request) {
	if s.cache == nil {
		writeJSON(w, http.StatusOK, imgcache.Stats{Enabled: false})
		return
	}
	writeJSON(w, http.StatusOK, s.cache.Stats())
}

func (s *Server) performance(w http.ResponseWriter, _ *http.Request) {
	if s.perf == nil {
		writeJSON(w, http.StatusOK, map[string]any{"images": imgcache.Stats{Enabled: s.cache != nil}, "library": libcache.Stats{Enabled: s.lib != nil}})
		return
	}
	writeJSON(w, http.StatusOK, s.perf())
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) ready(w http.ResponseWriter, _ *http.Request) {
	if !s.rt.Ready() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type backendView struct {
	health.Snapshot
	Disabled        bool `json:"disabled"`
	ConfigDisabled  bool `json:"config_disabled"`
	RuntimeDisabled bool `json:"runtime_disabled"`
}

func (s *Server) backendViews() []backendView {
	snaps := s.mon.All()
	out := make([]backendView, 0, len(snaps))
	for _, snap := range snaps {
		v := backendView{
			Snapshot:        snap,
			Disabled:        s.rt.Flags().Disabled(snap.Name),
			ConfigDisabled:  s.rt.Flags().ConfigDisabled(snap.Name),
			RuntimeDisabled: s.rt.Flags().RuntimeDisabled(snap.Name),
		}
		if v.Disabled {
			v.IneligibleReason = "disabled"
		} else {
			v.IneligibleReason = health.IneligibleReason(snap, s.cfg.Affinity.NewClientsRequire)
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) backends(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"backends": s.backendViews()})
}

func (s *Server) disableBackend(w http.ResponseWriter, r *http.Request) {
	s.setBackendDisabled(w, r, true)
}

func (s *Server) enableBackend(w http.ResponseWriter, r *http.Request) {
	s.setBackendDisabled(w, r, false)
}

func (s *Server) setBackendDisabled(w http.ResponseWriter, r *http.Request, disabled bool) {
	name := r.PathValue("name")
	if s.rt.Backend(name) == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "backend": name})
		return
	}
	if !disabled && s.rt.Flags().ConfigDisabled(name) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    "config_disabled",
			"backend":  name,
			"backends": s.backendViews(),
		})
		return
	}
	if err := s.rt.Flags().SetRuntime(r.Context(), s.st, name, disabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backends": s.backendViews()})
}

func (s *Server) userAffinity(w http.ResponseWriter, _ *http.Request) {
	entries := []config.UserAffinityEntry{}
	if s.cfg != nil {
		entries = s.cfg.Affinity.UserAffinityEntries()
		if entries == nil {
			entries = []config.UserAffinityEntry{}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"userAffinity": entries})
}

func (s *Server) unpinUser(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.PathValue("username"))
	if username == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "username_required"})
		return
	}
	tokens, devices, err := s.st.DeletePinsByUsername(r.Context(), username, s.publicUserIDs(r.Context(), username)...)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"username": username, "tokens": tokens, "devices": devices})
}

func (s *Server) publicUserIDs(ctx context.Context, username string) []string {
	if s.cfg == nil {
		return nil
	}
	var ids []string
	seen := map[string]struct{}{}
	for _, b := range s.cfg.Backends {
		id := publicUserID(ctx, b, username)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func publicUserID(ctx context.Context, b config.Backend, username string) string {
	tr, err := health.HopTransport(b)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c := &http.Client{Timeout: 3 * time.Second, Transport: tr}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(b.URL, "/")+"/Users/Public", nil)
	if err != nil {
		return ""
	}
	res, err := c.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, res.Body)
		return ""
	}
	var users []struct {
		Name string `json:"Name"`
		ID   string `json:"Id"`
	}
	if json.NewDecoder(res.Body).Decode(&users) != nil {
		return ""
	}
	for _, u := range users {
		if strings.EqualFold(strings.TrimSpace(u.Name), username) {
			return strings.TrimSpace(u.ID)
		}
	}
	return ""
}

func (s *Server) affinity(w http.ResponseWriter, r *http.Request) {
	counts, err := s.st.CountsByBackend(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, counts)
}

type userRow struct {
	UserID     any    `json:"userId"`
	Username   string `json:"username,omitempty"`
	Backend    string `json:"backend"`
	Affinity   string `json:"affinity"`
	Created    string `json:"created"`
	LastActive string `json:"lastActive"`
	Status     string `json:"status"`
	SessionID  string `json:"sessionId,omitempty"`
	Graylisted bool   `json:"graylisted,omitempty"`
}

type userDump struct {
	userRow
	Sessions      []sessionDump   `json:"sessions"`
	BackendHealth health.Snapshot `json:"backendHealth"`
}

type sessionDump struct {
	DeviceID    string `json:"deviceId,omitempty"`
	Client      string `json:"client,omitempty"`
	Device      string `json:"device,omitempty"`
	Version     string `json:"version,omitempty"`
	TokenPrefix string `json:"tokenHashPrefix"`
	Created     string `json:"created"`
	LastActive  string `json:"lastActive"`
	LastMethod  string `json:"lastMethod,omitempty"`
	LastPath    string `json:"lastPath,omitempty"`
	LastStatus  int    `json:"lastStatus,omitempty"`
}

func (s *Server) users(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/hap/users" {
		s.user(w, r)
		return
	}
	rows, err := s.listUsers(r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) user(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/hap/users/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	backendFilter := r.URL.Query().Get("backend")
	tokens, err := s.st.ListTokens(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	grouped := map[string][]store.TokenRow{}
	for _, t := range tokens {
		if t.UserID != id {
			continue
		}
		if backendFilter != "" && t.Backend != backendFilter {
			continue
		}
		grouped[t.Backend] = append(grouped[t.Backend], t)
	}
	if len(grouped) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
		return
	}
	var dumps []userDump
	for backend, sess := range grouped {
		d := userDump{
			userRow:       s.rowFromTokens(sess, s.mon.State(backend)),
			BackendHealth: *s.mon.Snapshot(backend),
		}
		for _, t := range sess {
			d.Sessions = append(d.Sessions, sessionDump{
				DeviceID:    t.DeviceID,
				Client:      t.Client,
				Device:      t.Device,
				Version:     t.Version,
				TokenPrefix: prefix(t.TokenHash),
				Created:     t.CreatedAt.UTC().Format(time.RFC3339),
				LastActive:  t.LastSeen.UTC().Format(time.RFC3339),
				LastMethod:  t.LastMethod,
				LastPath:    t.LastPath,
				LastStatus:  t.LastStatus,
			})
		}
		dumps = append(dumps, d)
	}
	writeJSON(w, http.StatusOK, dumps)
}

func (s *Server) listUsers(r *http.Request) ([]userRow, error) {
	tokens, err := s.st.ListTokens(r.Context())
	if err != nil {
		return nil, err
	}
	type key struct{ backend, user string }
	grouped := map[key][]store.TokenRow{}
	for _, t := range tokens {
		k := key{t.Backend, t.UserID}
		if t.UserID == "" {
			k.user = t.SessionID
		}
		grouped[k] = append(grouped[k], t)
	}
	var out []userRow
	for _, sess := range grouped {
		out = append(out, s.rowFromTokens(sess, s.mon.State(sess[0].Backend)))
	}
	slices.SortFunc(out, func(a, b userRow) int {
		if c := strings.Compare(strings.ToLower(a.Username), strings.ToLower(b.Username)); c != 0 {
			return c
		}
		if c := strings.Compare(userIDKey(a.UserID), userIDKey(b.UserID)); c != 0 {
			return c
		}
		return strings.Compare(a.Backend, b.Backend)
	})
	return out, nil
}

func userIDKey(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func (s *Server) rowFromTokens(sess []store.TokenRow, st health.State) userRow {
	t := sess[0]
	for _, x := range sess[1:] {
		if x.LastSeen.After(t.LastSeen) {
			t = x
		}
	}
	var uid any
	if t.UserID != "" {
		uid = t.UserID
	} else {
		uid = nil
	}
	return userRow{
		UserID:     uid,
		Username:   t.Username,
		Backend:    t.Backend,
		Affinity:   "token",
		Created:    t.CreatedAt.UTC().Format(time.RFC3339),
		LastActive: t.LastSeen.UTC().Format(time.RFC3339),
		Status:     userStatus(t, st),
		SessionID:  t.SessionID,
		Graylisted: s.rt.Graylisted(nil, authheader.Identifiers{Client: t.Client}, t.Client),
	}
}

func userStatus(t store.TokenRow, st health.State) string {
	if t.UserID == "" {
		return "unknown_user"
	}
	if st == health.StateUnhealthy {
		return "backend_unhealthy"
	}
	if time.Since(t.LastSeen) > 30*time.Minute {
		return "idle"
	}
	return "active"
}

func prefix(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
