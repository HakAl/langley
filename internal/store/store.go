// Package store provides data persistence using SQLite.
package store

import (
	"context"
	"slices"
	"strings"
	"time"
)

// Flow represents an HTTP request/response pair.
type Flow struct {
	ID                    string
	TaskID                *string
	TaskSource            *string // 'explicit', 'metadata', 'inferred'
	Host                  string
	Method                string
	Path                  string
	URL                   string
	Timestamp             time.Time
	TimestampMono         int64
	DurationMs            *int64
	StatusCode            *int
	StatusText            *string
	IsSSE                 bool
	FlowIntegrity         string // 'complete', 'partial', 'corrupted', 'interrupted'
	EventsDroppedCount    int
	RequestBody           *string
	RequestBodyTruncated  bool
	ResponseBody          *string
	ResponseBodyTruncated bool
	RequestHeaders        map[string][]string
	ResponseHeaders       map[string][]string
	RequestSignature      *string
	InputTokens           *int
	OutputTokens          *int
	CacheCreationTokens   *int
	CacheReadTokens       *int
	TotalCost             *float64
	CostSource            *string // 'exact', 'estimated'
	Model                 *string
	Provider              string // 'anthropic', 'bedrock', 'other'
	CreatedAt             time.Time
	ExpiresAt             *time.Time
}

// Event represents an SSE event.
type Event struct {
	ID            string
	FlowID        string
	Sequence      int
	Timestamp     time.Time
	TimestampMono int64
	EventType     string
	EventData     map[string]interface{}
	Priority      string // 'high', 'medium', 'low'
	CreatedAt     time.Time
	ExpiresAt     *time.Time
}

// ToolInvocation represents a tool usage record.
type ToolInvocation struct {
	ID           string
	FlowID       string
	TaskID       *string
	ToolUseID    *string // Claude tool_use_id for correlating with tool_result
	ToolName     string
	ToolType     *string
	Timestamp    time.Time
	DurationMs   *int64
	Success      *bool
	ErrorMessage *string
	ToolInput    *string // JSON of tool input parameters
	ToolResult   *string // JSON of tool result content
	InputTokens  *int
	OutputTokens *int
	Cost         *float64
	CreatedAt    time.Time
	ExpiresAt    *time.Time
}

// DropLogEntry represents a dropped event record.
type DropLogEntry struct {
	ID        int64
	FlowID    *string
	EventType *string
	Priority  string
	Reason    string
	Timestamp time.Time
}

// FlowFilter defines filter criteria for flow queries.
type FlowFilter struct {
	Host       *string
	TaskID     *string
	TaskSource *string
	Model      string  // case-insensitive substring match on model
	Method     string  // case-insensitive exact match on HTTP method
	Path       string  // case-insensitive substring match on path
	Status     *string // "success" (status_code < 400) or "error" (status_code >= 400)
	StartTime  *time.Time
	EndTime    *time.Time
	SortBy     string // see FlowSortColumns; empty means timestamp
	SortDir    string // "asc" or "desc" (default "desc")
	Limit      int
	Offset     int
}

// FlowSortColumns lists the accepted FlowFilter.SortBy values.
var FlowSortColumns = []string{
	"timestamp", "cost", "total_cost", "tokens", "total_tokens",
	"input_tokens", "output_tokens", "duration", "duration_ms",
}

// IsValidFlowSort reports whether sortBy and sortDir are accepted values.
// Empty values are valid and select the defaults.
func IsValidFlowSort(sortBy, sortDir string) bool {
	if sortBy != "" && !slices.Contains(FlowSortColumns, sortBy) {
		return false
	}
	switch strings.ToLower(sortDir) {
	case "", "asc", "desc":
		return true
	}
	return false
}

// Store defines the interface for data persistence.
// This follows the Dependency Inversion Principle - depend on abstractions.
type Store interface {
	// Flows
	SaveFlow(ctx context.Context, flow *Flow) error
	UpdateFlow(ctx context.Context, flow *Flow) error
	GetFlow(ctx context.Context, id string) (*Flow, error)
	ListFlows(ctx context.Context, filter FlowFilter) ([]*Flow, error)
	CountFlows(ctx context.Context, filter FlowFilter) (int, error)
	DeleteFlow(ctx context.Context, id string) error

	// Events
	SaveEvent(ctx context.Context, event *Event) error
	SaveEvents(ctx context.Context, events []*Event) error
	GetEventsByFlow(ctx context.Context, flowID string) ([]*Event, error)

	// Tool Invocations
	SaveToolInvocation(ctx context.Context, inv *ToolInvocation) error
	GetToolInvocationsByFlow(ctx context.Context, flowID string) ([]*ToolInvocation, error)
	GetToolInvocation(ctx context.Context, id string) (*ToolInvocation, error)
	ListToolInvocations(ctx context.Context, toolName string, start, end time.Time, limit, offset int) ([]*ToolInvocation, int, error)
	UpdateToolResult(ctx context.Context, toolUseID string, success bool, errorMsg *string, resultContent *string, resultTime time.Time) error

	// Drop Log
	LogDrop(ctx context.Context, entry *DropLogEntry) error

	// Maintenance
	RunRetention(ctx context.Context) (deleted int64, err error)
	Close() error

	// DB returns the underlying database connection for analytics queries.
	DB() interface{}
}
