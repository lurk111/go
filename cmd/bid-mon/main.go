package main

import (
	"bidmon/internal/bootstrap"
	"bidmon/internal/monitor"
	"bidmon/webui"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func main() {
	if handled, err := bootstrap.Run(os.Args[1:]); handled {
		if err != nil {
			slog.Error("installation command failed", "error", err)
			os.Exit(1)
		}
		return
	}
	cfg, e := monitor.LoadConfig()
	if e != nil {
		slog.Error("invalid configuration")
		os.Exit(1)
	}
	if len(os.Args) > 1 && os.Args[1] == "health" {
		client := &http.Client{Timeout: 4 * time.Second}
		_, port, err := net.SplitHostPort(cfg.Addr)
		if err != nil {
			os.Exit(1)
		}
		res, err := client.Get("http://127.0.0.1:" + port + "/health/ready")
		if err != nil {
			os.Exit(1)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	db, e := monitor.OpenStore(cfg.Database)
	if e != nil {
		slog.Error("storage unavailable")
		os.Exit(1)
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(12)
	defer pool.Close()
	if e = monitor.Migrate(db); e != nil {
		slog.Error("migration failed")
		os.Exit(1)
	}
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		slog.Info("database migration complete")
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	leader, e := monitor.LockLeader(ctx, db)
	if e != nil {
		slog.Error("another monitor is active")
		os.Exit(1)
	}
	defer leader.Close()
	if cfg.Document != nil {
		cfg.Runtime, e = monitor.NewGeneralRuntime(ctx, db, cfg)
		if e != nil {
			slog.Error("monitor configuration unavailable")
			os.Exit(1)
		}
		defer cfg.Runtime.Close()
		cfg.Document, _ = cfg.Runtime.Current()
	}
	var collector *monitor.Collector
	if cfg.PresalesEnabled() {
		source, err := monitor.OpenSource(cfg.MySQL)
		if err != nil {
			slog.Error("source configuration invalid")
			os.Exit(1)
		}
		defer source.DB.Close()
		collector = &monitor.Collector{Cfg: cfg, DB: db, Source: source}
	}
	dispatcher := &monitor.Dispatcher{Cfg: cfg, DB: db}
	diagnosis := &monitor.Diagnosis{Cfg: cfg, DB: db}
	var wg sync.WaitGroup
	loop := func(name string, interval time.Duration, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				if err := fn(ctx); err != nil && ctx.Err() == nil {
					slog.Warn("background operation failed", "operation", name)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(interval):
				}
			}
		}()
	}
	if collector != nil {
		loop("scan", cfg.Poll, func(ctx context.Context) error {
			work, stop := context.WithTimeout(ctx, 60*time.Second)
			defer stop()
			if e := leader.PingContext(work); e != nil {
				cancel()
				return e
			}
			e := collector.Scan(work)
			if e != nil && ctx.Err() == nil {
				health, stopHealth := context.WithTimeout(ctx, 5*time.Second)
				defer stopHealth()
				_ = collector.Health(health, false, time.Now().UTC())
			}
			return e
		})
	}
	if cfg.Runtime != nil {
		loop("checks", time.Second, func(ctx context.Context) error {
			work, stop := context.WithTimeout(ctx, 65*time.Second)
			defer stop()
			if err := leader.PingContext(work); err != nil {
				cancel()
				return err
			}
			return cfg.Runtime.Scan(work)
		})
	}
	loop("notification", time.Second, dispatcher.Deliver)
	if cfg.Diagnose {
		loop("diagnosis_prepare", time.Second, diagnosis.Prepare)
		for i := 0; i < cfg.DiagConcurrency; i++ {
			loop("diagnosis", time.Second, diagnosis.RunOne)
		}
	}
	assets, e := webui.Assets()
	if e != nil {
		slog.Error("frontend unavailable")
		cancel()
		wg.Wait()
		return
	}
	srv := &http.Server{Addr: cfg.Addr, Handler: (&monitor.Server{Cfg: cfg, DB: db, Assets: assets}).Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		slog.Info("independent Go monitor ready", "address", cfg.Addr)
		if e := srv.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
			slog.Error("HTTP server failed")
			cancel()
		}
	}()
	<-ctx.Done()
	shutdown, stop := context.WithTimeout(context.Background(), 12*time.Second)
	defer stop()
	_ = srv.Shutdown(shutdown)
	wg.Wait()
}
