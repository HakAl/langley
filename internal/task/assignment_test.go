package task

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestNewAssigner(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		idleGapMinutes int
		wantIdleGap    time.Duration
	}{
		{
			name:           "default idle gap",
			idleGapMinutes: 0,
			wantIdleGap:    5 * time.Minute,
		},
		{
			name:           "custom idle gap",
			idleGapMinutes: 10,
			wantIdleGap:    10 * time.Minute,
		},
		{
			name:           "negative uses default",
			idleGapMinutes: -1,
			wantIdleGap:    5 * time.Minute,
		},
		{
			name:           "one minute",
			idleGapMinutes: 1,
			wantIdleGap:    1 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := NewAssigner(AssignerConfig{IdleGapMinutes: tt.idleGapMinutes})
			if a.idleGap != tt.wantIdleGap {
				t.Errorf("idleGap = %v, want %v", a.idleGap, tt.wantIdleGap)
			}
			if a.lastActivity == nil {
				t.Error("lastActivity map not initialized")
			}
			if a.lastTaskID == nil {
				t.Error("lastTaskID map not initialized")
			}
		})
	}
}

func TestAssign_ExplicitHeader(t *testing.T) {
	t.Parallel()

	a := NewAssigner(AssignerConfig{})

	tests := []struct {
		name       string
		taskID     string
		wantSource string
	}{
		{
			name:       "explicit task ID",
			taskID:     "my-task-123",
			wantSource: SourceExplicit,
		},
		{
			name:       "explicit with special chars",
			taskID:     "task_with-special.chars",
			wantSource: SourceExplicit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}
			headers.Set(TaskHeader, tt.taskID)

			assignment := a.Assign("api.anthropic.com", headers, nil)

			if assignment.TaskID != tt.taskID {
				t.Errorf("TaskID = %q, want %q", assignment.TaskID, tt.taskID)
			}
			if assignment.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", assignment.Source, tt.wantSource)
			}
		})
	}
}

func TestAssign_Metadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       []byte
		wantTaskID string
		wantSource string
	}{
		{
			name:       "metadata user_id present",
			body:       []byte(`{"metadata": {"user_id": "user-abc-123"}}`),
			wantTaskID: "user-abc-123",
			wantSource: SourceMetadata,
		},
		{
			name:       "metadata with other fields",
			body:       []byte(`{"model": "claude-3", "metadata": {"user_id": "task-456", "other": "ignored"}}`),
			wantTaskID: "task-456",
			wantSource: SourceMetadata,
		},
		{
			name:       "empty metadata falls through to heuristic",
			body:       []byte(`{"metadata": {}}`),
			wantTaskID: "", // Will be assigned by heuristic
			wantSource: SourceInferred,
		},
		{
			name:       "no metadata falls through to heuristic",
			body:       []byte(`{"model": "claude-3"}`),
			wantTaskID: "", // Will be assigned by heuristic
			wantSource: SourceInferred,
		},
		{
			name:       "empty body falls through to heuristic",
			body:       nil,
			wantTaskID: "", // Will be assigned by heuristic
			wantSource: SourceInferred,
		},
		{
			name:       "invalid JSON falls through to heuristic",
			body:       []byte(`not json`),
			wantTaskID: "", // Will be assigned by heuristic
			wantSource: SourceInferred,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use fresh assigner for each test to avoid state interference
			assigner := NewAssigner(AssignerConfig{})
			assignment := assigner.Assign("api.anthropic.com", http.Header{}, tt.body)

			if tt.wantTaskID != "" {
				if assignment.TaskID != tt.wantTaskID {
					t.Errorf("TaskID = %q, want %q", assignment.TaskID, tt.wantTaskID)
				}
			}
			if assignment.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", assignment.Source, tt.wantSource)
			}
		})
	}
}

func TestAssign_Priority(t *testing.T) {
	t.Parallel()

	// Test that explicit header takes priority over metadata
	a := NewAssigner(AssignerConfig{})

	headers := http.Header{}
	headers.Set(TaskHeader, "explicit-task")
	body := []byte(`{"metadata": {"user_id": "metadata-task"}}`)

	assignment := a.Assign("api.anthropic.com", headers, body)

	if assignment.TaskID != "explicit-task" {
		t.Errorf("TaskID = %q, want %q (explicit should win)", assignment.TaskID, "explicit-task")
	}
	if assignment.Source != SourceExplicit {
		t.Errorf("Source = %q, want %q", assignment.Source, SourceExplicit)
	}
}

func TestAssign_Heuristic(t *testing.T) {
	t.Parallel()

	t.Run("first request creates new task", func(t *testing.T) {
		a := NewAssigner(AssignerConfig{IdleGapMinutes: 5})

		assignment := a.Assign("api.anthropic.com", http.Header{}, nil)

		if assignment.TaskID == "" {
			t.Error("expected non-empty TaskID")
		}
		if assignment.Source != SourceInferred {
			t.Errorf("Source = %q, want %q", assignment.Source, SourceInferred)
		}
	})

	t.Run("same host within idle gap returns same task", func(t *testing.T) {
		a := NewAssigner(AssignerConfig{IdleGapMinutes: 5})

		first := a.Assign("api.anthropic.com", http.Header{}, nil)
		second := a.Assign("api.anthropic.com", http.Header{}, nil)

		if first.TaskID != second.TaskID {
			t.Errorf("expected same TaskID, got %q and %q", first.TaskID, second.TaskID)
		}
	})

	t.Run("different hosts get different tasks", func(t *testing.T) {
		a := NewAssigner(AssignerConfig{IdleGapMinutes: 5})

		anthropic := a.Assign("api.anthropic.com", http.Header{}, nil)
		openai := a.Assign("api.openai.com", http.Header{}, nil)

		if anthropic.TaskID == openai.TaskID {
			t.Errorf("expected different TaskIDs, both got %q", anthropic.TaskID)
		}
	})

	t.Run("task counter increments", func(t *testing.T) {
		a := NewAssigner(AssignerConfig{IdleGapMinutes: 5})

		a.Assign("host1.com", http.Header{}, nil)
		a.Assign("host2.com", http.Header{}, nil)
		a.Assign("host3.com", http.Header{}, nil)

		stats := a.GetStats()
		if stats.TotalTasks != 3 {
			t.Errorf("TotalTasks = %d, want 3", stats.TotalTasks)
		}
		if stats.ActiveHosts != 3 {
			t.Errorf("ActiveHosts = %d, want 3", stats.ActiveHosts)
		}
	})
}

func TestExtractMetadataTaskID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body []byte
		want string
	}{
		{
			name: "valid metadata",
			body: []byte(`{"metadata": {"user_id": "test-123"}}`),
			want: "test-123",
		},
		{
			name: "empty user_id",
			body: []byte(`{"metadata": {"user_id": ""}}`),
			want: "",
		},
		{
			name: "missing user_id",
			body: []byte(`{"metadata": {"other": "value"}}`),
			want: "",
		},
		{
			name: "missing metadata",
			body: []byte(`{"model": "claude"}`),
			want: "",
		},
		{
			name: "empty body",
			body: []byte{},
			want: "",
		},
		{
			name: "nil body",
			body: nil,
			want: "",
		},
		{
			name: "invalid JSON",
			body: []byte(`{invalid`),
			want: "",
		},
		{
			name: "nested structure",
			body: []byte(`{"data": {"metadata": {"user_id": "nested"}}}`),
			want: "", // Only top-level metadata counts
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractMetadataTaskID(tt.body)
			if got != tt.want {
				t.Errorf("extractMetadataTaskID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSimplifyHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		host string
		want string
	}{
		{"api.anthropic.com", "anthropic"},
		{"api.anthropic.com:443", "anthropic"},
		{"api.openai.com", "openai"},
		{"bedrock-runtime.us-east-1.amazonaws.com", "amazonaws"},
		{"localhost", "localhost"},
		{"localhost:8080", "localhost"},
		{"127.0.0.1", "0"},       // IP treated as domain parts: [127, 0, 0, 1] -> second-to-last
		{"127.0.0.1:9090", "0"}, // Same after port stripping
		{"single", "single"},
		{"", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			t.Parallel()
			got := simplifyHost(tt.host)
			if got != tt.want {
				t.Errorf("simplifyHost(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}

func TestSplitDomain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		host string
		want []string
	}{
		{"api.anthropic.com", []string{"api", "anthropic", "com"}},
		{"localhost", []string{"localhost"}},
		{"a.b.c.d", []string{"a", "b", "c", "d"}},
		{"", nil},
		{"...", nil},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			t.Parallel()
			got := splitDomain(tt.host)
			if len(got) != len(tt.want) {
				t.Errorf("splitDomain(%q) = %v, want %v", tt.host, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("splitDomain(%q)[%d] = %q, want %q", tt.host, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestFormatCounter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{9, "9"},
		{10, "a"},
		{35, "z"},
		{36, "10"},
		{37, "11"},
		{100, "2s"},
		{1000, "rs"},
		{1296, "100"}, // 36^2
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			got := formatCounter(tt.n)
			if got != tt.want {
				t.Errorf("formatCounter(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}

func TestGenerateTaskID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		host    string
		counter int
		want    string
	}{
		{"api.anthropic.com", 1, "anthropic-1"},
		{"api.openai.com", 36, "openai-10"},
		{"localhost", 100, "localhost-2s"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			got := generateTaskID(tt.host, tt.counter)
			if got != tt.want {
				t.Errorf("generateTaskID(%q, %d) = %q, want %q", tt.host, tt.counter, got, tt.want)
			}
		})
	}
}

func TestReset(t *testing.T) {
	t.Parallel()

	a := NewAssigner(AssignerConfig{})

	// Create some state
	a.Assign("host1.com", http.Header{}, nil)
	a.Assign("host2.com", http.Header{}, nil)

	stats := a.GetStats()
	if stats.TotalTasks != 2 {
		t.Errorf("before reset: TotalTasks = %d, want 2", stats.TotalTasks)
	}

	// Reset
	a.Reset()

	stats = a.GetStats()
	if stats.TotalTasks != 0 {
		t.Errorf("after reset: TotalTasks = %d, want 0", stats.TotalTasks)
	}
	if stats.ActiveHosts != 0 {
		t.Errorf("after reset: ActiveHosts = %d, want 0", stats.ActiveHosts)
	}

	// New assignment should start fresh
	assignment := a.Assign("host1.com", http.Header{}, nil)
	if assignment.TaskID != "host1-1" {
		t.Errorf("after reset: TaskID = %q, want %q", assignment.TaskID, "host1-1")
	}
}

func TestGetStats(t *testing.T) {
	t.Parallel()

	a := NewAssigner(AssignerConfig{})

	// Initial state
	stats := a.GetStats()
	if stats.TotalTasks != 0 || stats.ActiveHosts != 0 {
		t.Errorf("initial stats = %+v, want zeros", stats)
	}

	// Add hosts
	a.Assign("host1.com", http.Header{}, nil)
	stats = a.GetStats()
	if stats.TotalTasks != 1 || stats.ActiveHosts != 1 {
		t.Errorf("after 1 host: stats = %+v, want {1, 1}", stats)
	}

	// Same host doesn't increment
	a.Assign("host1.com", http.Header{}, nil)
	stats = a.GetStats()
	if stats.TotalTasks != 1 || stats.ActiveHosts != 1 {
		t.Errorf("after same host: stats = %+v, want {1, 1}", stats)
	}

	// New host increments both
	a.Assign("host2.com", http.Header{}, nil)
	stats = a.GetStats()
	if stats.TotalTasks != 2 || stats.ActiveHosts != 2 {
		t.Errorf("after 2 hosts: stats = %+v, want {2, 2}", stats)
	}
}

func TestConcurrentAccess(t *testing.T) {
	t.Parallel()

	a := NewAssigner(AssignerConfig{IdleGapMinutes: 1})

	const numGoroutines = 100
	const requestsPerGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Stress test concurrent access
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()

			host := "api.anthropic.com"
			if id%2 == 0 {
				host = "api.openai.com"
			}

			for j := 0; j < requestsPerGoroutine; j++ {
				headers := http.Header{}
				if j%10 == 0 {
					headers.Set(TaskHeader, "explicit-task")
				}

				var body []byte
				if j%5 == 0 {
					body = []byte(`{"metadata": {"user_id": "metadata-task"}}`)
				}

				assignment := a.Assign(host, headers, body)

				// Basic sanity checks
				if assignment == nil {
					t.Error("assignment is nil")
					return
				}
				if assignment.TaskID == "" {
					t.Error("TaskID is empty")
					return
				}
				if assignment.Source == "" {
					t.Error("Source is empty")
					return
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify state is consistent
	stats := a.GetStats()
	if stats.ActiveHosts < 1 || stats.ActiveHosts > 2 {
		t.Errorf("ActiveHosts = %d, expected 1-2", stats.ActiveHosts)
	}
}

func TestConcurrentReset(t *testing.T) {
	t.Parallel()

	a := NewAssigner(AssignerConfig{})

	var wg sync.WaitGroup
	wg.Add(3)

	// Concurrent assigns
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			a.Assign("host1.com", http.Header{}, nil)
		}
	}()

	// Concurrent resets
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			a.Reset()
		}
	}()

	// Concurrent stats
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = a.GetStats()
		}
	}()

	wg.Wait()

	// Should not panic - that's the main test
}
