package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
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
	consoleOnly := flag.Bool("console-only", false, "collect to terminal without starting an HTTP server")
	terminal := flag.Bool("terminal", true, "print detected entries to the terminal")
	showBaseline := flag.Bool("show-baseline", false, "also print entries present at the first successful fetch")
	summaries := flag.Bool("summaries", true, "add asynchronous local/extractive summaries (no AI or external calls)")
	requireTor := flag.Bool("require-tor", false, "refuse any source not explicitly routed through Tor")
	checkConfig := flag.Bool("check-config", false, "validate configuration and exit without listening, opening state, or fetching sources")
	readyFile := flag.String("ready-file", "", "create a private PID/address readiness file after binding a loopback API listener")
	flag.Parse()
	config, err := watch.LoadConfig(*path)
	if err != nil {
		return err
	}
	if *requireTor {
		for _, source := range config.Sources {
			if source.Transport != "tor" {
				return fmt.Errorf("%s: require-tor refuses transport %q", source.ID, source.Transport)
			}
		}
	}
	if *checkConfig {
		return nil
	}
	if *consoleOnly && len(config.Sources) == 0 {
		return errors.New("no sources configured: refusing to pretend live monitoring is active")
	}
	if *consoleOnly && !*terminal {
		return errors.New("console-only requires terminal output")
	}
	token := os.Getenv(config.TokenEnv)
	if !*consoleOnly && len(token) < 24 {
		return errors.New(config.TokenEnv + " must contain a random token of at least 24 characters")
	}
	if *consoleOnly && *readyFile != "" {
		return errors.New("ready-file requires the HTTP API")
	}
	var listener net.Listener
	if !*consoleOnly {
		listener, err = net.Listen("tcp", config.Listen)
		if err != nil {
			return fmt.Errorf("bind API: %w", err)
		}
		defer listener.Close()
	}
	store, err := watch.OpenStore(config.StatePath)
	if err != nil {
		return err
	}
	hub, err := watch.NewFilteredHub(config.Sources)
	if err != nil {
		return err
	}
	hub.RestoreSnapshot(store)
	collector := watch.NewCollector(config, store, hub)
	var summarizer *watch.Summarizer
	if *summaries {
		summarizer, err = watch.NewSummarizer(store, hub)
		if err != nil {
			return err
		}
		collector.OnItem = summarizer.Enqueue
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *readyFile != "" {
		if err := writeReadyFile(*readyFile, listener.Addr().String()); err != nil {
			return err
		}
		defer os.Remove(*readyFile)
	}
	var terminalDone <-chan error
	if *terminal {
		terminalDone = watch.RunTerminal(ctx, store, hub, os.Stdout, *showBaseline)
	}
	var summaryDone chan error
	if summarizer != nil {
		summaryDone = make(chan error, 1)
		go func() { summaryDone <- summarizer.Run(ctx) }()
	}
	var server *http.Server
	var serverDone chan error
	if !*consoleOnly {
		server = &http.Server{
			Addr: config.Listen, Handler: watch.Handler(token, store, hub, collector),
			ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
		}
		serverDone = make(chan error, 1)
		go func() { serverDone <- server.Serve(listener) }()
		slog.Info("HTTP API listening", "address", listener.Addr().String())
	}
	slog.Info("collection starting; live coverage requires successful source checks", "sources", len(config.Sources), "require_tor", *requireTor)
	if !*showBaseline {
		slog.Info("initial source contents establish a baseline; alerts begin with subsequent new/changed entries")
	}
	collectorDone := make(chan error, 1)
	go func() { collectorDone <- collector.Run(ctx) }()
	var result error
	collectorExited := false
	select {
	case <-ctx.Done():
	case result = <-collectorDone:
		collectorExited = true
	case result = <-serverDone:
	case result = <-terminalDone:
	}
	stop()
	if server != nil {
		server.Close()
	}
	if !collectorExited {
		if err := <-collectorDone; result == nil {
			result = err
		}
	}
	if summaryDone != nil {
		if err := <-summaryDone; result == nil {
			result = err
		}
	}
	if errors.Is(result, http.ErrServerClosed) {
		return nil
	}
	return result
}
