package analytics

import (
	"context"
	"time"
)

// AnomalyType identifies the kind of anomaly detected.
type AnomalyType string

const (
	AnomalyLargeContext   AnomalyType = "large_context"   // Unusually large input token count
	AnomalySlowResponse   AnomalyType = "slow_response"   // Response took longer than threshold
	AnomalyRapidRepeats   AnomalyType = "rapid_repeats"   // Multiple similar requests in short time
	AnomalyHighCost       AnomalyType = "high_cost"       // Single request cost above threshold
	AnomalyToolFailure    AnomalyType = "tool_failure"    // Tool invocation failed
	AnomalyManyToolCalls  AnomalyType = "many_tool_calls" // Unusually high tool call count
	AnomalyDroppedEvents  AnomalyType = "dropped_events"  // Events were dropped due to backpressure
)

// Anomaly represents a detected issue.
type Anomaly struct {
	Type        AnomalyType
	FlowID      string
	TaskID      *string
	Timestamp   time.Time
	Severity    string // 'info', 'warning', 'critical'
	Description string
	Value       float64 // The actual value that triggered the anomaly
	Threshold   float64 // The threshold that was exceeded
}

// AnomalyThresholds configures what triggers anomaly detection.
type AnomalyThresholds struct {
	LargeContextTokens  int           // Input tokens above this = large context
	SlowResponseMs      int64         // Duration above this = slow response
	RapidRepeatWindow   time.Duration // Window for detecting rapid repeats
	RapidRepeatCount    int           // Number of similar requests to trigger
	HighCostDollars     float64       // Cost above this = high cost
	ManyToolCallsCount  int           // Tool calls above this = many tool calls
}

// DefaultThresholds returns sensible default anomaly thresholds.
func DefaultThresholds() *AnomalyThresholds {
	return &AnomalyThresholds{
		LargeContextTokens:  100000, // 100k tokens
		SlowResponseMs:      30000,  // 30 seconds
		RapidRepeatWindow:   60 * time.Second,
		RapidRepeatCount:    5,
		HighCostDollars:     1.0, // $1 per request
		ManyToolCallsCount:  20,
	}
}

// DetectFlowAnomalies checks a single flow for anomalies.
func (e *Engine) DetectFlowAnomalies(ctx context.Context, flowID string, thresholds *AnomalyThresholds) ([]*Anomaly, error) {
	if thresholds == nil {
		thresholds = DefaultThresholds()
	}

	var anomalies []*Anomaly

	// Get flow details
	row := e.db.QueryRowContext(ctx, `
		SELECT id, task_id, timestamp, duration_ms, input_tokens, total_cost, events_dropped_count
		FROM flows WHERE id = ?
	`, flowID)

	var id string
	var taskID *string
	var ts string
	var durationMs, inputTokens, droppedCount *int64
	var totalCost *float64

	err := row.Scan(&id, &taskID, &ts, &durationMs, &inputTokens, &totalCost, &droppedCount)
	if err != nil {
		return nil, err
	}

	timestamp, _ := time.Parse(time.RFC3339Nano, ts)

	// Check large context
	if inputTokens != nil && int(*inputTokens) > thresholds.LargeContextTokens {
		anomalies = append(anomalies, &Anomaly{
			Type:        AnomalyLargeContext,
			FlowID:      flowID,
			TaskID:      taskID,
			Timestamp:   timestamp,
			Severity:    "warning",
			Description: "Input token count exceeds threshold",
			Value:       float64(*inputTokens),
			Threshold:   float64(thresholds.LargeContextTokens),
		})
	}

	// Check slow response
	if durationMs != nil && *durationMs > thresholds.SlowResponseMs {
		anomalies = append(anomalies, &Anomaly{
			Type:        AnomalySlowResponse,
			FlowID:      flowID,
			TaskID:      taskID,
			Timestamp:   timestamp,
			Severity:    "info",
			Description: "Response duration exceeds threshold",
			Value:       float64(*durationMs),
			Threshold:   float64(thresholds.SlowResponseMs),
		})
	}

	// Check high cost
	if totalCost != nil && *totalCost > thresholds.HighCostDollars {
		anomalies = append(anomalies, &Anomaly{
			Type:        AnomalyHighCost,
			FlowID:      flowID,
			TaskID:      taskID,
			Timestamp:   timestamp,
			Severity:    "warning",
			Description: "Single request cost exceeds threshold",
			Value:       *totalCost,
			Threshold:   thresholds.HighCostDollars,
		})
	}

	// Check dropped events
	if droppedCount != nil && *droppedCount > 0 {
		anomalies = append(anomalies, &Anomaly{
			Type:        AnomalyDroppedEvents,
			FlowID:      flowID,
			TaskID:      taskID,
			Timestamp:   timestamp,
			Severity:    "warning",
			Description: "Events dropped due to backpressure",
			Value:       float64(*droppedCount),
			Threshold:   0,
		})
	}

	// Check tool call count
	var toolCount int
	row = e.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tool_invocations WHERE flow_id = ?
	`, flowID)
	_ = row.Scan(&toolCount)

	if toolCount > thresholds.ManyToolCallsCount {
		anomalies = append(anomalies, &Anomaly{
			Type:        AnomalyManyToolCalls,
			FlowID:      flowID,
			TaskID:      taskID,
			Timestamp:   timestamp,
			Severity:    "info",
			Description: "High number of tool calls in single flow",
			Value:       float64(toolCount),
			Threshold:   float64(thresholds.ManyToolCallsCount),
		})
	}

	// Check tool failures
	rows, err := e.db.QueryContext(ctx, `
		SELECT tool_name, error_message FROM tool_invocations
		WHERE flow_id = ? AND success = 0
	`, flowID)
	if err != nil {
		return anomalies, err
	}
	defer rows.Close()

	for rows.Next() {
		var toolName string
		var errorMsg *string
		_ = rows.Scan(&toolName, &errorMsg)

		desc := "Tool invocation failed: " + toolName
		if errorMsg != nil {
			desc += " - " + *errorMsg
		}

		anomalies = append(anomalies, &Anomaly{
			Type:        AnomalyToolFailure,
			FlowID:      flowID,
			TaskID:      taskID,
			Timestamp:   timestamp,
			Severity:    "warning",
			Description: desc,
			Value:       1,
			Threshold:   0,
		})
	}

	return anomalies, nil
}

// DetectRapidRepeats finds flows that look like rapid retries.
func (e *Engine) DetectRapidRepeats(ctx context.Context, thresholds *AnomalyThresholds) ([]*Anomaly, error) {
	if thresholds == nil {
		thresholds = DefaultThresholds()
	}

	// Find flows with same host+path in a short window
	rows, err := e.db.QueryContext(ctx, `
		WITH repeat_groups AS (
			SELECT
				host,
				path,
				task_id,
				COUNT(*) as count,
				MIN(timestamp) as first_ts,
				MAX(timestamp) as last_ts,
				GROUP_CONCAT(id) as flow_ids
			FROM flows
			WHERE timestamp >= datetime('now', ?)
			GROUP BY host, path, task_id
			HAVING COUNT(*) >= ?
		)
		SELECT host, path, task_id, count, first_ts, flow_ids
		FROM repeat_groups
	`, "-"+thresholds.RapidRepeatWindow.String(), thresholds.RapidRepeatCount)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var anomalies []*Anomaly
	for rows.Next() {
		var host, path, firstTs, flowIDs string
		var taskID *string
		var count int

		err := rows.Scan(&host, &path, &taskID, &count, &firstTs, &flowIDs)
		if err != nil {
			continue
		}

		timestamp, _ := time.Parse(time.RFC3339Nano, firstTs)

		anomalies = append(anomalies, &Anomaly{
			Type:        AnomalyRapidRepeats,
			FlowID:      flowIDs, // Contains all IDs
			TaskID:      taskID,
			Timestamp:   timestamp,
			Severity:    "warning",
			Description: "Multiple similar requests to " + host + path,
			Value:       float64(count),
			Threshold:   float64(thresholds.RapidRepeatCount),
		})
	}

	return anomalies, rows.Err()
}

// ListRecentAnomalies returns anomalies from recent flows.
func (e *Engine) ListRecentAnomalies(ctx context.Context, since time.Time, thresholds *AnomalyThresholds) ([]*Anomaly, error) {
	if thresholds == nil {
		thresholds = DefaultThresholds()
	}

	var allAnomalies []*Anomaly

	// Get recent flow IDs
	rows, err := e.db.QueryContext(ctx, `
		SELECT id FROM flows WHERE timestamp >= ? ORDER BY timestamp DESC LIMIT 100
	`, since.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var flowIDs []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		flowIDs = append(flowIDs, id)
	}

	// Check each flow for anomalies
	for _, flowID := range flowIDs {
		anomalies, err := e.DetectFlowAnomalies(ctx, flowID, thresholds)
		if err != nil {
			continue
		}
		allAnomalies = append(allAnomalies, anomalies...)
	}

	// Also check for rapid repeats
	rapidRepeats, err := e.DetectRapidRepeats(ctx, thresholds)
	if err == nil {
		allAnomalies = append(allAnomalies, rapidRepeats...)
	}

	// Check drop_log for recent drops
	dropAnomalies, err := e.getDropLogAnomalies(ctx, since)
	if err == nil {
		allAnomalies = append(allAnomalies, dropAnomalies...)
	}

	return allAnomalies, nil
}

// getDropLogAnomalies surfaces recent dropped events from the drop_log table.
func (e *Engine) getDropLogAnomalies(ctx context.Context, since time.Time) ([]*Anomaly, error) {
	// Aggregate drops by flow and priority
	rows, err := e.db.QueryContext(ctx, `
		SELECT
			flow_id,
			priority,
			COUNT(*) as drop_count,
			MIN(timestamp) as first_drop,
			MAX(timestamp) as last_drop,
			GROUP_CONCAT(DISTINCT reason) as reasons
		FROM drop_log
		WHERE timestamp >= ?
		GROUP BY flow_id, priority
		ORDER BY drop_count DESC
		LIMIT 50
	`, since.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var anomalies []*Anomaly
	for rows.Next() {
		var flowID *string
		var priority string
		var dropCount int
		var firstDrop, lastDrop, reasons string

		if err := rows.Scan(&flowID, &priority, &dropCount, &firstDrop, &lastDrop, &reasons); err != nil {
			continue
		}

		timestamp, _ := time.Parse(time.RFC3339Nano, firstDrop)

		// Determine severity based on priority of dropped events
		severity := "info"
		if priority == "high" {
			severity = "critical"
		} else if priority == "medium" {
			severity = "warning"
		}

		desc := "Dropped " + priority + " priority events"
		if reasons != "" {
			desc += ": " + reasons
		}

		fid := ""
		if flowID != nil {
			fid = *flowID
		}

		anomalies = append(anomalies, &Anomaly{
			Type:        AnomalyDroppedEvents,
			FlowID:      fid,
			Timestamp:   timestamp,
			Severity:    severity,
			Description: desc,
			Value:       float64(dropCount),
			Threshold:   0, // Any drop is notable
		})
	}

	return anomalies, rows.Err()
}
