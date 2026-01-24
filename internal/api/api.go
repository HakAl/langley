// Package api provides the REST API for Langley.
package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/langley/internal/analytics"
	"github.com/anthropics/langley/internal/config"
	"github.com/anthropics/langley/internal/store"
)

// Server is the REST API server.
type Server struct {
	cfg       *config.Config
	store     store.Store
	analytics *analytics.Engine
	logger    *slog.Logger
	mux       *http.ServeMux
	startTime time.Time
}

// NewServer creates a new API server.
func NewServer(cfg *config.Config, dataStore store.Store, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}

	s := &Server{
		cfg:       cfg,
		store:     dataStore,
		logger:    logger,
		mux:       http.NewServeMux(),
		startTime: time.Now(),
	}

	// Initialize analytics engine if we have a database connection
	if db, ok := dataStore.DB().(*sql.DB); ok {
		s.analytics = analytics.NewEngine(db)
	}

	// Register routes
	s.mux.HandleFunc("GET /api/flows", s.authMiddleware(s.listFlows))
	s.mux.HandleFunc("GET /api/flows/{id}", s.authMiddleware(s.getFlow))
	s.mux.HandleFunc("GET /api/flows/{id}/events", s.authMiddleware(s.getFlowEvents))
	s.mux.HandleFunc("GET /api/flows/{id}/anomalies", s.authMiddleware(s.getFlowAnomalies))
	s.mux.HandleFunc("GET /api/stats", s.authMiddleware(s.getStats))
	s.mux.HandleFunc("GET /api/analytics/tasks", s.authMiddleware(s.getTaskAnalytics))
	s.mux.HandleFunc("GET /api/analytics/tasks/{id}", s.authMiddleware(s.getTaskSummary))
	s.mux.HandleFunc("GET /api/analytics/tools", s.authMiddleware(s.getToolAnalytics))
	s.mux.HandleFunc("GET /api/analytics/cost/daily", s.authMiddleware(s.getCostByDay))
	s.mux.HandleFunc("GET /api/analytics/cost/model", s.authMiddleware(s.getCostByModel))
	s.mux.HandleFunc("GET /api/analytics/anomalies", s.authMiddleware(s.getAnomalies))
	s.mux.HandleFunc("GET /api/health", s.healthCheck)
	s.mux.HandleFunc("POST /api/checkpoint", s.authMiddleware(s.checkpoint))

	return s
}

// Handler returns the HTTP handler for the API.
func (s *Server) Handler() http.Handler {
	return s.corsMiddleware(s.mux)
}

// authMiddleware wraps a handler with bearer token authentication.
// Uses constant-time comparison to prevent timing attacks.
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check Authorization header
		auth := r.Header.Get("Authorization")
		if auth == "" {
			// Also check query param for WebSocket compatibility
			auth = "Bearer " + r.URL.Query().Get("token")
		}

		expected := "Bearer " + s.cfg.Auth.Token

		// Use constant-time comparison to prevent timing attacks
		if subtle.ConstantTimeCompare([]byte(auth), []byte(expected)) != 1 {
			s.logger.Debug("auth failed", "provided_len", len(auth))
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
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Parse time range (default: last 24 hours)
	start, end := s.parseTimeRange(r)

	if s.analytics == nil {
		s.writeJSON(w, StatsResponse{Status: "analytics_unavailable", Timestamp: time.Now()})
		return
	}

	stats, err := s.analytics.GetOverallStats(ctx, start, end)
	if err != nil {
		s.logger.Error("failed to get stats", "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, OverallStatsResponse{
		Status:           "ok",
		Timestamp:        time.Now(),
		TotalFlows:       stats.TotalFlows,
		TotalCost:        stats.TotalCost,
		TotalTokensIn:    stats.TotalTokensIn,
		TotalTokensOut:   stats.TotalTokensOut,
		TotalTasks:       stats.TotalTasks,
		TotalToolCalls:   stats.TotalToolCalls,
		AvgCostPerFlow:   stats.AvgCostPerFlow,
		AvgTokensPerFlow: stats.AvgTokensPerFlow,
		StartTime:        start,
		EndTime:          end,
	})
}

// getTaskAnalytics returns task-level analytics.
func (s *Server) getTaskAnalytics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if s.analytics == nil {
		http.Error(w, "Analytics unavailable", http.StatusServiceUnavailable)
		return
	}

	start, end := s.parseTimeRange(r)
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	summaries, err := s.analytics.ListTaskSummaries(ctx, start, end, limit)
	if err != nil {
		s.logger.Error("failed to get task summaries", "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	response := make([]TaskSummaryResponse, len(summaries))
	for i, ts := range summaries {
		response[i] = TaskSummaryResponse{
			TaskID:         ts.TaskID,
			FlowCount:      ts.FlowCount,
			TotalTokensIn:  ts.TotalTokensIn,
			TotalTokensOut: ts.TotalTokensOut,
			TotalCost:      ts.TotalCost,
			FirstSeen:      ts.FirstSeen,
			LastSeen:       ts.LastSeen,
			DurationMs:     ts.DurationMs,
			Models:         ts.Models,
			ToolsUsed:      ts.ToolsUsed,
		}
	}

	s.writeJSON(w, response)
}

// getTaskSummary returns analytics for a single task.
func (s *Server) getTaskSummary(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if s.analytics == nil {
		http.Error(w, "Analytics unavailable", http.StatusServiceUnavailable)
		return
	}

	taskID := r.PathValue("id")
	if taskID == "" {
		http.Error(w, "Missing task ID", http.StatusBadRequest)
		return
	}

	summary, err := s.analytics.GetTaskSummary(ctx, taskID)
	if err != nil {
		s.logger.Error("failed to get task summary", "task_id", taskID, "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, TaskSummaryResponse{
		TaskID:         summary.TaskID,
		FlowCount:      summary.FlowCount,
		TotalTokensIn:  summary.TotalTokensIn,
		TotalTokensOut: summary.TotalTokensOut,
		TotalCost:      summary.TotalCost,
		FirstSeen:      summary.FirstSeen,
		LastSeen:       summary.LastSeen,
		DurationMs:     summary.DurationMs,
		Models:         summary.Models,
		ToolsUsed:      summary.ToolsUsed,
	})
}

// getToolAnalytics returns tool usage analytics.
func (s *Server) getToolAnalytics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if s.analytics == nil {
		http.Error(w, "Analytics unavailable", http.StatusServiceUnavailable)
		return
	}

	start, end := s.parseTimeRange(r)

	stats, err := s.analytics.GetToolStats(ctx, start, end)
	if err != nil {
		s.logger.Error("failed to get tool stats", "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	response := make([]ToolStatsResponse, len(stats))
	for i, ts := range stats {
		response[i] = ToolStatsResponse{
			ToolName:        ts.ToolName,
			InvocationCount: ts.InvocationCount,
			SuccessCount:    ts.SuccessCount,
			FailureCount:    ts.FailureCount,
			SuccessRate:     ts.SuccessRate,
			TotalCost:       ts.TotalCost,
			AvgDurationMs:   ts.AvgDurationMs,
			TotalTokensIn:   ts.TotalTokensIn,
			TotalTokensOut:  ts.TotalTokensOut,
		}
	}

	s.writeJSON(w, response)
}

// getCostByDay returns daily cost breakdown.
func (s *Server) getCostByDay(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if s.analytics == nil {
		http.Error(w, "Analytics unavailable", http.StatusServiceUnavailable)
		return
	}

	start, end := s.parseTimeRange(r)

	periods, err := s.analytics.GetCostByDay(ctx, start, end)
	if err != nil {
		s.logger.Error("failed to get daily costs", "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	response := make([]CostPeriodResponse, len(periods))
	for i, p := range periods {
		response[i] = CostPeriodResponse{
			Period:         p.Period,
			FlowCount:      p.FlowCount,
			TotalCost:      p.TotalCost,
			TotalTokensIn:  p.TotalTokensIn,
			TotalTokensOut: p.TotalTokensOut,
		}
	}

	s.writeJSON(w, response)
}

// getCostByModel returns cost breakdown by model.
func (s *Server) getCostByModel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if s.analytics == nil {
		http.Error(w, "Analytics unavailable", http.StatusServiceUnavailable)
		return
	}

	start, end := s.parseTimeRange(r)

	models, err := s.analytics.GetCostByModel(ctx, start, end)
	if err != nil {
		s.logger.Error("failed to get model costs", "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	response := make([]CostPeriodResponse, len(models))
	for i, m := range models {
		response[i] = CostPeriodResponse{
			Period:         m.Period, // Model name
			FlowCount:      m.FlowCount,
			TotalCost:      m.TotalCost,
			TotalTokensIn:  m.TotalTokensIn,
			TotalTokensOut: m.TotalTokensOut,
		}
	}

	s.writeJSON(w, response)
}

// getFlowAnomalies returns anomalies for a specific flow.
func (s *Server) getFlowAnomalies(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if s.analytics == nil {
		http.Error(w, "Analytics unavailable", http.StatusServiceUnavailable)
		return
	}

	flowID := r.PathValue("id")
	if flowID == "" {
		http.Error(w, "Missing flow ID", http.StatusBadRequest)
		return
	}

	anomalies, err := s.analytics.DetectFlowAnomalies(ctx, flowID, nil)
	if err != nil {
		s.logger.Error("failed to detect anomalies", "flow_id", flowID, "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	response := make([]AnomalyResponse, len(anomalies))
	for i, a := range anomalies {
		response[i] = toAnomalyResponse(a)
	}

	s.writeJSON(w, response)
}

// getAnomalies returns recent anomalies.
func (s *Server) getAnomalies(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if s.analytics == nil {
		http.Error(w, "Analytics unavailable", http.StatusServiceUnavailable)
		return
	}

	// Default to last hour
	since := time.Now().Add(-1 * time.Hour)
	if v := r.URL.Query().Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			since = t
		}
	}

	anomalies, err := s.analytics.ListRecentAnomalies(ctx, since, nil)
	if err != nil {
		s.logger.Error("failed to list anomalies", "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	response := make([]AnomalyResponse, len(anomalies))
	for i, a := range anomalies {
		response[i] = toAnomalyResponse(a)
	}

	s.writeJSON(w, response)
}

// parseTimeRange extracts start/end times from query params (default: last 24 hours).
func (s *Server) parseTimeRange(r *http.Request) (start, end time.Time) {
	end = time.Now()
	start = end.Add(-24 * time.Hour)

	if v := r.URL.Query().Get("start"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			start = t
		}
	}
	if v := r.URL.Query().Get("end"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			end = t
		}
	}

	return start, end
}

// healthCheck returns server health status with operational metrics.
func (s *Server) healthCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	health := HealthResponse{
		Status:    "ok",
		Timestamp: time.Now(),
		Uptime:    time.Since(s.startTime).String(),
	}

	// Get WAL info and queue stats from database
	if db, ok := s.store.DB().(*sql.DB); ok {
		// WAL file size
		var walPages, walCheckpointed int64
		row := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)")
		if err := row.Scan(new(int), &walPages, &walCheckpointed); err == nil {
			// Each WAL page is typically 4096 bytes
			health.WALSizeBytes = walPages * 4096
			health.WALCheckpointed = walCheckpointed * 4096
		}

		// Drop count
		var dropCount int64
		row = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM drop_log WHERE timestamp > datetime('now', '-24 hours')")
		row.Scan(&dropCount)
		health.DropsLast24h = dropCount

		// Active flows (recent 5 minutes)
		var activeFlows int
		row = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM flows WHERE timestamp > datetime('now', '-5 minutes')")
		row.Scan(&activeFlows)
		health.ActiveFlows = activeFlows

		// Total flows
		var totalFlows int64
		row = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM flows")
		row.Scan(&totalFlows)
		health.TotalFlows = totalFlows

		// Database file size
		var pageCount, pageSize int64
		db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount)
		db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize)
		health.DBSizeBytes = pageCount * pageSize
	}

	// Set status based on health indicators
	if health.DropsLast24h > 100 {
		health.Status = "degraded"
		health.Warning = "High drop rate in last 24h"
	}
	if health.WALSizeBytes > 100*1024*1024 { // 100MB WAL is concerning
		health.Status = "degraded"
		health.Warning = "Large WAL file - consider checkpoint"
	}

	s.writeJSON(w, health)
}

// checkpoint triggers a WAL checkpoint to free up disk space.
// Rate limited to prevent abuse.
func (s *Server) checkpoint(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	db, ok := s.store.DB().(*sql.DB)
	if !ok {
		http.Error(w, "Database unavailable", http.StatusServiceUnavailable)
		return
	}

	// Get WAL size before checkpoint
	var walPagesBefore int64
	db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(new(int), &walPagesBefore, new(int))

	// Run TRUNCATE checkpoint (most aggressive - blocks briefly but reclaims space)
	var blocked, walPagesLog, walPagesCheckpointed int
	err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&blocked, &walPagesLog, &walPagesCheckpointed)
	if err != nil {
		s.logger.Error("checkpoint failed", "error", err)
		http.Error(w, "Checkpoint failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Get WAL size after checkpoint
	var walPagesAfter int64
	db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(new(int), &walPagesAfter, new(int))

	response := CheckpointResponse{
		Success:          blocked == 0,
		WALSizeBefore:    walPagesBefore * 4096,
		WALSizeAfter:     walPagesAfter * 4096,
		PagesLog:         walPagesLog,
		PagesCheckpointed: walPagesCheckpointed,
		Blocked:          blocked == 1,
		Timestamp:        time.Now(),
	}

	if blocked == 1 {
		response.Message = "Checkpoint was blocked by active readers"
	} else {
		response.Message = "Checkpoint completed successfully"
	}

	s.logger.Info("WAL checkpoint", "pages_before", walPagesBefore, "pages_after", walPagesAfter, "blocked", blocked)
	s.writeJSON(w, response)
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

// OverallStatsResponse is the detailed stats response.
type OverallStatsResponse struct {
	Status           string    `json:"status"`
	Timestamp        time.Time `json:"timestamp"`
	TotalFlows       int       `json:"total_flows"`
	TotalCost        float64   `json:"total_cost"`
	TotalTokensIn    int       `json:"total_tokens_in"`
	TotalTokensOut   int       `json:"total_tokens_out"`
	TotalTasks       int       `json:"total_tasks"`
	TotalToolCalls   int       `json:"total_tool_calls"`
	AvgCostPerFlow   float64   `json:"avg_cost_per_flow"`
	AvgTokensPerFlow float64   `json:"avg_tokens_per_flow"`
	StartTime        time.Time `json:"start_time"`
	EndTime          time.Time `json:"end_time"`
}

// TaskSummaryResponse is the API response for task analytics.
type TaskSummaryResponse struct {
	TaskID         string    `json:"task_id"`
	FlowCount      int       `json:"flow_count"`
	TotalTokensIn  int       `json:"total_tokens_in"`
	TotalTokensOut int       `json:"total_tokens_out"`
	TotalCost      float64   `json:"total_cost"`
	FirstSeen      time.Time `json:"first_seen"`
	LastSeen       time.Time `json:"last_seen"`
	DurationMs     int64     `json:"duration_ms"`
	Models         []string  `json:"models,omitempty"`
	ToolsUsed      []string  `json:"tools_used,omitempty"`
}

// ToolStatsResponse is the API response for tool analytics.
type ToolStatsResponse struct {
	ToolName        string  `json:"tool_name"`
	InvocationCount int     `json:"invocation_count"`
	SuccessCount    int     `json:"success_count"`
	FailureCount    int     `json:"failure_count"`
	SuccessRate     float64 `json:"success_rate"`
	TotalCost       float64 `json:"total_cost"`
	AvgDurationMs   float64 `json:"avg_duration_ms"`
	TotalTokensIn   int     `json:"total_tokens_in"`
	TotalTokensOut  int     `json:"total_tokens_out"`
}

// CostPeriodResponse is the API response for cost breakdowns.
type CostPeriodResponse struct {
	Period         string  `json:"period"`
	FlowCount      int     `json:"flow_count"`
	TotalCost      float64 `json:"total_cost"`
	TotalTokensIn  int     `json:"total_tokens_in"`
	TotalTokensOut int     `json:"total_tokens_out"`
}

// AnomalyResponse is the API response for anomalies.
type AnomalyResponse struct {
	Type        string    `json:"type"`
	FlowID      string    `json:"flow_id"`
	TaskID      *string   `json:"task_id,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
	Severity    string    `json:"severity"`
	Description string    `json:"description"`
	Value       float64   `json:"value"`
	Threshold   float64   `json:"threshold"`
}

// HealthResponse is the API response for health status.
type HealthResponse struct {
	Status         string    `json:"status"` // "ok", "degraded", "error"
	Timestamp      time.Time `json:"timestamp"`
	Uptime         string    `json:"uptime"`
	WALSizeBytes   int64     `json:"wal_size_bytes"`
	WALCheckpointed int64    `json:"wal_checkpointed_bytes"`
	DropsLast24h   int64     `json:"drops_last_24h"`
	ActiveFlows    int       `json:"active_flows"` // Flows in last 5 minutes
	TotalFlows     int64     `json:"total_flows"`
	DBSizeBytes    int64     `json:"db_size_bytes"`
	Warning        string    `json:"warning,omitempty"`
}

// CheckpointResponse is the API response for WAL checkpoint operations.
type CheckpointResponse struct {
	Success           bool      `json:"success"`
	Message           string    `json:"message"`
	WALSizeBefore     int64     `json:"wal_size_before_bytes"`
	WALSizeAfter      int64     `json:"wal_size_after_bytes"`
	PagesLog          int       `json:"pages_in_log"`
	PagesCheckpointed int       `json:"pages_checkpointed"`
	Blocked           bool      `json:"was_blocked"`
	Timestamp         time.Time `json:"timestamp"`
}

func toAnomalyResponse(a *analytics.Anomaly) AnomalyResponse {
	return AnomalyResponse{
		Type:        string(a.Type),
		FlowID:      a.FlowID,
		TaskID:      a.TaskID,
		Timestamp:   a.Timestamp,
		Severity:    a.Severity,
		Description: a.Description,
		Value:       a.Value,
		Threshold:   a.Threshold,
	}
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
