package proxy

import (
	"net/http"
	"strings"
	"testing"
)

func TestRewritePlaylistBodyLeavesRelative(t *testing.T) {
	in := "#EXTM3U\nhttps://server-a.example.test/Videos/x/segment.ts\nsegment.ts\n"
	out := string(rewritePlaylistBody([]byte(in), "http://hap.example.test"))
	if !strings.Contains(out, "http://hap.example.test/Videos/x/segment.ts") {
		t.Fatalf("abs: %s", out)
	}
	if !strings.Contains(out, "\nsegment.ts\n") {
		t.Fatalf("rel: %s", out)
	}
}

func TestRewriteSystemInfoJSONIdAndURLs(t *testing.T) {
	in := []byte(`{"Id":"SERVER-A-ID","LocalAddress":"https://server-a.example.test:8096","Name":"A"}`)
	out := string(rewriteSystemInfoJSON(in, "http://hap.example.test", "11111111-2222-3333-4444-555555555555", "", true))
	if !strings.Contains(out, `"Id":"11111111-2222-3333-4444-555555555555"`) {
		t.Fatalf("id: %s", out)
	}
	if !strings.Contains(out, `"Name":"A"`) {
		t.Fatalf("empty name must leave Name: %s", out)
	}
	if strings.Contains(out, "server-a.example.test") || !strings.Contains(out, `"LocalAddress":"http://hap.example.test`) {
		t.Fatalf("url: %s", out)
	}
	named := string(rewriteSystemInfoJSON(in, "http://hap.example.test", "11111111-2222-3333-4444-555555555555", "HAP", false))
	if !strings.Contains(named, `"Name":"HAP"`) || !strings.Contains(named, `"ServerName":"HAP"`) {
		t.Fatalf("name: %s", named)
	}
}

func TestIsSystemInfoAndPlaylist(t *testing.T) {
	if !isSystemInfoPath("/System/Info") || !isSystemInfoPath("/System/Info/Public") {
		t.Fatal("system info paths")
	}
	if isSystemInfoPath("/Users/x/Items") {
		t.Fatal("items is not system info")
	}
	if !isPlaylist("/Videos/x/master.m3u8", "text/plain") || !isPlaylist("/x", "application/vnd.apple.mpegurl") {
		t.Fatal("playlist detect")
	}
	if isPlaylist("/Videos/x/stream", "video/mp4") {
		t.Fatal("byte stream is not a playlist")
	}
	h := http.Header{}
	h.Set("Location", "https://server-a.example.test/web/")
	rewriteLocationHeaders(h, "http://hap.example.test")
	if h.Get("Location") != "http://hap.example.test/web/" {
		t.Fatalf("location %q", h.Get("Location"))
	}
}
