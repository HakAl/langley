// Package api provides the REST API for Langley.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/langley/internal/config"
	"github.com/anthropics/langley/internal/store"
)

// Server is the REST API server.
type Server struct {
	cfg    *config.Config
	store  store.Store
	logger *slog.Logger
	mux    *http.ServeMux
}

// NewServer creates a new API server.
func NewServer(cfg *config.Config, dataStore store.Store, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}

	s := &Server{
		cfg:    cfg,
		store:  dataStore,
		logger: logger,
		mux:    http.NewServeMux(),
	}

	// Register routes
	s.mux.HandleFunc("GET /api/flows", s.authMiddleware(s.listFlows))
	s.mux.HandleFunc("GET /api/flows/{id}", s.authMiddleware(s.getFlow))
	s.mux.HandleFunc("GET /api/flows/{id}/events", s.authMiddleware(s.getFlowEvents))
	s.mux.HandleFunc("GET /api/stats", s.authMiddleware(s.getStats))
	s.mux.HandleFunc("GET /api/health", s.healthCheck)

	return s
}

// Handler returns the HTTP handler for the API.
func (s *Server) Handler() http.Handler {
	return s.corsMiddleware(s.mux)
}

// authMiddleware wraps a handler with bearer token authentication.
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check Authorization header
		auth := r.Header.Get("Authorization")
		if auth == "" {
			// Also check query param for WebSocket compatibility
			auth = "Bearer " + r.URL.Query().Get("token")
		}

		expected := "Bearer " + s.cfg.Auth.Token
		if auth != expected {
			s.logger.Debug("auth failed", "provided", auth[:min(len(auth), 20)])
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}

// corsMiddleware adds CORS headers for local development.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// Only allow localhost origins (addresses langley-yni origin validation)
		if origin != "" {
			if strings.HasPrefix(origin, "http://localhost") ||
				strings.HasPrefix(origin, "http://127.0.0.1") ||
				strings.HasPrefix(origin, "https://localhost") ||
				strings.HasPrefix(origin, "https://127.0.0.1") {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				w.Header().Set("Access-Control-Max-Age", "86400")
			}
		}

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// listFlows returns a paginated list of flows.
func (s *Server) listFlows(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Parse query params
	filter := store.FlowFilter{
		Limit:  50,
		Offset: 0,
	}

	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			filter.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			filter.Offset = n
		}
	}
	if v := r.URL.Query().Get("host"); v != "" {
		filter.Host = &v
	}
	if v := r.URL.Query().Get("task_id"); v != "" {
		filter.TaskID = &v
	}
	if v := r.URL.Query().Get("model"); v != "" {
		filter.Model = &v
	}
	if v := r.URL.Query().Get("start_time"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.StartTime = &t
		}
	}
	if v := r.URL.Query().Get("end_time"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.EndTime = &t
		}
	}

	flows, err := s.store.ListFlows(ctx, filter)
	if err != nil {
		s.logger.Error("failed to list flows", "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Convert to API response format
	response := make([]FlowSummary, len(flows))
	for i, f := range flows {
		response[i] = toFlowSummary(f)
	}

	s.writeJSON(w, response)
}

// getFlow returns a single flow by ID.
func (s *Server) getFlow(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing flow ID", http.StatusBadRequest)
		return
	}

	flow, err := s.store.GetFlow(ctx, id)
	if err != nil {
		s.logger.Error("failed to get flow", "id", id, "error", err)
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	s.writeJSON(w, toFlowDetail(flow))
}

// getFlowEvents returns events for a flow.
func (s *Server) getFlowEvents(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing flow ID", http.StatusBadRequest)
		return
	}

	events, err := s.store.GetEventsByFlow(ctx, id)
	if err != nil {
		s.logger.Error("failed to get events", "flow_id", id, "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	response := make([]EventResponse, len(events))
	for i, e := range events {
		response[i] = toEventResponse(e)
	}

	s.writeJSON(w, response)
}

// getStats returns aggregate statistics.
func (s *Server) getStats(w http.ResponseWriter, r *http.Request) {
	// For now, return basic stats
	// Full analytics will be added in Phase 3
	stats := StatsResponse{
		Status:    "ok",
		Timestamp: time.Now(),
	}
	s.writeJSON(w, stats)
}

// healthCheck returns server health status.
func (s *Server) healthCheck(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.logger.Error("failed to encode JSON response", "error", err)
	}
}

// API response types

// FlowSummary is the summary view of a flow.
type FlowSummary struct {
	ID           string     `json:"id"`
	Host         string     `json:"host"`
	Method       string     `json:"method"`
	Path         string     `json:"path"`
	StatusCode   *int       `json:"status_code,omitempty"`
	IsSSE        bool       `json:"is_sse"`
	Timestamp    time.Time  `json:"timestamp"`
	DurationMs   *int64     `json:"duration_ms,omitempty"`
	TaskID       *string    `json:"task_id,omitempty"`
	TaskSource   *string    `json:"task_source,omitempty"`
	Model        *string    `json:"model,omitempty"`
	InputTokens  *int       `json:"input_tokens,omitempty"`
	OutputTokens *int       `json:"output_tokens,omitempty"`
	TotalCost    *float64   `json:"total_cost,omitempty"`
}

// FlowDetail is the detailed view of a flow.
type FlowDetail struct {
	FlowSummary
	URL                   string              `json:"url"`
	StatusText            *string             `json:"status_text,omitempty"`
	Provider              string              `json:"provider"`
	FlowIntegrity         string              `json:"flow_integrity"`
	EventsDroppedCount    int                 `json:"events_dropped_count"`
	RequestBody           *string             `json:"request_body,omitempty"`
	RequestBodyTruncated  bool                `json:"request_body_truncated"`
	ResponseBody          *string             `json:"response_body,omitempty"`
	ResponseBodyTruncated bool                `json:"response_body_truncated"`
	RequestHeaders        map[string][]string `json:"request_headers,omitempty"`
	ResponseHeaders       map[string][]string `json:"response_headers,omitempty"`
	CacheCreationTokens   *int                `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens       *int                `json:"cache_read_tokens,omitempty"`
	CostSource            *string             `json:"cost_source,omitempty"`
}

// EventResponse is the API response for an event.
type EventResponse struct {
	ID        string                 `json:"id"`
	Sequence  int                    `json:"sequence"`
	Timestamp time.Time              `json:"timestamp"`
	EventType string                 `json:"event_type"`
	EventData map[string]interface{} `json:"event_data,omitempty"`
	Priority  string                 `json:"priority"`
}

// StatsResponse is the API response for stats.
type StatsResponse struct {
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
}

func toFlowSummary(f *store.Flow) FlowSummary {
	return FlowSummary{
		ID:           f.ID,
		Host:         f.Host,
		Method:       f.Method,
		Path:         f.Path,
		StatusCode:   f.StatusCode,
		IsSSE:        f.IsSSE,
		Timestamp:    f.Timestamp,
		DurationMs:   f.DurationMs,
		TaskID:       f.TaskID,
		TaskSource:   f.TaskSource,
		Model:        f.Model,
		InputTokens:  f.InputTokens,
		OutputTokens: f.OutputTokens,
		TotalCost:    f.TotalCost,
	}
}

func toFlowDetail(f *store.Flow) FlowDetail {
	return FlowDetail{
		FlowSummary:           toFlowSummary(f),
		URL:                   f.URL,
		StatusText:            f.StatusText,
		Provider:              f.Provider,
		FlowIntegrity:         f.FlowIntegrity,
		EventsDroppedCount:    f.EventsDroppedCount,
		RequestBody:           f.RequestBody,
		RequestBodyTruncated:  f.RequestBodyTruncated,
		ResponseBody:          f.ResponseBody,
		ResponseBodyTruncated: f.ResponseBodyTruncated,
		RequestHeaders:        f.RequestHeaders,
		ResponseHeaders:       f.ResponseHeaders,
		CacheCreationTokens:   f.CacheCreationTokens,
		CacheReadTokens:       f.CacheReadTokens,
		CostSource:            f.CostSource,
	}
}

func toEventResponse(e *store.Event) EventResponse {
	return EventResponse{
		ID:        e.ID,
		Sequence:  e.Sequence,
		Timestamp: e.Timestamp,
		EventType: e.EventType,
		EventData: e.EventData,
		Priority:  e.Priority,
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
