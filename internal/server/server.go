package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/mylxsw/openai-cost-optimal-gateway/internal/config"
	"github.com/mylxsw/openai-cost-optimal-gateway/internal/gateway"
	internalmw "github.com/mylxsw/openai-cost-optimal-gateway/internal/middleware"
)

type Server struct {
	cfg     *config.Config
	gateway *gateway.Gateway
	auth    *internalmw.APIKeyAuth
	httpSrv *http.Server
}

func New(cfg *config.Config, gw *gateway.Gateway) *Server {
	return &Server{
		cfg:     cfg,
		gateway: gw,
		auth:    internalmw.NewAPIKeyAuth(cfg.APIKeys),
	}
}

func (s *Server) Run(ctx context.Context) error {
	handler := s.buildHandler()
	s.httpSrv = &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("http server shutdown: %v", err)
		}
	}()

	log.Printf("listening on %s", s.cfg.Listen)
	err := s.httpSrv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.Handle("/v1/chat/completions", http.HandlerFunc(s.handleChatCompletions))
	mux.Handle("/v1/responses", http.HandlerFunc(s.handleResponses))
	mux.Handle("/v1/messages", http.HandlerFunc(s.handleAnthropicMessages))
	mux.Handle("/v1/models", http.HandlerFunc(s.handleModels))

	return chain(mux, s.auth.Middleware, recoverMiddleware, loggingMiddleware)
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	s.gateway.Proxy(w, r, gateway.RequestTypeChatCompletions)
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	s.gateway.Proxy(w, r, gateway.RequestTypeResponses)
}

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	s.gateway.Proxy(w, r, gateway.RequestTypeAnthropicMessages)
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	response := s.gateway.ModelList()
	_ = json.NewEncoder(w).Encode(response)
}

func methodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

type middleware func(http.Handler) http.Handler

func chain(h http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		duration := time.Since(start)
		log.Printf("%s %s %s", r.Method, r.URL.Path, duration)
	})
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic recovered: %v", rec)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
