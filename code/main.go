// Command citewise runs the CiteWise AI web server.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"citewise/internal/ai"
	"citewise/internal/config"
	"citewise/internal/essay"
	"citewise/internal/providers"
	"citewise/internal/providers/crossref"
	"citewise/internal/providers/openalex"
	"citewise/internal/search"
	"citewise/internal/store"
	"citewise/internal/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	if err := config.LoadDotEnv(".env"); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := store.Open(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()

	searchSvc := &search.Service{
		Providers: []providers.Provider{
			crossref.New(cfg.CrossrefMailto),
			openalex.New(cfg.OpenAlexKey, cfg.CrossrefMailto),
		},
		Log: log,
	}
	var essays web.EssayAnalyzer
	if cfg.AIEnabled() {
		client := ai.New(cfg.OpenAIKey, cfg.OpenAIModel)
		searchSvc.AI = client
		essays = &essay.Service{AI: client, Search: searchSvc, Log: log}
	}

	srv, err := web.New(web.Options{Config: cfg, Store: db, Searcher: searchSvc, Essays: essays, Logger: log})
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      5 * time.Minute, // essay analysis can take a while
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("CiteWise AI listening", "url", "http://localhost:"+cfg.Port, "database", cfg.DatabasePath,
			"ai_enabled", cfg.AIEnabled(), "model", cfg.OpenAIModel)
		if !cfg.AIEnabled() {
			log.Info("OPENAI_API_KEY not set: title search uses keyword queries; essay analysis is disabled")
		}
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutCtx)
	}
	return nil
}
