package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/imevul/hapudding/internal/config"
)

func TestPublicOKPlusAuthFailuresIsDegraded(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/System/Info/Public", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Id":"id-a","ServerName":"A","Version":"10"}`))
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := testHealthCfg(srv.URL)
	mon, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mon.ProbeAll(context.Background())
	if mon.State("server-a") != StateHealthy {
		t.Fatalf("want healthy, got %s", mon.State("server-a"))
	}
	for i := 0; i < cfg.Health.PassiveAuthFailures.Threshold; i++ {
		mon.RecordAuthFailure("server-a")
	}
	mon.ProbeAll(context.Background())
	if mon.State("server-a") != StateDegraded {
		t.Fatalf("public-OK + login-5xx window should be degraded, got %s", mon.State("server-a"))
	}
	snap := mon.Snapshot("server-a")
	if snap.PublicID != "id-a" {
		t.Fatalf("cached public id: %+v", snap)
	}
}

func TestJellyfinHealthFailIsUnhealthy(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/System/Info/Public", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"Id":"x"}`))
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "db locked", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mon, err := New(testHealthCfg(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	mon.ProbeAll(context.Background())
	if mon.State("server-a") != StateUnhealthy {
		t.Fatalf("got %s", mon.State("server-a"))
	}
}

func testHealthCfg(url string) *config.Config {
	t := true
	return &config.Config{
		Backends: []config.Backend{{Name: "server-a", URL: url, Timeout: 2 * time.Second}},
		Health: config.Health{
			Interval:            time.Hour,
			PublicInfo:          &t,
			JellyfinHealth:      &t,
			PassiveAuthFailures: config.PassiveAuthFailures{Enabled: &t, Threshold: 3, Window: time.Minute},
		},
	}
}
