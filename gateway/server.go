package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

type Server struct {
	httpServer *http.Server
	updateCh   <-chan *Config
	currentMux *http.ServeMux
	handler    *atomicHandler
}

type atomicHandler struct {
	handler http.Handler
}

func NewServer(cfg *Config, updateCh <-chan *Config) *Server {
	mux := buildMux(cfg)
	h := &atomicHandler{handler: mux}

	s := &Server{
		updateCh: updateCh,
		handler:  h,
		httpServer: &http.Server{
			Addr:    cfg.Server.Listen,
			Handler: h,
		},
	}

	go s.watchUpdates()

	return s
}

func (s *Server) Start() error {
	slog.Info("starting server", "listen", s.httpServer.Addr)
	return s.httpServer.ListenAndServe()
}

func (s *Server) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.httpServer.Shutdown(ctx)
}

func (s *Server) watchUpdates() {
	for cfg := range s.updateCh {
		mux := buildMux(cfg)
		s.handler.handler = mux
		slog.Info("routes updated", "count", len(cfg.Routes))

		if s.httpServer.Addr != cfg.Server.Listen {
			slog.Info("listen address changed, restart required", "old", s.httpServer.Addr, "new", cfg.Server.Listen)
		}
	}
}

func (h *atomicHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handler.ServeHTTP(w, r)
}

func buildMux(cfg *Config) *http.ServeMux {
	mux := http.NewServeMux()

	for _, route := range cfg.Routes {
		handler := newCommandHandler(route, cfg.Server.Timeout)
		mux.Handle(route.Path, handler)
	}

	return mux
}
