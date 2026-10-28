// Package web contains the HTTP handlers, templates and static assets.
package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"citewise/internal/config"
	"citewise/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Server holds handler dependencies.
type Server struct {
	cfg       config.Config
	store     *store.Store
	searcher  Searcher
	essays    EssayAnalyzer
	doi       DOIResolver
	log       *slog.Logger
	pages     map[string]*template.Template
	currentYr int
}

// Options configures a Server.
type Options struct {
	Config   config.Config
	Store    *store.Store
	Searcher Searcher
	Essays   EssayAnalyzer
	DOI      DOIResolver
	Logger   *slog.Logger
}

// New creates a Server and parses all templates.
func New(opts Options) (*Server, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Server{
		cfg:       opts.Config,
		store:     opts.Store,
		searcher:  opts.Searcher,
		essays:    opts.Essays,
		doi:       opts.DOI,
		log:       opts.Logger,
		pages:     map[string]*template.Template{},
		currentYr: time.Now().Year(),
	}
	entries, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	for _, path := range entries {
		name := strings.TrimPrefix(path, "templates/")
		if name == "layout.html" {
			continue
		}
		t, err := template.New(name).Funcs(templateFuncs).ParseFS(templateFS, "templates/layout.html", path)
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", name, err)
		}
		s.pages[name] = t
	}
	return s, nil
}

// Handler returns the application's HTTP handler with middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))

	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /references/search", s.handleSearchForm)
	mux.HandleFunc("POST /references/search", s.handleSearch)
	mux.HandleFunc("GET /references/upload", s.handleUploadForm)
	mux.HandleFunc("POST /references/upload", s.handleUpload)
	mux.HandleFunc("GET /references/results/{id}", s.handleResults)
	mux.HandleFunc("GET /references/saved", s.handleSaved)
	mux.HandleFunc("POST /references/save", s.handleSave)
	mux.HandleFunc("POST /references/doi", s.handleAddDOI)
	mux.HandleFunc("POST /references/export", s.handleExport)
	mux.HandleFunc("/", s.handleNotFound)

	return s.recoverer(s.sameOrigin(s.securityHeaders(s.logRequests(mux))))
}

// View is the data passed to every page template.
type View struct {
	Title     string
	Nav       string
	AIEnabled bool
	Page      any
}

func (s *Server) render(w http.ResponseWriter, status int, page, title, nav string, data any) {
	t, ok := s.pages[page]
	if !ok {
		s.log.Error("missing template", "page", page)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	view := View{Title: title, Nav: nav, AIEnabled: s.cfg.AIEnabled(), Page: data}
	if err := t.ExecuteTemplate(&buf, "layout", view); err != nil {
		s.log.Error("render template", "page", page, "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

type errorPage struct {
	Status  int
	Message string
	Back    string
}

func (s *Server) renderError(w http.ResponseWriter, status int, message, back string) {
	if back == "" {
		back = "/"
	}
	s.render(w, status, "error.html", http.StatusText(status), "", errorPage{Status: status, Message: message, Back: back})
}

type homePage struct {
	Recent []store.Search
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	recent, err := s.store.ListSearches(r.Context(), 8)
	if err != nil {
		s.log.Error("list searches", "err", err)
	}
	s.render(w, http.StatusOK, "home.html", "CiteWise AI", "home", homePage{Recent: recent})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	status, dbStatus, code := "ok", "ok", http.StatusOK
	if err := s.store.Ping(ctx); err != nil {
		status, dbStatus, code = "degraded", "unavailable", http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"status":     status,
		"database":   dbStatus,
		"ai_enabled": s.cfg.AIEnabled(),
	})
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.renderError(w, http.StatusNotFound, "The page you requested does not exist.", "/")
}

// safeReturn returns path if it is a local path, otherwise fallback.
func safeReturn(path, fallback string) string {
	if path == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.HasPrefix(path, "/\\") {
		return fallback
	}
	u, err := url.Parse(path)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return fallback
	}
	return path
}

// --- middleware ---

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "duration", time.Since(start).Round(time.Millisecond))
		}
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// sameOrigin rejects cross-site POSTs (basic CSRF protection for an app
// without accounts).
func (s *Server) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host {
					s.renderError(w, http.StatusForbidden, "Cross-site form submissions are not allowed.", "/")
					return
				}
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				s.renderError(w, http.StatusForbidden, "Cross-site form submissions are not allowed.", "/")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic in handler", "path", r.URL.Path, "panic", v)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
