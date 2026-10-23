// Package server serves the JSON API and the embedded frontend. It is
// read-only: every route is GET, and nothing it calls changes the box.
package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/state"
)

// Source is where the server gets its data: the live collector or the demo.
type Source interface {
	State() state.State
	History(ctx context.Context, rng string) (state.History, error)
	Journal(project string) (state.ProjectJournal, error)
}

// Server wires routes to a Source.
type Server struct {
	src     Source
	assets  fs.FS
	log     *slog.Logger
	version string
}

// New returns the HTTP handler.
func New(src Source, assets fs.FS, version string, log *slog.Logger) http.Handler {
	s := &Server{src: src, assets: assets, log: log, version: version}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET /api/history", s.history)
	mux.HandleFunc("GET /api/journal", s.journal)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.Handle("GET /", http.FileServerFS(assets))
	return s.guard(mux)
}

// guard rejects non-GET methods and foreign Host headers. The Host check
// stops a web page using DNS rebinding to read the API through the tunnel.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		if !localHost(r.Host) {
			s.log.Warn("server: refused foreign host header", "host", r.Host, "remote", r.RemoteAddr)
			http.Error(w, "use http://localhost:<port> through the SSH tunnel", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func localHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Warn("server: write response", "err", err)
	}
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.src.State())
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "1h"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	h, err := s.src.History(ctx, rng)
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, h)
}

func (s *Server) journal(w http.ResponseWriter, r *http.Request) {
	j, err := s.src.Journal(r.URL.Query().Get("project"))
	if err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, j)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
}
