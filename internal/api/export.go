package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/HakAl/langley/internal/store"
)

// ExportFormat represents supported export formats.
type ExportFormat string

const (
	FormatNDJSON ExportFormat = "ndjson"
	FormatJSON   ExportFormat = "json"
	FormatCSV    ExportFormat = "csv"

	// MaxCSVRows limits CSV exports to prevent browser/Excel issues
	MaxCSVRows = 10000
	// MaxJSONRows limits JSON exports to prevent OOM (JSON buffers all rows in memory)
	MaxJSONRows = 10000
)

// ExportFlowFull extends ExportFlowSummary with body fields.
type ExportFlowFull struct {
	ExportFlowSummary
	RequestBody           *string            `json:"request_body,omitempty"`
	RequestBodyTruncated  bool               `json:"request_body_truncated,omitempty"`
	ResponseBody          *string            `json:"response_body,omitempty"`
	ResponseBodyTruncated bool               `json:"response_body_truncated,omitempty"`
	RequestHeaders        map[string][]string `json:"request_headers,omitempty"`
	ResponseHeaders       map[string][]string `json:"response_headers,omitempty"`
}

// ExportConfig holds export configuration parsed from query params.
type ExportConfig struct {
	Format        ExportFormat
	IncludeBodies bool
	MaxRows       int
}

// ParseExportConfig parses export configuration from request query params.
func ParseExportConfig(r *http.Request) ExportConfig {
	cfg := ExportConfig{
		Format:        FormatNDJSON,
		IncludeBodies: false,
		MaxRows:       0,
	}

	if v := r.URL.Query().Get("format"); v != "" {
		switch v {
		case "json":
			cfg.Format = FormatJSON
			if cfg.MaxRows == 0 {
				cfg.MaxRows = MaxJSONRows
			}
		case "csv":
			cfg.Format = FormatCSV
			if cfg.MaxRows == 0 {
				cfg.MaxRows = MaxCSVRows
			}
		case "ndjson":
			cfg.Format = FormatNDJSON
		}
	}

	if v := r.URL.Query().Get("include_bodies"); v == "true" {
		cfg.IncludeBodies = true
	}

	if v := r.URL.Query().Get("max_rows"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxRows = n
		}
	}

	return cfg
}

// FlowExporter writes flows in a specific format.
type FlowExporter interface {
	// ContentType returns the MIME type for this format.
	ContentType() string
	// FileExtension returns the file extension for downloads.
	FileExtension() string
	// WriteHeader writes any header/preamble needed.
	WriteHeader(w io.Writer) error
	// WriteFlow writes a single flow.
	WriteFlow(w io.Writer, flow *store.Flow, includeBodies bool) error
	// WriteFooter writes any footer/closing needed.
	WriteFooter(w io.Writer, rowCount int, truncatedBodies int) error
}

// NDJSONExporter exports flows as newline-delimited JSON.
type NDJSONExporter struct {
	encoder *json.Encoder
}

func NewNDJSONExporter() *NDJSONExporter {
	return &NDJSONExporter{}
}

func (e *NDJSONExporter) ContentType() string   { return "application/x-ndjson" }
func (e *NDJSONExporter) FileExtension() string { return "ndjson" }

func (e *NDJSONExporter) WriteHeader(w io.Writer) error {
	e.encoder = json.NewEncoder(w)
	return nil
}

func (e *NDJSONExporter) WriteFlow(w io.Writer, flow *store.Flow, includeBodies bool) error {
	if includeBodies {
		return e.encoder.Encode(toExportFlowFull(flow))
	}
	return e.encoder.Encode(toExportFlowSummary(flow))
}

func (e *NDJSONExporter) WriteFooter(w io.Writer, rowCount int, truncatedBodies int) error {
	return nil // NDJSON has no footer
}

// JSONExporter exports flows as a JSON array with metadata.
type JSONExporter struct {
	flows           []interface{}
	includeBodies   bool
	truncatedBodies int
}

func NewJSONExporter() *JSONExporter {
	return &JSONExporter{
		flows: make([]interface{}, 0),
	}
}

func (e *JSONExporter) ContentType() string   { return "application/json" }
func (e *JSONExporter) FileExtension() string { return "json" }

func (e *JSONExporter) WriteHeader(w io.Writer) error {
	return nil // JSON writes everything in footer
}

func (e *JSONExporter) WriteFlow(w io.Writer, flow *store.Flow, includeBodies bool) error {
	e.includeBodies = includeBodies
	if includeBodies {
		full := toExportFlowFull(flow)
		if flow.RequestBodyTruncated || flow.ResponseBodyTruncated {
			e.truncatedBodies++
		}
		e.flows = append(e.flows, full)
	} else {
		e.flows = append(e.flows, toExportFlowSummary(flow))
	}
	return nil
}

func (e *JSONExporter) WriteFooter(w io.Writer, rowCount int, truncatedBodies int) error {
	response := map[string]interface{}{
		"flows": e.flows,
		"meta": map[string]interface{}{
			"row_count":   rowCount,
			"exported_at": time.Now().UTC().Format(time.RFC3339),
		},
	}
	if e.includeBodies && e.truncatedBodies > 0 {
		response["meta"].(map[string]interface{})["truncated_bodies"] = e.truncatedBodies
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(response)
}

// CSVExporter exports flows as CSV (summary fields only).
type CSVExporter struct {
	writer *csv.Writer
}

func NewCSVExporter() *CSVExporter {
	return &CSVExporter{}
}

func (e *CSVExporter) ContentType() string   { return "text/csv" }
func (e *CSVExporter) FileExtension() string { return "csv" }

func (e *CSVExporter) WriteHeader(w io.Writer) error {
	e.writer = csv.NewWriter(w)
	return e.writer.Write([]string{
		"id", "timestamp", "host", "method", "path", "status_code",
		"duration_ms", "is_sse", "task_id", "task_source", "model",
		"provider", "input_tokens", "output_tokens", "total_cost", "flow_integrity",
	})
}

func (e *CSVExporter) WriteFlow(w io.Writer, flow *store.Flow, includeBodies bool) error {
	// CSV ignores includeBodies - always summary only
	record := []string{
		flow.ID,
		flow.Timestamp.Format(time.RFC3339),
		flow.Host,
		flow.Method,
		flow.Path,
		ptrToStr(flow.StatusCode),
		ptrInt64ToStr(flow.DurationMs),
		strconv.FormatBool(flow.IsSSE),
		ptrStr(flow.TaskID),
		ptrStr(flow.TaskSource),
		ptrStr(flow.Model),
		flow.Provider,
		ptrToStr(flow.InputTokens),
		ptrToStr(flow.OutputTokens),
		ptrFloat64ToStr(flow.TotalCost),
		flow.FlowIntegrity,
	}
	return e.writer.Write(record)
}

func (e *CSVExporter) WriteFooter(w io.Writer, rowCount int, truncatedBodies int) error {
	e.writer.Flush()
	return e.writer.Error()
}

// NewExporter creates an exporter for the given format.
func NewExporter(format ExportFormat) FlowExporter {
	switch format {
	case FormatJSON:
		return NewJSONExporter()
	case FormatCSV:
		return NewCSVExporter()
	default:
		return NewNDJSONExporter()
	}
}

// toExportFlowFull converts a store.Flow to ExportFlowFull.
func toExportFlowFull(f *store.Flow) ExportFlowFull {
	return ExportFlowFull{
		ExportFlowSummary:     toExportFlowSummary(f),
		RequestBody:          f.RequestBody,
		RequestBodyTruncated:  f.RequestBodyTruncated,
		ResponseBody:          f.ResponseBody,
		ResponseBodyTruncated: f.ResponseBodyTruncated,
		RequestHeaders:        f.RequestHeaders,
		ResponseHeaders:       f.ResponseHeaders,
	}
}

// Helper functions for CSV conversion
func ptrToStr(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func ptrInt64ToStr(p *int64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatInt(*p, 10)
}

func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func ptrFloat64ToStr(p *float64) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf("%.6f", *p)
}
