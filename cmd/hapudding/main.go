package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/imevul/hapudding/internal/config"
	"github.com/imevul/hapudding/internal/health"
	"github.com/imevul/hapudding/internal/proxy"
	"github.com/imevul/hapudding/internal/router"
	"github.com/imevul/hapudding/internal/status"
	"github.com/imevul/hapudding/internal/store"
)

var (
	// x-release-please-start-version
	version = "0.1.0"
	// x-release-please-end
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "configs/hap.example.yaml", "path to hap.yaml")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion || (flag.NArg() == 1 && flag.Arg(0) == "version") {
		fmt.Println(version)
		return nil
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	dsn := cfg.Affinity.SQLite.Path
	if cfg.Affinity.Store == "postgres" {
		dsn = cfg.Affinity.Postgres.URL
	}
	st, err := store.Open(cfg.Affinity.Store, dsn, cfg.Affinity.TokenTTL, cfg.Affinity.DeviceTTL, cfg.Affinity.AnonTTL)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer st.Close()

	mon, err := health.New(cfg)
	if err != nil {
		return fmt.Errorf("health: %w", err)
	}
	rt := router.New(cfg, st, mon)
	ph := proxy.New(cfg, rt, st, mon, log)
	sh := status.New(cfg, st, mon, rt)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go mon.Run(ctx)
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				proxy.ObserveStates(mon)
				if counts, err := st.CountsByBackend(ctx); err == nil {
					proxy.ObserveBinds(counts)
				}
			}
		}
	}()

	pub := &http.Server{Addr: cfg.Listen, Handler: ph, MaxHeaderBytes: 1 << 20}
	ops := &http.Server{Addr: cfg.Status.Listen, Handler: sh.Handler()}

	errc := make(chan error, 2)
	go func() {
		log.Info("listen", "addr", cfg.Listen, "version", version)
		errc <- pub.ListenAndServe()
	}()
	go func() {
		log.Info("status", "addr", cfg.Status.Listen)
		errc <- ops.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
	case err := <-errc:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = pub.Shutdown(shutdownCtx)
	_ = ops.Shutdown(shutdownCtx)
	return nil
}
