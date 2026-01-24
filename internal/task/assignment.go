// Package task provides task boundary detection and assignment.
// This implements the layered approach from REVIEW.md:
// Priority 1: X-Langley-Task header (explicit)
// Priority 2: request.metadata.user_id (metadata)
// Priority 3: host + idle gap heuristic (inferred)
package task

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

const (
	// SourceExplicit means task was set via X-Langley-Task header.
	SourceExplicit = "explicit"
	// SourceMetadata means task was extracted from request metadata.
	SourceMetadata = "metadata"
	// SourceInferred means task was assigned by heuristic.
	SourceInferred = "inferred"

	// TaskHeader is the header used for explicit task assignment.
	TaskHeader = "X-Langley-Task"

	// DefaultIdleGapMinutes is the default idle gap for task boundaries.
	DefaultIdleGapMinutes = 5
)

// Assignment represents a task assignment.
type Assignment struct {
	TaskID string
	Source string // explicit, metadata, inferred
}

// Assigner assigns tasks to flows.
type Assigner struct {
	mu            sync.Mutex
	lastActivity  map[string]time.Time // host -> last activity time
	lastTaskID    map[string]string    // host -> last task ID
	idleGap       time.Duration
	taskCounter   int
}

// AssignerConfig configures the task assigner.
type AssignerConfig struct {
	IdleGapMinutes int
}

// NewAssigner creates a new task assigner.
func NewAssigner(cfg AssignerConfig) *Assigner {
	idleGap := DefaultIdleGapMinutes
	if cfg.IdleGapMinutes > 0 {
		idleGap = cfg.IdleGapMinutes
	}

	return &Assigner{
		lastActivity: make(map[string]time.Time),
		lastTaskID:   make(map[string]string),
		idleGap:      time.Duration(idleGap) * time.Minute,
	}
}

// Assign determines the task for a request.
func (a *Assigner) Assign(host string, headers http.Header, body []byte) *Assignment {
	// Priority 1: Explicit header
	if taskID := headers.Get(TaskHeader); taskID != "" {
		return &Assignment{
			TaskID: taskID,
			Source: SourceExplicit,
		}
	}

	// Priority 2: Metadata from request body
	if taskID := extractMetadataTaskID(body); taskID != "" {
		return &Assignment{
			TaskID: taskID,
			Source: SourceMetadata,
		}
	}

	// Priority 3: Heuristic based on host + idle gap
	return a.assignByHeuristic(host)
}

// assignByHeuristic assigns a task using the idle gap heuristic.
func (a *Assigner) assignByHeuristic(host string) *Assignment {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	lastTime, hasLast := a.lastActivity[host]

	// Check if this is a new task (first request or idle gap exceeded)
	newTask := !hasLast || now.Sub(lastTime) > a.idleGap

	if newTask {
		// Generate new task ID
		a.taskCounter++
		taskID := generateTaskID(host, a.taskCounter)
		a.lastTaskID[host] = taskID
	}

	// Update last activity
	a.lastActivity[host] = now

	return &Assignment{
		TaskID: a.lastTaskID[host],
		Source: SourceInferred,
	}
}

// extractMetadataTaskID extracts task ID from request body metadata.
// Claude API requests can include metadata.user_id which we use for task grouping.
func extractMetadataTaskID(body []byte) string {
	if len(body) == 0 {
		return ""
	}

	var request struct {
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}

	if err := json.Unmarshal(body, &request); err != nil {
		return ""
	}

	return request.Metadata.UserID
}

// generateTaskID generates a task ID for heuristic assignment.
func generateTaskID(host string, counter int) string {
	// Use a simple format: host-derived prefix + counter
	// This makes it easy to identify the source
	prefix := simplifyHost(host)
	return prefix + "-" + formatCounter(counter)
}

// simplifyHost extracts a simple identifier from a host.
func simplifyHost(host string) string {
	// Remove port
	if idx := len(host) - 1; idx > 0 {
		for i := idx; i >= 0; i-- {
			if host[i] == ':' {
				host = host[:i]
				break
			}
		}
	}

	// Use last two parts of domain
	parts := splitDomain(host)
	if len(parts) >= 2 {
		return parts[len(parts)-2]
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "unknown"
}

func splitDomain(host string) []string {
	var parts []string
	start := 0
	for i := 0; i <= len(host); i++ {
		if i == len(host) || host[i] == '.' {
			if i > start {
				parts = append(parts, host[start:i])
			}
			start = i + 1
		}
	}
	return parts
}

func formatCounter(n int) string {
	// Base36 encoding for compact task IDs
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	if n == 0 {
		return "0"
	}
	var result []byte
	for n > 0 {
		result = append([]byte{digits[n%36]}, result...)
		n /= 36
	}
	return string(result)
}

// Reset clears the assigner state.
func (a *Assigner) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastActivity = make(map[string]time.Time)
	a.lastTaskID = make(map[string]string)
	a.taskCounter = 0
}

// Stats returns statistics about task assignment.
type Stats struct {
	ActiveHosts  int
	TotalTasks   int
}

// GetStats returns current statistics.
func (a *Assigner) GetStats() Stats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return Stats{
		ActiveHosts: len(a.lastActivity),
		TotalTasks:  a.taskCounter,
	}
}
