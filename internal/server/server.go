package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mylxsw/asteria/log"
	"github.com/mylxsw/openai-cost-optimal-gateway/internal/config"
	"github.com/mylxsw/openai-cost-optimal-gateway/internal/gateway"
	internalmw "github.com/mylxsw/openai-cost-optimal-gateway/internal/middleware"
	"github.com/mylxsw/openai-cost-optimal-gateway/internal/storage"
)

// getEnv fetches environment variable value; returns empty string if not set.
func getEnv(key string) string {
	if v, ok := lookupEnv(key); ok {
		return v
	}
	return ""
}

// lookupEnv allows us to patch during tests if needed.
var lookupEnv = func(key string) (string, bool) { return os.LookupEnv(key) }

type Server struct {
	cfg     *config.Config
	gateway *gateway.Gateway
	auth    *internalmw.APIKeyAuth
	httpSrv *http.Server
	usage   storage.Store
}

func New(cfg *config.Config, gw *gateway.Gateway, usage storage.Store) *Server {
	return &Server{
		cfg:     cfg,
		gateway: gw,
		auth:    internalmw.NewAPIKeyAuth(cfg.APIKeys),
		usage:   usage,
	}
}

func (s *Server) Run(ctx context.Context) error {
	handler := s.buildHandler()
	// allow PORT env var to override the listen port, common for cloud envs
	listen := s.cfg.Listen
	if port := strings.TrimSpace(getEnv("PORT")); port != "" {
		// if listen is host:port, replace port; if only port provided in env, use :PORT
		if strings.Contains(listen, ":") {
			parts := strings.Split(listen, ":")
			// join all but last as host
			host := strings.Join(parts[:len(parts)-1], ":")
			if host == "" {
				listen = ":" + port
			} else {
				listen = host + ":" + port
			}
		} else {
			listen = ":" + port
		}
		log.Infof("PORT env detected, overriding listen to %s", listen)
	}
	s.httpSrv = &http.Server{
		Addr:              listen,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}

	// Start cleanup goroutine if usage tracking and cleanup are enabled
	if s.cfg.SaveUsage && s.usage != nil && s.cfg.CleanupEnabled {
		go s.startCleanupTask(ctx)
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Errorf("http server shutdown: %v", err)
		}
	}()

	log.Infof("listening on %s", listen)
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

	// Handle common static resources
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /"))
	})

	mux.Handle("/v1/chat/completions", http.HandlerFunc(s.handleChatCompletions))
	mux.Handle("/v1/responses", http.HandlerFunc(s.handleResponses))
	mux.Handle("/v1/messages", http.HandlerFunc(s.handleAnthropicMessages))
	mux.Handle("/v1/models", http.HandlerFunc(s.handleModels))

	if s.cfg.SaveUsage && s.usage != nil {
		mux.Handle("/usage", http.HandlerFunc(s.handleUsage))
		if dashboardHandler := newDashboardHandler(); dashboardHandler != nil {
			mux.Handle("/dashboard", dashboardHandler)
			mux.Handle("/dashboard/", dashboardHandler)
		}
	}

	return chain(mux, s.auth.MiddlewareWithSkipper(s.shouldSkipAuth), recoverMiddleware, loggingMiddleware)
}

func (s *Server) shouldSkipAuth(r *http.Request) bool {
	if r.Method == http.MethodGet {
		if r.URL.Path == "/healthz" {
			return true
		}
		if strings.HasPrefix(r.URL.Path, "/dashboard") {
			return true
		}
		// Skip authentication for common static resources
		staticPaths := []string{
			"/favicon.ico",
			"/robots.txt",
			"/sitemap.xml",
		}
		for _, path := range staticPaths {
			if r.URL.Path == path {
				return true
			}
		}
	}
	return false
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

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	if s.usage == nil {
		http.Error(w, "usage tracking disabled", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}

	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	requestID := strings.TrimSpace(r.URL.Query().Get("request_id"))
	records, err := s.usage.QueryUsage(r.Context(), storage.UsageQuery{Limit: limit, RequestID: requestID})
	if err != nil {
		http.Error(w, "query usage records: "+err.Error(), http.StatusInternalServerError)
		return
	}

	summary := usageSummary{}
	summary.TotalRequests = len(records)
	for _, rec := range records {
		summary.TotalPromptTokens += rec.RequestTokens
		summary.TotalCompletionTokens += rec.ResponseTokens
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(usageResponse{Data: records, Summary: summary})
}

type usageSummary struct {
	TotalRequests         int `json:"total_requests"`
	TotalPromptTokens     int `json:"total_prompt_tokens"`
	TotalCompletionTokens int `json:"total_completion_tokens"`
}

type usageResponse struct {
	Data    []storage.UsageRecord `json:"data"`
	Summary usageSummary          `json:"summary"`
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
		log.Debugf("%s %s %s", r.Method, r.URL.Path, duration)
	})
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Errorf("panic recovered: %v", rec)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) startCleanupTask(ctx context.Context) {
	// Get retention period from config, default to 3 days
	retentionDays := s.cfg.RetentionDays
	if retentionDays <= 0 {
		retentionDays = 3
	}

	// Cleanup interval: default every 6 hours, configurable via config
	intervalHours := s.cfg.CleanupIntervalHours
	if intervalHours <= 0 {
		intervalHours = 6
	}
	interval := time.Duration(intervalHours) * time.Hour
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Infof("usage records cleanup task started: retention=%d days, interval=%dh", retentionDays, intervalHours)

	// Run cleanup immediately on startup
	s.performCleanup(ctx, retentionDays)

	for {
		select {
		case <-ctx.Done():
			log.Infof("cleanup task stopped")
			return
		case <-ticker.C:
			s.performCleanup(ctx, retentionDays)
		}
	}
}

func (s *Server) performCleanup(ctx context.Context, retentionDays int) {
	if s.usage == nil {
		return
	}

	log.Infof("starting cleanup of usage records older than %d days", retentionDays)

	deleted, err := s.usage.CleanupOldRecords(ctx, retentionDays)
	if err != nil {
		log.Errorf("cleanup old records failed: %v", err)
		return
	}

	if deleted > 0 {
		log.Infof("cleanup completed: deleted %d old usage records", deleted)
	} else {
		log.Debugf("cleanup completed: no old records to delete")
	}
}
