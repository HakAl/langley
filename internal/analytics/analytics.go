// Package analytics provides cost calculation, usage metrics, and anomaly detection.
package analytics

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/HakAl/langley/internal/pricing"
)

// Engine provides analytics queries and calculations.
type Engine struct {
	db            *sql.DB
	pricingSource *pricing.Source
}

// NewEngine creates a new analytics engine.
func NewEngine(db *sql.DB) *Engine {
	return &Engine{db: db}
}

// SetPricingSource sets the LiteLLM pricing source for cost calculations.
func (e *Engine) SetPricingSource(source *pricing.Source) {
	e.pricingSource = source
}

// ModelPricing contains pricing data for a model.
type ModelPricing struct {
	Provider           string
	ModelPattern       string
	InputCostPer1k     float64
	OutputCostPer1k    float64
	CacheCreationPer1k *float64
	CacheReadPer1k     *float64
	EffectiveDate      time.Time
}

// GetPricing retrieves pricing for a model.
// It first checks the LiteLLM pricing source, then falls back to the database.
func (e *Engine) GetPricing(ctx context.Context, provider, model string) (*ModelPricing, error) {
	// Try LiteLLM pricing source first
	if e.pricingSource != nil {
		if price := e.pricingSource.GetPrice(provider, model); price != nil {
			return &ModelPricing{
				Provider:           price.Provider,
				ModelPattern:       price.Model,
				InputCostPer1k:     price.InputCostPer1k,
				OutputCostPer1k:    price.OutputCostPer1k,
				CacheCreationPer1k: price.CacheCreationPer1k,
				CacheReadPer1k:     price.CacheReadPer1k,
				EffectiveDate:      time.Now(), // LiteLLM pricing is always current
			}, nil
		}
	}

	// Fall back to database pricing
	row := e.db.QueryRowContext(ctx, `
		SELECT provider, model_pattern, input_cost_per_1k, output_cost_per_1k,
		       cache_creation_per_1k, cache_read_per_1k, effective_date
		FROM pricing
		WHERE provider = ? AND ? LIKE model_pattern
		ORDER BY effective_date DESC
		LIMIT 1
	`, provider, model)

	var pricingResult ModelPricing
	var effectiveDate string
	var cacheCreation, cacheRead sql.NullFloat64

	err := row.Scan(
		&pricingResult.Provider, &pricingResult.ModelPattern,
		&pricingResult.InputCostPer1k, &pricingResult.OutputCostPer1k,
		&cacheCreation, &cacheRead, &effectiveDate,
	)
	if err == sql.ErrNoRows {
		return nil, nil // No pricing found
	}
	if err != nil {
		return nil, err
	}

	pricingResult.EffectiveDate, _ = time.Parse("2006-01-02", effectiveDate)
	if cacheCreation.Valid {
		pricingResult.CacheCreationPer1k = &cacheCreation.Float64
	}
	if cacheRead.Valid {
		pricingResult.CacheReadPer1k = &cacheRead.Float64
	}

	return &pricingResult, nil
}

// CalculateCost computes the cost for token usage.
func (e *Engine) CalculateCost(ctx context.Context, provider, model string, inputTokens, outputTokens, cacheCreation, cacheRead int) (float64, string, error) {
	pricing, err := e.GetPricing(ctx, provider, model)
	if err != nil {
		return 0, "", err
	}
	if pricing == nil {
		return 0, "", nil // No pricing available
	}

	var cost float64
	cost += float64(inputTokens) * pricing.InputCostPer1k / 1000
	cost += float64(outputTokens) * pricing.OutputCostPer1k / 1000

	if pricing.CacheCreationPer1k != nil {
		cost += float64(cacheCreation) * *pricing.CacheCreationPer1k / 1000
	}
	if pricing.CacheReadPer1k != nil {
		cost += float64(cacheRead) * *pricing.CacheReadPer1k / 1000
	}

	return cost, "exact", nil
}

// TaskSummary represents aggregated statistics for a task.
type TaskSummary struct {
	TaskID        string
	FlowCount     int
	TotalTokensIn int
	TotalTokensOut int
	TotalCost     float64
	FirstSeen     time.Time
	LastSeen      time.Time
	DurationMs    int64 // Wall clock time from first to last
	Models        []string
	ToolsUsed     []string
}

// GetTaskSummary returns aggregated metrics for a task.
func (e *Engine) GetTaskSummary(ctx context.Context, taskID string) (*TaskSummary, error) {
	// Get flow aggregates
	row := e.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) as flow_count,
			COALESCE(SUM(input_tokens), 0) as total_in,
			COALESCE(SUM(output_tokens), 0) as total_out,
			COALESCE(SUM(total_cost), 0) as total_cost,
			MIN(timestamp) as first_seen,
			MAX(timestamp) as last_seen,
			GROUP_CONCAT(DISTINCT model) as models
		FROM flows
		WHERE task_id = ?
	`, taskID)

	var summary TaskSummary
	summary.TaskID = taskID
	var firstSeen, lastSeen string
	var models sql.NullString

	err := row.Scan(
		&summary.FlowCount,
		&summary.TotalTokensIn,
		&summary.TotalTokensOut,
		&summary.TotalCost,
		&firstSeen,
		&lastSeen,
		&models,
	)
	if err != nil {
		return nil, err
	}

	summary.FirstSeen, _ = time.Parse(time.RFC3339Nano, firstSeen)
	summary.LastSeen, _ = time.Parse(time.RFC3339Nano, lastSeen)
	summary.DurationMs = summary.LastSeen.Sub(summary.FirstSeen).Milliseconds()

	if models.Valid && models.String != "" {
		summary.Models = strings.Split(models.String, ",")
	}

	// Get tools used
	rows, err := e.db.QueryContext(ctx, `
		SELECT DISTINCT tool_name FROM tool_invocations WHERE task_id = ?
	`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var toolName string
		if err := rows.Scan(&toolName); err != nil {
			return nil, err
		}
		summary.ToolsUsed = append(summary.ToolsUsed, toolName)
	}

	return &summary, rows.Err()
}

// ListTaskSummaries returns summaries for all tasks in a time range.
func (e *Engine) ListTaskSummaries(ctx context.Context, start, end time.Time, limit int) ([]*TaskSummary, error) {
	rows, err := e.db.QueryContext(ctx, `
		SELECT
			task_id,
			COUNT(*) as flow_count,
			COALESCE(SUM(input_tokens), 0) as total_in,
			COALESCE(SUM(output_tokens), 0) as total_out,
			COALESCE(SUM(total_cost), 0) as total_cost,
			MIN(timestamp) as first_seen,
			MAX(timestamp) as last_seen
		FROM flows
		WHERE task_id IS NOT NULL
		  AND timestamp >= ?
		  AND timestamp <= ?
		GROUP BY task_id
		ORDER BY total_cost DESC
		LIMIT ?
	`, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []*TaskSummary
	for rows.Next() {
		var s TaskSummary
		var firstSeen, lastSeen string

		err := rows.Scan(
			&s.TaskID,
			&s.FlowCount,
			&s.TotalTokensIn,
			&s.TotalTokensOut,
			&s.TotalCost,
			&firstSeen,
			&lastSeen,
		)
		if err != nil {
			return nil, err
		}

		s.FirstSeen, _ = time.Parse(time.RFC3339Nano, firstSeen)
		s.LastSeen, _ = time.Parse(time.RFC3339Nano, lastSeen)
		s.DurationMs = s.LastSeen.Sub(s.FirstSeen).Milliseconds()

		summaries = append(summaries, &s)
	}

	return summaries, rows.Err()
}

// ToolStats represents statistics for a tool.
type ToolStats struct {
	ToolName       string
	InvocationCount int
	SuccessCount   int
	FailureCount   int
	SuccessRate    float64
	TotalCost      float64
	AvgDurationMs  float64
	TotalTokensIn  int
	TotalTokensOut int
}

// GetToolStats returns aggregated statistics for tools.
func (e *Engine) GetToolStats(ctx context.Context, start, end time.Time) ([]*ToolStats, error) {
	rows, err := e.db.QueryContext(ctx, `
		SELECT
			tool_name,
			COUNT(*) as invocation_count,
			SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END) as success_count,
			SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END) as failure_count,
			COALESCE(SUM(cost), 0) as total_cost,
			COALESCE(AVG(duration_ms), 0) as avg_duration,
			COALESCE(SUM(input_tokens), 0) as total_in,
			COALESCE(SUM(output_tokens), 0) as total_out
		FROM tool_invocations
		WHERE timestamp >= ? AND timestamp <= ?
		GROUP BY tool_name
		ORDER BY invocation_count DESC
	`, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []*ToolStats
	for rows.Next() {
		var s ToolStats
		err := rows.Scan(
			&s.ToolName,
			&s.InvocationCount,
			&s.SuccessCount,
			&s.FailureCount,
			&s.TotalCost,
			&s.AvgDurationMs,
			&s.TotalTokensIn,
			&s.TotalTokensOut,
		)
		if err != nil {
			return nil, err
		}

		if s.InvocationCount > 0 {
			s.SuccessRate = float64(s.SuccessCount) / float64(s.InvocationCount) * 100
		}

		stats = append(stats, &s)
	}

	return stats, rows.Err()
}

// CostByPeriod represents cost aggregated by time period.
type CostByPeriod struct {
	Period        string // ISO date or hour
	FlowCount     int
	TotalCost     float64
	TotalTokensIn int
	TotalTokensOut int
}

// GetCostByDay returns daily cost breakdown.
func (e *Engine) GetCostByDay(ctx context.Context, start, end time.Time) ([]*CostByPeriod, error) {
	rows, err := e.db.QueryContext(ctx, `
		SELECT
			date(timestamp) as period,
			COUNT(*) as flow_count,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(input_tokens), 0) as total_in,
			COALESCE(SUM(output_tokens), 0) as total_out
		FROM flows
		WHERE timestamp >= ? AND timestamp <= ?
		GROUP BY date(timestamp)
		ORDER BY period
	`, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var periods []*CostByPeriod
	for rows.Next() {
		var p CostByPeriod
		err := rows.Scan(&p.Period, &p.FlowCount, &p.TotalCost, &p.TotalTokensIn, &p.TotalTokensOut)
		if err != nil {
			return nil, err
		}
		periods = append(periods, &p)
	}

	return periods, rows.Err()
}

// GetCostByModel returns cost breakdown by model.
func (e *Engine) GetCostByModel(ctx context.Context, start, end time.Time) ([]*CostByPeriod, error) {
	rows, err := e.db.QueryContext(ctx, `
		SELECT
			COALESCE(model, 'unknown') as period,
			COUNT(*) as flow_count,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(input_tokens), 0) as total_in,
			COALESCE(SUM(output_tokens), 0) as total_out
		FROM flows
		WHERE timestamp >= ? AND timestamp <= ?
		GROUP BY model
		ORDER BY total_cost DESC
	`, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var models []*CostByPeriod
	for rows.Next() {
		var m CostByPeriod
		err := rows.Scan(&m.Period, &m.FlowCount, &m.TotalCost, &m.TotalTokensIn, &m.TotalTokensOut)
		if err != nil {
			return nil, err
		}
		models = append(models, &m)
	}

	return models, rows.Err()
}

// OverallStats represents summary statistics.
type OverallStats struct {
	TotalFlows      int
	TotalCost       float64
	TotalTokensIn   int
	TotalTokensOut  int
	TotalTasks      int
	TotalToolCalls  int
	AvgCostPerFlow  float64
	AvgTokensPerFlow float64
}

// GetOverallStats returns summary statistics for a time range.
func (e *Engine) GetOverallStats(ctx context.Context, start, end time.Time) (*OverallStats, error) {
	var stats OverallStats

	// Flow stats
	row := e.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) as total_flows,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(input_tokens), 0) as total_in,
			COALESCE(SUM(output_tokens), 0) as total_out,
			COUNT(DISTINCT task_id) as total_tasks
		FROM flows
		WHERE timestamp >= ? AND timestamp <= ?
	`, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))

	err := row.Scan(
		&stats.TotalFlows,
		&stats.TotalCost,
		&stats.TotalTokensIn,
		&stats.TotalTokensOut,
		&stats.TotalTasks,
	)
	if err != nil {
		return nil, err
	}

	// Tool invocation count
	row = e.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tool_invocations
		WHERE timestamp >= ? AND timestamp <= ?
	`, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
	_ = row.Scan(&stats.TotalToolCalls)

	// Calculate averages
	if stats.TotalFlows > 0 {
		stats.AvgCostPerFlow = stats.TotalCost / float64(stats.TotalFlows)
		stats.AvgTokensPerFlow = float64(stats.TotalTokensIn+stats.TotalTokensOut) / float64(stats.TotalFlows)
	}

	return &stats, nil
}
