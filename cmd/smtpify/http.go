package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// HTTPServer serves the minimal operational API (/health, /stats).
type HTTPServer struct {
	Host   string
	Port   int
	Queue  *Queue
	Logger *Logger
	start  time.Time

	srv *http.Server
	ln  net.Listener
}

// NewHTTPServer constructs an HTTPServer. start defaults to now when zero.
func NewHTTPServer(host string, port int, q *Queue, logger *Logger) *HTTPServer {
	return &HTTPServer{
		Host:   host,
		Port:   port,
		Queue:  q,
		Logger: logger,
		start:  time.Now(),
	}
}

func (h *HTTPServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", h.handleHealth)
	mux.HandleFunc("/stats", h.handleStats)
	return mux
}

func (h *HTTPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.Queue == nil {
		w.WriteHeader(http.StatusInternalServerError)
		encodeJSON(w, map[string]string{"status": "error"})
		return
	}
	w.WriteHeader(http.StatusOK)
	encodeJSON(w, map[string]string{"status": "ok"})
}

func (h *HTTPServer) handleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	stats, err := h.Queue.Stats()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		encodeJSON(w, map[string]string{"status": "error"})
		return
	}
	stats.Uptime = time.Since(h.start).Round(time.Second).String()
	w.WriteHeader(http.StatusOK)
	encodeJSON(w, stats)
}

// Listen starts the server on the configured host/port.
func (h *HTTPServer) Listen() error {
	addr := net.JoinHostPort(h.Host, fmt.Sprintf("%d", h.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("http listen %s: %w", addr, err)
	}
	h.ln = ln
	h.srv = &http.Server{
		Handler:           h.handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		_ = h.srv.Serve(ln)
	}()
	return nil
}

// Shutdown gracefully stops the HTTP server.
func (h *HTTPServer) Shutdown(ctx context.Context) error {
	if h.srv == nil {
		return nil
	}
	return h.srv.Shutdown(ctx)
}

func encodeJSON(w http.ResponseWriter, v interface{}) {
	_ = json.NewEncoder(w).Encode(v)
}
