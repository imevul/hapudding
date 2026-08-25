// Package authheader parses Jellyfin/Emby identifiers without changing semantics.
package authheader

import (
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

// Identifiers extracted from a request. Empty fields were not present.
type Identifiers struct {
	Token    string
	DeviceID string
	Client   string
	Device   string
	Version  string
}

// Parse reads MediaBrowser Authorization, legacy headers, and query tokens.
// Unknown MediaBrowser keys are ignored. This does not mutate the request.
func Parse(r *http.Request) Identifiers {
	var id Identifiers
	if r == nil {
		return id
	}

	if parts := parseMediaBrowser(r.Header.Get("Authorization")); parts != nil {
		applyParts(&id, parts)
	}
	if id.Token == "" || id.DeviceID == "" {
		if parts := parseMediaBrowser(r.Header.Get("X-Emby-Authorization")); parts != nil {
			if id.Token == "" {
				id.Token = parts["Token"]
			}
			if id.DeviceID == "" {
				id.DeviceID = parts["DeviceId"]
			}
			if id.Client == "" {
				id.Client = parts["Client"]
			}
			if id.Device == "" {
				id.Device = parts["Device"]
			}
			if id.Version == "" {
				id.Version = parts["Version"]
			}
		}
	}
	if id.Token == "" {
		id.Token = firstNonEmpty(
			r.Header.Get("X-Emby-Token"),
			r.Header.Get("X-MediaBrowser-Token"),
		)
	}
	q := r.URL.Query()
	if id.Token == "" {
		id.Token = firstNonEmpty(q.Get("ApiKey"), q.Get("api_key"))
	}
	if id.DeviceID == "" {
		id.DeviceID = firstNonEmpty(q.Get("deviceId"), q.Get("device_id"), q.Get("DeviceId"))
	}
	return id
}

func applyParts(id *Identifiers, parts map[string]string) {
	if v := parts["Token"]; v != "" {
		id.Token = v
	}
	if v := parts["DeviceId"]; v != "" {
		id.DeviceID = v
	}
	if v := parts["Client"]; v != "" {
		id.Client = v
	}
	if v := parts["Device"]; v != "" {
		id.Device = v
	}
	if v := parts["Version"]; v != "" {
		id.Version = v
	}
}

func parseMediaBrowser(header string) map[string]string {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	scheme, rest, ok := strings.Cut(header, " ")
	if !ok {
		return nil
	}
	if !strings.EqualFold(scheme, "MediaBrowser") && !strings.EqualFold(scheme, "Emby") {
		return nil
	}
	return parseNamedValues(rest)
}

// parseNamedValues implements Jellyfin's comma-separated key="value" scheme.
// Values are double-quoted and URL-encoded. Unknown keys are kept then ignored by callers.
func parseNamedValues(s string) map[string]string {
	out := make(map[string]string)
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ',' || unicode.IsSpace(rune(s[i]))) {
			i++
		}
		if i >= len(s) {
			break
		}
		start := i
		for i < len(s) && s[i] != '=' {
			i++
		}
		if i >= len(s) {
			break
		}
		key := strings.TrimSpace(s[start:i])
		i++ // skip =
		for i < len(s) && unicode.IsSpace(rune(s[i])) {
			i++
		}
		var val string
		if i < len(s) && s[i] == '"' {
			i++
			var b strings.Builder
			escaped := false
			for i < len(s) {
				c := s[i]
				i++
				if escaped {
					b.WriteByte(c)
					escaped = false
					continue
				}
				if c == '\\' {
					escaped = true
					continue
				}
				if c == '"' {
					break
				}
				b.WriteByte(c)
			}
			val = b.String()
		} else {
			start = i
			for i < len(s) && s[i] != ',' {
				i++
			}
			val = strings.TrimSpace(s[start:i])
		}
		if key != "" {
			if dec, err := url.QueryUnescape(val); err == nil {
				val = dec
			}
			out[key] = val
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
