package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/gemini"
	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/httpapi"
	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/report"
	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/store"
	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/worker"
)

const (
	defaultListenAddress = ":8080"
	defaultDatabasePath  = "./data/homekeeper.db"
	defaultMaxBodyBytes  = 1 << 20
	defaultGeminiModel   = gemini.DefaultModel
)

type config struct {
	listenAddress    string
	databasePath     string
	sharedToken      string
	contractMajor    string
	maxBodyBytes     int64
	geminiConfigured bool
	geminiAPIKey     string
	geminiModel      string
	timezone         string
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	ctx := context.Background()
	var llmClient report.LLMClient
	if cfg.geminiAPIKey != "" {
		client, clientErr := gemini.New(ctx, cfg.geminiAPIKey, cfg.geminiModel)
		if clientErr != nil {
			logger.Warn("Gemini is not available", "error", clientErr)
		} else {
			llmClient = client
			cfg.geminiConfigured = true
		}
	}

	if err := os.MkdirAll(filepathDir(cfg.databasePath), 0o750); err != nil {
		logger.Error("create database directory", "error", err)
		os.Exit(1)
	}

	database, err := store.Open(ctx, cfg.databasePath)
	if err != nil {
		logger.Error("database startup failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	handlers, err := httpapi.NewServer(httpapi.Config{
		SharedToken:      cfg.sharedToken,
		ContractMajor:    cfg.contractMajor,
		MaxBodyBytes:     cfg.maxBodyBytes,
		GeminiConfigured: cfg.geminiConfigured,
	}, database)
	if err != nil {
		logger.Error("HTTP startup failed", "error", err)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:              cfg.listenAddress,
		Handler:           handlers.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	shutdownContext, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	location := time.Local
	if cfg.timezone != "" {
		if configured, locationErr := time.LoadLocation(cfg.timezone); locationErr != nil {
			logger.Warn("invalid HOMEKEEPER_TIMEZONE; using local timezone", "timezone", cfg.timezone, "error", locationErr)
		} else {
			location = configured
		}
	}
	reportWorker := &worker.ReportWorker{Store: database, LLM: llmClient, Location: location, Model: cfg.geminiModel, Interval: 30 * time.Second}
	go func() {
		if workerErr := reportWorker.Run(shutdownContext); workerErr != nil && !errors.Is(workerErr, context.Canceled) {
			logger.Error("report worker stopped", "error", workerErr)
		}
	}()
	go func() {
		maintenanceTicker := time.NewTicker(6 * time.Hour)
		defer maintenanceTicker.Stop()
		for {
			select {
			case <-shutdownContext.Done():
				return
			case now := <-maintenanceTicker.C:
				if purgeErr := database.PurgeRetention(shutdownContext, now); purgeErr != nil {
					logger.Warn("retention purge failed", "error", purgeErr)
				}
			}
		}
	}()
	go func() {
		<-shutdownContext.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			logger.Error("HTTP shutdown failed", "error", err)
		}
	}()

	logger.Info("analytics sidecar listening", "address", cfg.listenAddress, "database", cfg.databasePath)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}
}

func loadConfig() (config, error) {
	cfg := config{
		listenAddress: defaultListenAddress,
		databasePath:  defaultDatabasePath,
		contractMajor: "1",
		maxBodyBytes:  defaultMaxBodyBytes,
		geminiModel:   defaultGeminiModel,
	}
	if value := strings.TrimSpace(os.Getenv("HOMEKEEPER_LISTEN_ADDR")); value != "" {
		cfg.listenAddress = value
	}
	if value := strings.TrimSpace(os.Getenv("HOMEKEEPER_DB_PATH")); value != "" {
		cfg.databasePath = value
	}
	cfg.sharedToken = strings.TrimSpace(os.Getenv("HOMEKEEPER_SHARED_TOKEN"))
	if cfg.sharedToken == "" {
		return config{}, errors.New("HOMEKEEPER_SHARED_TOKEN is required")
	}
	if value := strings.TrimSpace(os.Getenv("HOMEKEEPER_CONTRACT_MAJOR")); value != "" {
		cfg.contractMajor = value
	}
	if value := strings.TrimSpace(os.Getenv("HOMEKEEPER_MAX_BODY_BYTES")); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 1 {
			return config{}, fmt.Errorf("HOMEKEEPER_MAX_BODY_BYTES must be a positive integer")
		}
		cfg.maxBodyBytes = parsed
	}
	cfg.geminiAPIKey = strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if value := strings.TrimSpace(os.Getenv("HOMEKEEPER_GEMINI_MODEL")); value != "" {
		cfg.geminiModel = value
	}
	if !strings.HasPrefix(cfg.geminiModel, "gemini-") || len(cfg.geminiModel) <= len("gemini-") || len(cfg.geminiModel) > 128 {
		return config{}, errors.New("HOMEKEEPER_GEMINI_MODEL must be a gemini-* model name")
	}
	cfg.timezone = strings.TrimSpace(os.Getenv("HOMEKEEPER_TIMEZONE"))
	return cfg, nil
}

func filepathDir(path string) string {
	index := strings.LastIndexAny(path, `/\\`)
	if index < 0 {
		return "."
	}
	if index == 0 {
		return path[:1]
	}
	return path[:index]
}
