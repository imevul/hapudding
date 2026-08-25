package router

import (
	"context"
	"hash/fnv"
	"net"
	"net/http"
	"sort"
	"strings"

	"github.com/imevul/hapudding/internal/authheader"
	"github.com/imevul/hapudding/internal/config"
	"github.com/imevul/hapudding/internal/health"
	"github.com/imevul/hapudding/internal/store"
)

type Decision struct {
	Backend     *config.Backend
	Kind        store.BindingKind
	Bound       bool
	DropClient  bool
	RefuseToken bool // force_reauth: do not forward old token
	Graylisted  bool
	HAPError    string
	HAPStatus   int
}

type Router struct {
	cfg *config.Config
	st  store.Store
	mon *health.Monitor
	by  map[string]*config.Backend
}

func New(cfg *config.Config, st store.Store, mon *health.Monitor) *Router {
	r := &Router{cfg: cfg, st: st, mon: mon, by: map[string]*config.Backend{}}
	for i := range cfg.Backends {
		b := &cfg.Backends[i]
		r.by[b.Name] = b
	}
	return r
}

func (r *Router) Backend(name string) *config.Backend {
	return r.by[name]
}

func (r *Router) Names() []string {
	names := make([]string, 0, len(r.cfg.Backends))
	for _, b := range r.cfg.Backends {
		names = append(names, b.Name)
	}
	sort.Strings(names)
	return names
}

func (r *Router) Decide(ctx context.Context, req *http.Request, id authheader.Identifiers) Decision {
	anonKey := store.HashAnon(clientIP(req), req.UserAgent())

	var tokenRow *store.TokenRow
	var deviceRow *store.SimpleBinding
	if id.Token != "" {
		tokenRow, _ = r.st.LookupToken(ctx, id.Token)
	}
	if id.DeviceID != "" {
		deviceRow, _ = r.st.LookupDevice(ctx, id.DeviceID)
	}
	tokenClient, deviceClient := "", ""
	if tokenRow != nil {
		tokenClient = tokenRow.Client
	}
	if deviceRow != nil {
		deviceClient = deviceRow.Client
	}
	gray := r.Graylisted(req, id, tokenClient, deviceClient)
	policy := r.policyFor(gray)

	if tokenRow != nil {
		return r.bound(ctx, tokenRow.Backend, store.KindToken, id, policy, gray)
	}
	if deviceRow != nil {
		return r.bound(ctx, deviceRow.Backend, store.KindDevice, id, policy, gray)
	}
	if row, _ := r.st.LookupAnon(ctx, anonKey); row != nil {
		return r.bound(ctx, row.Backend, store.KindAnon, id, policy, gray)
	}

	return r.pickNew(ctx, req, id, anonKey, policy, gray)
}

// Graylisted reports whether this request or stored client class matches the gray-list.
func (r *Router) Graylisted(req *http.Request, id authheader.Identifiers, storedClients ...string) bool {
	g := r.cfg.Affinity.Graylist
	if g.MatchesClient(id.Client) {
		return true
	}
	if req != nil && req.URL != nil && g.MatchesPath(req.URL.Path) {
		return true
	}
	for _, c := range storedClients {
		if g.MatchesClient(c) {
			return true
		}
	}
	return false
}

func (r *Router) policyFor(graylisted bool) string {
	if graylisted {
		p := r.cfg.Affinity.Graylist.Policy
		if p == "" {
			return "fail_closed"
		}
		return p
	}
	return r.cfg.Affinity.Policy
}

func (r *Router) bound(ctx context.Context, name string, kind store.BindingKind, id authheader.Identifiers, policy string, gray bool) Decision {
	b := r.by[name]
	st := r.mon.State(name)
	if st == health.StateHealthy || st == health.StateDegraded || st == health.StateUnknown {
		return Decision{Backend: b, Kind: kind, Bound: true, Graylisted: gray}
	}
	// unhealthy
	switch policy {
	case "fail_closed":
		return Decision{Backend: b, Kind: kind, Bound: true, Graylisted: gray, HAPError: "bound_backend_unavailable", HAPStatus: http.StatusServiceUnavailable}
	case "pin_unhealthy":
		if b != nil {
			return Decision{Backend: b, Kind: kind, Bound: true, Graylisted: gray}
		}
		return Decision{Graylisted: gray, HAPError: "bound_backend_unavailable", HAPStatus: http.StatusServiceUnavailable}
	default: // force_reauth
		if id.Token != "" {
			return Decision{
				DropClient:  true,
				Kind:        kind,
				RefuseToken: true,
				Graylisted:  gray,
				HAPError:    "reauth_required",
				HAPStatus:   http.StatusUnauthorized,
			}
		}
		d := r.pickEligible(ctx, id, "", true)
		d.DropClient = true
		d.Kind = kind
		d.Graylisted = gray
		return d
	}
}

func (r *Router) pickNew(ctx context.Context, req *http.Request, id authheader.Identifiers, anonKey, policy string, gray bool) Decision {
	if policy == "pin_unhealthy" {
		// Hash across the full pool so first placement stays even if that backend is down.
		name := r.hashName(id.DeviceID, anonKey, r.Names())
		b := r.by[name]
		st := r.mon.State(name)
		if st == health.StateUnhealthy {
			return Decision{Backend: b, Kind: store.KindDevice, Graylisted: gray, HAPError: "bound_backend_unavailable", HAPStatus: http.StatusServiceUnavailable}
		}
		return Decision{Backend: b, Graylisted: gray}
	}
	d := r.pickEligible(ctx, id, cookieHint(req), false)
	d.Graylisted = gray
	return d
}

func (r *Router) pickEligible(ctx context.Context, id authheader.Identifiers, hint string, afterDrop bool) Decision {
	elig := r.eligible()
	if len(elig) == 0 {
		return Decision{HAPError: "no_eligible_backend", HAPStatus: http.StatusServiceUnavailable}
	}
	if hint != "" {
		for _, b := range elig {
			if b.Name == hint {
				return Decision{Backend: b}
			}
		}
	}
	if id.DeviceID != "" {
		name := r.hashName(id.DeviceID, "", namesOf(elig))
		return Decision{Backend: r.by[name]}
	}
	// least-loaded among eligible
	counts, _ := r.st.CountsByBackend(ctx)
	best := elig[0]
	bestN := loadOf(counts, best.Name)
	for _, b := range elig[1:] {
		n := loadOf(counts, b.Name)
		if n < bestN || (n == bestN && b.Name < best.Name) {
			best = b
			bestN = n
		}
	}
	_ = afterDrop
	return Decision{Backend: best}
}

func (r *Router) eligible() []*config.Backend {
	var out []*config.Backend
	for i := range r.cfg.Backends {
		b := &r.cfg.Backends[i]
		st := r.mon.State(b.Name)
		ok := st == health.StateHealthy || st == health.StateUnknown
		if r.cfg.Affinity.NewClientsRequire == "healthy_or_degraded" {
			ok = ok || st == health.StateDegraded
		}
		if ok {
			out = append(out, b)
		}
	}
	return out
}

func (r *Router) hashName(deviceID, anonKey string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	key := deviceID
	if key == "" {
		key = anonKey
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return names[int(h.Sum32())%len(names)]
}

func namesOf(bs []*config.Backend) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.Name
	}
	return out
}

func loadOf(counts map[string]store.Counts, name string) int {
	c := counts[name]
	return c.Tokens + c.Devices
}

func cookieHint(req *http.Request) string {
	c, err := req.Cookie("hap_backend")
	if err != nil || c.Value == "" {
		return ""
	}
	return c.Value
}

func clientIP(req *http.Request) string {
	if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}
	return host
}

func (r *Router) Ready() bool {
	if err := r.st.Ping(context.Background()); err != nil {
		return false
	}
	if len(r.eligible()) > 0 {
		return true
	}
	// 1-backend fail_closed/pin: ready if that backend can serve bound traffic (not unknown-empty)
	if len(r.cfg.Backends) == 1 {
		st := r.mon.State(r.cfg.Backends[0].Name)
		return st == health.StateHealthy || st == health.StateDegraded || st == health.StateUnknown
	}
	return false
}
