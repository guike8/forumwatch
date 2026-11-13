package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/guike8/forumwatch/internal/watch"
)

func main() {
	if err := run(); err != nil {
		slog.Error("forumwatch stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	path := flag.String("config", "config.json", "configuration file")
	flag.Parse()
	config, err := watch.LoadConfig(*path)
	if err != nil {
		return err
	}
	token := os.Getenv(config.TokenEnv)
	if len(token) < 24 {
		return errors.New(config.TokenEnv + " must contain a random token of at least 24 characters")
	}
	store, err := watch.OpenStore(config.StatePath)
	if err != nil {
		return err
	}
	hub := watch.NewHub()
	collector := watch.NewCollector(config, store, hub)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{
		Addr: config.Listen, Handler: watch.Handler(token, store, hub, collector),
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	done := make(chan error, 2)
	go func() { done <- collector.Run(ctx) }()
	go func() {
		slog.Info("forumwatch listening", "address", config.Listen, "sources", len(config.Sources))
		done <- server.ListenAndServe()
	}()
	var result error
	select {
	case <-ctx.Done():
	case result = <-done:
	}
	stop()
	// Cancel SSE handlers before Shutdown; otherwise long-lived streams delay exit.
	server.Close()
	if errors.Is(result, http.ErrServerClosed) {
		return nil
	}
	return result
}
