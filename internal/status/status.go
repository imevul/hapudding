package status

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/imevul/hapudding/internal/authheader"
	"github.com/imevul/hapudding/internal/config"
	"github.com/imevul/hapudding/internal/health"
	"github.com/imevul/hapudding/internal/router"
	"github.com/imevul/hapudding/internal/store"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Server struct {
	cfg *config.Config
	st  store.Store
	mon *health.Monitor
	rt  *router.Router
}

func New(cfg *config.Config, st store.Store, mon *health.Monitor, rt *router.Router) *Server {
	return &Server{cfg: cfg, st: st, mon: mon, rt: rt}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/hap/health", s.health)
	mux.HandleFunc("/hap/ready", s.ready)
	mux.HandleFunc("/hap/backends", s.backends)
	mux.HandleFunc("/hap/affinity", s.affinity)
	mux.HandleFunc("/hap/users", s.users)
	mux.HandleFunc("/hap/users/", s.user)
	mux.Handle("/metrics", promhttp.Handler())
	return mux
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

func (s *Server) backends(w http.ResponseWriter, _ *http.Request) {
	snaps := s.mon.All()
	for i := range snaps {
		snaps[i].IneligibleReason = health.IneligibleReason(snaps[i], s.cfg.Affinity.NewClientsRequire)
	}
	writeJSON(w, http.StatusOK, map[string]any{"backends": snaps})
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
	return out, nil
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
