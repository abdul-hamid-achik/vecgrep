// Package web provides the HTTP server and web UI for vecgrep.
package web

import (
	stdembed "embed"
	"io/fs"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	vembed "github.com/abdul-hamid-achik/vecgrep/internal/embed"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
)

//go:embed static/*
var staticFS stdembed.FS

// ServerConfig holds configuration for the web server.
type ServerConfig struct {
	Host        string
	Port        int
	DB          *db.DB
	Provider    vembed.Provider
	ProjectRoot string
}

// Server is the HTTP server for the web UI.
type Server struct {
	config   ServerConfig
	router   *chi.Mux
	handler  *Handler
	searcher *search.Searcher
}

// NewServer creates a new web server.
func NewServer(cfg ServerConfig) *Server {
	s := &Server{
		config:   cfg,
		router:   chi.NewRouter(),
		searcher: search.NewSearcher(cfg.DB, cfg.Provider),
	}

	s.handler = NewHandler(s.searcher, cfg.ProjectRoot)
	s.setupMiddleware()
	s.setupRoutes()

	return s
}

// setupMiddleware configures middleware for the router.
func (s *Server) setupMiddleware() {
	// Request ID
	s.router.Use(middleware.RequestID)

	// Real IP
	s.router.Use(middleware.RealIP)

	// Logger
	s.router.Use(middleware.Logger)

	// Recoverer
	s.router.Use(middleware.Recoverer)

	// Timeout
	s.router.Use(middleware.Timeout(60 * time.Second))

	// Compress
	s.router.Use(middleware.Compress(5))
}

// setupRoutes configures routes for the server.
func (s *Server) setupRoutes() {
	// Serve static files
	staticContent, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Printf("Warning: failed to load static files: %v", err)
	} else {
		s.router.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticContent))))
	}

	// Web UI routes
	s.router.Get("/", s.handler.Index)
	s.router.Get("/search", s.handler.Search)
	s.router.Get("/status", s.handler.Status)

	// API routes
	s.router.Route("/api", func(r chi.Router) {
		r.Get("/search", s.handler.APISearch)
		r.Get("/status", s.handler.APIStatus)
		r.Get("/health", s.handler.Health)
	})
}

// Router returns the chi router for external use.
func (s *Server) Router() *chi.Mux {
	return s.router
}

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe() error {
	addr := s.config.Host + ":" + itoa(s.config.Port)
	log.Printf("Starting web server on http://%s", addr)
	return http.ListenAndServe(addr, s.router)
}

// itoa converts int to string (simple helper to avoid strconv import).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
