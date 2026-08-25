package authheader

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseAuthorizationQuotedAndEncoded(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
	r.Header.Set("Authorization", `MediaBrowser Client="Jellyfin Web", Device="Firefox", DeviceId="abc%2Fdef", Version="10.10.0", Token="tok123", UserId="ignored"`)
	id := Parse(r)
	if id.Token != "tok123" || id.DeviceID != "abc/def" || id.Client != "Jellyfin Web" || id.Device != "Firefox" || id.Version != "10.10.0" {
		t.Fatalf("got %+v", id)
	}
}

func TestParseLegacyHeadersAndQuery(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/socket?api_key=qs-token&device_id=dev-under", nil)
	r.Header.Set("X-Emby-Authorization", `MediaBrowser Client="Finamp", Device="Phone", DeviceId="d1", Version="0.1"`)
	r.Header.Set("X-Emby-Token", "legacy-tok")
	id := Parse(r)
	if id.Token != "legacy-tok" {
		t.Fatalf("token=%q", id.Token)
	}
	if id.DeviceID != "d1" {
		t.Fatalf("device from header should win over query, got %q", id.DeviceID)
	}

	r2 := httptest.NewRequest(http.MethodGet, "/socket?api_key=qs-token&deviceId=devQ", nil)
	id2 := Parse(r2)
	if id2.Token != "qs-token" || id2.DeviceID != "devQ" {
		t.Fatalf("got %+v", id2)
	}

	r3 := httptest.NewRequest(http.MethodGet, "/x?ApiKey=AK&device_id=du", nil)
	id3 := Parse(r3)
	if id3.Token != "AK" || id3.DeviceID != "du" {
		t.Fatalf("got %+v", id3)
	}
}

func TestParseDoesNotRequireToken(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/System/Info/Public", nil)
	r.Header.Set("Authorization", `MediaBrowser Client="Delfin", Device="PC", DeviceId="uuid-1", Version="1.0.0"`)
	id := Parse(r)
	if id.Token != "" || id.DeviceID != "uuid-1" {
		t.Fatalf("got %+v", id)
	}
}

func TestParseEmbySchemeAndMediaBrowserToken(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
	r.Header.Set("Authorization", `Emby Client="Old", Device="Box", DeviceId="e1", Version="1"`)
	r.Header.Set("X-MediaBrowser-Token", "mb-tok")
	id := Parse(r)
	if id.Token != "mb-tok" || id.DeviceID != "e1" {
		t.Fatalf("got %+v", id)
	}
}

func TestParseIgnoresUnknownKeysAndDoesNotMutate(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	orig := `MediaBrowser Client="Finamp", Device="Phone", DeviceId="d1", Version="0.1", Token="t", UserId="should-be-ignored"`
	r.Header.Set("Authorization", orig)
	id := Parse(r)
	if id.Token != "t" || id.DeviceID != "d1" {
		t.Fatalf("got %+v", id)
	}
	if r.Header.Get("Authorization") != orig {
		t.Fatal("parser mutated Authorization")
	}
}
