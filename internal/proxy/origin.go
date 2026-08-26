package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/imevul/hapudding/internal/config"
)

type inboundOrigin struct {
	Host  string
	Proto string
	Port  string
}

func (o inboundOrigin) String() string {
	if o.Host == "" {
		return ""
	}
	if o.Proto == "" {
		o.Proto = "http"
	}
	return o.Proto + "://" + o.Host
}

func inboundOriginOf(r *http.Request) inboundOrigin {
	if r == nil {
		return inboundOrigin{}
	}
	host := firstCSV(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	proto := firstCSV(r.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		if r.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	port := firstCSV(r.Header.Get("X-Forwarded-Port"))
	if port == "" {
		if _, p, err := net.SplitHostPort(host); err == nil {
			port = p
		} else if proto == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return inboundOrigin{Host: host, Proto: proto, Port: port}
}

func firstCSV(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, ','); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func applyForwardedOrigin(req *http.Request, in *http.Request, b *config.Backend, stay bool) {
	if req == nil || in == nil {
		return
	}
	orig := inboundOriginOf(in)
	if stay {
		if orig.Host != "" {
			req.Header.Set("X-Forwarded-Host", orig.Host)
		}
		if orig.Proto != "" {
			req.Header.Set("X-Forwarded-Proto", orig.Proto)
		}
		if orig.Port != "" {
			req.Header.Set("X-Forwarded-Port", orig.Port)
		}
	} else if req.Header.Get("X-Forwarded-Proto") == "" {
		req.Header.Set("X-Forwarded-Proto", orig.Proto)
	}
	if req.Header.Get("X-Forwarded-For") == "" {
		if host, _, err := net.SplitHostPort(in.RemoteAddr); err == nil {
			req.Header.Set("X-Forwarded-For", host)
		}
	}
	if b != nil && b.Host != "" {
		req.Host = b.Host
		return
	}
	if stay && orig.Host != "" {
		req.Host = orig.Host
		return
	}
	if b != nil {
		if pu, err := url.Parse(b.URL); err == nil {
			req.Host = pu.Host
		}
	}
}

func rewriteAnyAbsURL(raw, inbound string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || inbound == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return raw
	}
	in, err := url.Parse(inbound)
	if err != nil || in.Host == "" {
		return raw
	}
	u.Scheme = in.Scheme
	u.Host = in.Host
	return u.String()
}

func rewriteLocationHeaders(h http.Header, inbound string) {
	if h == nil || inbound == "" {
		return
	}
	for _, name := range []string{"Location", "Content-Location"} {
		vals := h.Values(name)
		if len(vals) == 0 {
			continue
		}
		h.Del(name)
		for _, v := range vals {
			h.Add(name, rewriteAnyAbsURL(v, inbound))
		}
	}
}

func isSystemInfoPath(path string) bool {
	p := strings.ToLower(path)
	return strings.HasSuffix(p, "/system/info") || strings.HasSuffix(p, "/system/info/public")
}

func isPlaylist(path, contentType string) bool {
	p := strings.ToLower(path)
	if strings.HasSuffix(p, ".m3u8") || strings.HasSuffix(p, ".mpd") {
		return true
	}
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "mpegurl") || strings.Contains(ct, "dash+xml")
}

func rewritePlaylistBody(body []byte, inbound string) []byte {
	if inbound == "" {
		return body
	}
	lines := strings.Split(string(body), "\n")
	changed := false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if !strings.HasPrefix(trim, "http://") && !strings.HasPrefix(trim, "https://") {
			continue
		}
		rewritten := rewriteAnyAbsURL(trim, inbound)
		if rewritten == trim {
			continue
		}
		lines[i] = strings.Replace(line, trim, rewritten, 1)
		changed = true
	}
	if !changed {
		return body
	}
	return []byte(strings.Join(lines, "\n"))
}

func rewriteSystemInfoJSON(body []byte, inbound, serverID, serverName string, rewriteURLs bool) []byte {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	changed := false
	if serverID != "" {
		if _, ok := m["Id"]; ok {
			m["Id"] = serverID
			changed = true
		}
	}
	if serverName != "" {
		m["ServerName"] = serverName
		if _, ok := m["Name"]; ok {
			m["Name"] = serverName
		}
		changed = true
	}
	if rewriteURLs && inbound != "" {
		for k, v := range m {
			switch t := v.(type) {
			case string:
				if strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://") {
					in, err := url.Parse(inbound)
					if err != nil || in.Host == "" {
						continue
					}
					u, err := url.Parse(t)
					if err != nil || !u.IsAbs() {
						continue
					}
					u.Scheme = in.Scheme
					u.Host = in.Host
					m[k] = u.String()
					changed = true
				}
			case []any:
				rewritten := rewriteURLArray(t, inbound)
				if rewritten != nil {
					m[k] = rewritten
					changed = true
				}
			}
		}
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

func rewriteURLArray(arr []any, inbound string) []any {
	in, err := url.Parse(inbound)
	if err != nil || in.Host == "" {
		return nil
	}
	out := make([]any, len(arr))
	changed := false
	for i, v := range arr {
		s, ok := v.(string)
		if !ok || !(strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) {
			out[i] = v
			continue
		}
		u, err := url.Parse(s)
		if err != nil || !u.IsAbs() {
			out[i] = v
			continue
		}
		u.Scheme = in.Scheme
		u.Host = in.Host
		out[i] = u.String()
		changed = true
	}
	if !changed {
		return nil
	}
	return out
}

func bufferAndRewrite(res *http.Response, max int64, fn func([]byte) []byte) {
	if res == nil || res.Body == nil || fn == nil {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, max+1))
	if err != nil || int64(len(raw)) > max {
		res.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), res.Body))
		return
	}
	_ = res.Body.Close()
	out := fn(raw)
	res.Body = io.NopCloser(bytes.NewReader(out))
	res.ContentLength = int64(len(out))
	res.Header.Set("Content-Length", strconv.Itoa(len(out)))
	res.Header.Del("Transfer-Encoding")
}

func (h *Handler) rewriteProxiedResponse(res *http.Response, r *http.Request) {
	if res == nil || r == nil || h.cfg == nil {
		return
	}
	stay := h.cfg.StayOnOriginEnabled()
	xlate := h.cfg.TranslateServerIDEnabled()
	if !stay && !xlate {
		return
	}
	orig := inboundOriginOf(r)
	if stay {
		rewriteLocationHeaders(res.Header, orig.String())
	}
	path := r.URL.Path
	if res.Request != nil && res.Request.URL != nil {
		path = res.Request.URL.Path
	}
	if stay && h.cfg.RewritePlaylistsEnabled() && isPlaylist(path, res.Header.Get("Content-Type")) {
		bufferAndRewrite(res, 8<<20, func(body []byte) []byte {
			return rewritePlaylistBody(body, orig.String())
		})
		return
	}
	if r.Method == http.MethodGet && isSystemInfoPath(path) && res.StatusCode == http.StatusOK {
		ct := strings.ToLower(res.Header.Get("Content-Type"))
		if ct != "" && !strings.Contains(ct, "json") {
			return
		}
		sid, sname := "", ""
		if xlate {
			sid = h.cfg.Translate.ServerID.ID
			sname = h.cfg.TranslateServerName()
		}
		bufferAndRewrite(res, 1<<20, func(body []byte) []byte {
			return rewriteSystemInfoJSON(body, orig.String(), sid, sname, stay)
		})
	}
}
