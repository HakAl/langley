package api

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HakAl/langley/internal/store"
)

func TestParseExportConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		query         string
		wantFormat    ExportFormat
		wantBodies    bool
		wantMaxRows   int
	}{
		{
			name:        "defaults",
			query:       "",
			wantFormat:  FormatNDJSON,
			wantBodies:  false,
			wantMaxRows: 0,
		},
		{
			name:        "json format with default max rows",
			query:       "format=json",
			wantFormat:  FormatJSON,
			wantBodies:  false,
			wantMaxRows: MaxJSONRows,
		},
		{
			name:        "csv format with default max rows",
			query:       "format=csv",
			wantFormat:  FormatCSV,
			wantBodies:  false,
			wantMaxRows: MaxCSVRows,
		},
		{
			name:        "include bodies",
			query:       "include_bodies=true",
			wantFormat:  FormatNDJSON,
			wantBodies:  true,
			wantMaxRows: 0,
		},
		{
			name:        "custom max rows",
			query:       "format=ndjson&max_rows=500",
			wantFormat:  FormatNDJSON,
			wantBodies:  false,
			wantMaxRows: 500,
		},
		{
			name:        "all options",
			query:       "format=json&include_bodies=true&max_rows=100",
			wantFormat:  FormatJSON,
			wantBodies:  true,
			wantMaxRows: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mustNewRequest(t, "GET", "/api/flows/export?"+tt.query)
			cfg := ParseExportConfig(req)

			if cfg.Format != tt.wantFormat {
				t.Errorf("Format = %v, want %v", cfg.Format, tt.wantFormat)
			}
			if cfg.IncludeBodies != tt.wantBodies {
				t.Errorf("IncludeBodies = %v, want %v", cfg.IncludeBodies, tt.wantBodies)
			}
			if cfg.MaxRows != tt.wantMaxRows {
				t.Errorf("MaxRows = %v, want %v", cfg.MaxRows, tt.wantMaxRows)
			}
		})
	}
}

func TestNDJSONExporter(t *testing.T) {
	t.Parallel()

	flow := testFlow()
	var buf bytes.Buffer
	exporter := NewNDJSONExporter()

	if err := exporter.WriteHeader(&buf); err != nil {
		t.Fatalf("WriteHeader error: %v", err)
	}

	// Write without bodies
	if err := exporter.WriteFlow(&buf, flow, false); err != nil {
		t.Fatalf("WriteFlow error: %v", err)
	}

	if err := exporter.WriteFooter(&buf, 1, 0); err != nil {
		t.Fatalf("WriteFooter error: %v", err)
	}

	// Parse result
	output := buf.String()
	if !strings.HasSuffix(output, "\n") {
		t.Error("NDJSON should end with newline")
	}

	var result ExportFlowSummary
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Failed to parse NDJSON: %v", err)
	}

	if result.ID != flow.ID {
		t.Errorf("ID = %v, want %v", result.ID, flow.ID)
	}
	if result.Host != flow.Host {
		t.Errorf("Host = %v, want %v", result.Host, flow.Host)
	}
}

func TestNDJSONExporter_WithBodies(t *testing.T) {
	t.Parallel()

	flow := testFlowWithBodies()
	var buf bytes.Buffer
	exporter := NewNDJSONExporter()

	exporter.WriteHeader(&buf)
	if err := exporter.WriteFlow(&buf, flow, true); err != nil {
		t.Fatalf("WriteFlow error: %v", err)
	}
	exporter.WriteFooter(&buf, 1, 0)

	var result ExportFlowFull
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse NDJSON: %v", err)
	}

	if result.RequestBody == nil || *result.RequestBody != *flow.RequestBody {
		t.Error("RequestBody not included")
	}
	if result.ResponseBody == nil || *result.ResponseBody != *flow.ResponseBody {
		t.Error("ResponseBody not included")
	}
}

func TestJSONExporter(t *testing.T) {
	t.Parallel()

	flows := []*store.Flow{testFlow(), testFlow()}
	flows[1].ID = "flow-2"

	var buf bytes.Buffer
	exporter := NewJSONExporter()

	exporter.WriteHeader(&buf)
	for _, f := range flows {
		if err := exporter.WriteFlow(&buf, f, false); err != nil {
			t.Fatalf("WriteFlow error: %v", err)
		}
	}
	if err := exporter.WriteFooter(&buf, 2, 0); err != nil {
		t.Fatalf("WriteFooter error: %v", err)
	}

	// Parse result
	var result struct {
		Flows []ExportFlowSummary    `json:"flows"`
		Meta  map[string]interface{} `json:"meta"`
	}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	if len(result.Flows) != 2 {
		t.Errorf("Expected 2 flows, got %d", len(result.Flows))
	}
	if result.Meta["row_count"].(float64) != 2 {
		t.Errorf("row_count = %v, want 2", result.Meta["row_count"])
	}
	if _, ok := result.Meta["exported_at"]; !ok {
		t.Error("Missing exported_at in meta")
	}
}

func TestCSVExporter(t *testing.T) {
	t.Parallel()

	flow := testFlow()
	var buf bytes.Buffer
	exporter := NewCSVExporter()

	if err := exporter.WriteHeader(&buf); err != nil {
		t.Fatalf("WriteHeader error: %v", err)
	}
	if err := exporter.WriteFlow(&buf, flow, false); err != nil {
		t.Fatalf("WriteFlow error: %v", err)
	}
	if err := exporter.WriteFooter(&buf, 1, 0); err != nil {
		t.Fatalf("WriteFooter error: %v", err)
	}

	// Parse CSV
	reader := csv.NewReader(&buf)
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("Failed to parse CSV: %v", err)
	}

	if len(records) != 2 { // header + 1 row
		t.Errorf("Expected 2 records (header + 1 row), got %d", len(records))
	}

	// Check header
	header := records[0]
	if header[0] != "id" {
		t.Errorf("First header = %v, want 'id'", header[0])
	}

	// Check data row
	row := records[1]
	if row[0] != flow.ID {
		t.Errorf("ID = %v, want %v", row[0], flow.ID)
	}
	if row[2] != flow.Host {
		t.Errorf("Host = %v, want %v", row[2], flow.Host)
	}
}

func TestCSVExporter_IgnoresBodies(t *testing.T) {
	t.Parallel()

	flow := testFlowWithBodies()
	var buf bytes.Buffer
	exporter := NewCSVExporter()

	exporter.WriteHeader(&buf)
	// Even with includeBodies=true, CSV should not include them
	exporter.WriteFlow(&buf, flow, true)
	exporter.WriteFooter(&buf, 1, 0)

	output := buf.String()
	if strings.Contains(output, "request body content") {
		t.Error("CSV should not include request body")
	}
	if strings.Contains(output, "response body content") {
		t.Error("CSV should not include response body")
	}
}

func TestExporterContentTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		format      ExportFormat
		contentType string
		extension   string
	}{
		{FormatNDJSON, "application/x-ndjson", "ndjson"},
		{FormatJSON, "application/json", "json"},
		{FormatCSV, "text/csv", "csv"},
	}

	for _, tt := range tests {
		t.Run(string(tt.format), func(t *testing.T) {
			exporter := NewExporter(tt.format)
			if exporter.ContentType() != tt.contentType {
				t.Errorf("ContentType = %v, want %v", exporter.ContentType(), tt.contentType)
			}
			if exporter.FileExtension() != tt.extension {
				t.Errorf("FileExtension = %v, want %v", exporter.FileExtension(), tt.extension)
			}
		})
	}
}

// Helper functions

func testFlow() *store.Flow {
	status := 200
	duration := int64(150)
	model := "claude-3-opus"
	inputTokens := 100
	outputTokens := 50

	return &store.Flow{
		ID:            "flow-1",
		Timestamp:     time.Now(),
		Host:          "api.anthropic.com",
		Method:        "POST",
		Path:          "/v1/messages",
		StatusCode:    &status,
		DurationMs:    &duration,
		IsSSE:         true,
		Model:         &model,
		Provider:      "anthropic",
		InputTokens:   &inputTokens,
		OutputTokens:  &outputTokens,
		FlowIntegrity: "complete",
	}
}

func testFlowWithBodies() *store.Flow {
	flow := testFlow()
	reqBody := "request body content"
	respBody := "response body content"
	flow.RequestBody = &reqBody
	flow.ResponseBody = &respBody
	flow.RequestHeaders = map[string][]string{"Content-Type": {"application/json"}}
	flow.ResponseHeaders = map[string][]string{"Content-Type": {"text/event-stream"}}
	return flow
}

func mustNewRequest(t *testing.T, method, url string) *http.Request {
	t.Helper()
	return httptest.NewRequest(method, url, nil)
}
