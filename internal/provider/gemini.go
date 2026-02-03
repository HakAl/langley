package provider

import (
	"encoding/json"
	"strings"
)

// Gemini implements Provider for Google's Gemini API.
type Gemini struct{}

// Name returns "gemini".
func (g *Gemini) Name() string {
	return "gemini"
}

// DetectHost returns true for Google Gemini API hosts.
func (g *Gemini) DetectHost(host string) bool {
	return MatchDomainSuffix(host, "generativelanguage.googleapis.com")
}

// ParseUsage extracts token usage from Gemini responses.
func (g *Gemini) ParseUsage(body []byte, isSSE bool) (*Usage, error) {
	if isSSE {
		return g.parseSSE(body)
	}
	return g.parseJSON(body)
}

// geminiUsageMetadata represents Gemini's usage structure.
type geminiUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

// parseJSON extracts usage from a non-streaming Gemini response.
func (g *Gemini) parseJSON(body []byte) (*Usage, error) {
	var response struct {
		UsageMetadata *geminiUsageMetadata `json:"usageMetadata"`
		ModelVersion  string               `json:"modelVersion"`
	}

	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}

	usage := &Usage{
		Model: response.ModelVersion,
	}

	if response.UsageMetadata != nil {
		usage.InputTokens = response.UsageMetadata.PromptTokenCount
		usage.OutputTokens = response.UsageMetadata.CandidatesTokenCount
	}

	return usage, nil
}

// parseSSE extracts usage from a Gemini streaming response.
// Gemini streaming returns JSON objects, with usageMetadata in the final chunk.
func (g *Gemini) parseSSE(body []byte) (*Usage, error) {
	usage := &Usage{}
	lines := strings.Split(string(body), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Handle SSE format (data: prefix)
		if strings.HasPrefix(line, "data:") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}

		if line == "" || line == "[DONE]" {
			continue
		}

		// Try to parse as JSON chunk
		var chunk struct {
			UsageMetadata *geminiUsageMetadata `json:"usageMetadata"`
			ModelVersion  string               `json:"modelVersion"`
		}

		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			continue
		}

		if chunk.ModelVersion != "" {
			usage.Model = chunk.ModelVersion
		}

		// UsageMetadata appears in the final chunk
		if chunk.UsageMetadata != nil {
			usage.InputTokens = chunk.UsageMetadata.PromptTokenCount
			usage.OutputTokens = chunk.UsageMetadata.CandidatesTokenCount
		}
	}

	return usage, nil
}
