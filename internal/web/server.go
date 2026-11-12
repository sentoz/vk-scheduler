package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
	"github.com/sentoz/vk-scheduler/internal/calendar"
	"github.com/sentoz/vk-scheduler/internal/config"
	"github.com/sentoz/vk-scheduler/internal/metrics"
	"github.com/sentoz/vk-scheduler/internal/resource"
	"github.com/sentoz/vk-scheduler/internal/vars"
)

//go:embed static/*
var assets embed.FS

// Server provides lifecycle probes, metrics, and a read-only calendar UI.
type Server struct {
	server   *http.Server
	listener net.Listener
	ready    atomic.Bool
	errors   chan error
	done     chan struct{}
	cache    *statusCache
	snapshot func() *config.Snapshot
	config   config.Config
}

// New binds synchronously so a port conflict is a startup failure.
func New(ctx context.Context, address string, cfg config.Config, snapshot func() *config.Snapshot, checker resource.StateChecker, m *metrics.Metrics) (*Server, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	s := &Server{listener: listener, errors: make(chan error, 1), done: make(chan struct{}), snapshot: snapshot, config: cfg, cache: &statusCache{ctx: ctx, timeout: cfg.Cloud.RequestTimeout.Std(), checker: checker, entries: map[string]cacheEntry{}, flights: map[cacheKey]*flight{}}}
	mux := http.NewServeMux()
	live := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
	}
	mux.HandleFunc("GET /health", live)
	mux.HandleFunc("GET /health/live", live)
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, _ *http.Request) {
		if !s.ready.Load() {
			writeJSON(w, 503, map[string]string{"status": "not_ready"})
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /info", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, vars.Info()) })
	if cfg.MetricsEnabled {
		mux.Handle("GET /metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}))
	}
	if cfg.UIEnabled {
		sub, subErr := fs.Sub(assets, "static")
		if subErr != nil {
			closeErr := listener.Close()
			return nil, errors.Join(subErr, closeErr)
		}
		mux.Handle("GET /", http.FileServer(http.FS(sub)))
		mux.HandleFunc("GET /api/calendar", s.calendar)
	}
	s.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second}
	return s, nil
}

// SetReady changes readiness after startup or before shutdown.
func (s *Server) SetReady(ready bool) { s.ready.Store(ready) }

// Address returns the bound listener address, including an allocated test port.
func (s *Server) Address() string { return s.listener.Addr().String() }

// Errors reports an unexpected Serve failure.
func (s *Server) Errors() <-chan error { return s.errors }

// Start serves HTTP on the already-bound listener.
func (s *Server) Start() {
	go func() {
		defer close(s.done)
		if err := s.server.Serve(s.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.errors <- err
		}
	}()
}

// Shutdown drains HTTP handlers within the shared hard deadline.
func (s *Server) Shutdown(ctx context.Context) error {
	s.SetReady(false)
	err := s.server.Shutdown(ctx)
	if err != nil {
		err = errors.Join(err, s.server.Close())
	}
	joined := make(chan struct{})
	go func() { <-s.done; s.cache.workers.Wait(); close(joined) }()
	select {
	case <-joined:
		return err
	case <-ctx.Done():
		return errors.Join(err, ctx.Err())
	}
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Debug().Err(err).Msg("Write HTTP response")
	}
}

func (s *Server) calendar(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	snapshot := s.snapshot()
	if snapshot == nil {
		writeJSON(w, 503, map[string]string{"error": "Schedules unavailable"})
		return
	}
	start, end, err := calendar.Range(r.URL.Query().Get("from"), r.URL.Query().Get("to"), time.Now(), snapshot.Location)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	events, err := calendar.Events(ctx, snapshot, start, end)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "Calendar range exceeds limits or is unavailable"})
		return
	}
	statuses := map[string]status{}
	for i := range events {
		if r.Context().Err() != nil {
			return
		}
		event := &events[i]
		item, ok := statuses[event.ResourceKey]
		if !ok {
			item = s.cache.get(ctx, snapshot.Generation, snapshot.Groups[event.ResourceKey].Resource)
			statuses[event.ResourceKey] = item
		}
		event.State, event.Transitional, event.StatusError = item.state, item.transitional, item.failure
		if pending, ok := s.cache.checker.(interface{ Unconfirmed(string) bool }); ok && pending.Unconfirmed(event.ResourceKey) {
			event.OperationPending = true
			event.StatusError = "Cloud operation awaits stable confirmation"
		}
	}
	writeJSON(w, 200, map[string]any{"events": events, "timezone": snapshot.Location.String(), "generation": snapshot.Generation, "validation_resources": s.config.ValidationResources, "validation_interval": s.config.ValidationInterval.String()})
}
