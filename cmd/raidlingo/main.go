package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"raidlingo/internal/raidlingo"
	"syscall"
	"time"
)

func run() error {
	production := flag.Bool("production", false, "serve built Preact apps")
	lanIP := flag.String("lan-ip", "", "serve built apps on your private IPv4 address; keep the registered Twitch callback")
	demo := flag.Bool("demo", false, "run local Twitch/OpenAI simulation without credentials")
	check := flag.Bool("check-live", false, "check Twitch app credentials and a sample translation without posting to chat")
	root := flag.String("root", raidlingo.FindRoot(), "project root containing .env and apps")
	flag.Parse()
	if !*demo {
		if err := raidlingo.LoadEnv(filepath.Join(*root, ".env")); err != nil {
			return err
		}
	}
	config, err := raidlingo.LoadConfig(*root, os.Getenv, *production, *demo)
	if err != nil {
		return err
	}
	if *lanIP != "" {
		if err := config.EnableLAN(*lanIP); err != nil {
			return err
		}
	}
	if *check {
		if *demo {
			return errors.New("--check-live cannot be combined with --demo")
		}
		return checkLive(config)
	}
	level := slog.LevelInfo
	if config.LogLevel == "debug" {
		level = slog.LevelDebug
	}
	if config.LogLevel == "error" || config.LogLevel == "silent" {
		level = slog.LevelError
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	var simulator *raidlingo.Simulator
	var mock http.Handler
	if *demo {
		directory, err := os.MkdirTemp("", "raidlingo-demo-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(directory)
		config.TokenPath = filepath.Join(directory, "tokens.json")
		simulator = raidlingo.NewSimulator()
		simulator.Configure(&config)
		mock = simulator
		defer simulator.Close()
	}
	store, err := raidlingo.OpenTokenStore(config.TokenPath)
	if err != nil {
		return err
	}
	session := raidlingo.NewSession(config, store, nil, nil, nil, logger)
	app := raidlingo.NewApp(session, mock)
	session.Start()
	defer session.Close()
	if simulator != nil {
		simulator.Start()
	}
	server := &http.Server{Addr: app.Address(), Handler: app, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	if *demo {
		logger.Info("LOCAL SIMULATION ONLY — authorize mock streamers and bot", "desk", config.SeatURL)
	} else {
		logger.Info("Raidlingo ready — connect both streamers and relay bot", "desk", config.SeatURL, "overlay", config.OverlayURL)
	}
	select {
	case err := <-done:
		if err != http.ErrServerClosed {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		session.Close()
		return server.Shutdown(shutdown)
	}
	return nil
}
func checkLive(config raidlingo.Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 15 * time.Second}
	var failures []error
	oauth := &raidlingo.OAuth{Config: config, HTTP: client}
	if _, err := oauth.AppAccess(ctx, false); err != nil {
		failures = append(failures, fmt.Errorf("Twitch application check: %w", err))
	} else {
		fmt.Println("Twitch application credentials: accepted (account authorization is still required)")
	}
	started := time.Now()
	translator := raidlingo.NewTranslator(raidlingo.NewTranslationProvider(config, client))
	result, err := translator.Translate(ctx, raidlingo.TranslationRequest{Text: "Hello from Raidlingo!", Source: "en", Target: "es", Hints: []string{"Raidlingo"}})
	if err != nil {
		failures = append(failures, fmt.Errorf("translation check: %w", err))
	} else {
		bundle, _ := json.Marshal(result.Bundle)
		fmt.Printf("Translation (%s): passed in %.1fs — %s\n", config.TranslationProvider, time.Since(started).Seconds(), bundle)
	}
	return errors.Join(failures...)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Raidlingo startup failed:", err)
		os.Exit(1)
	}
}
