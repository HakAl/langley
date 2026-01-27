# Test Fixtures

This package provides shared test fixtures for consistent, realistic test data across Go tests.

## Directory Structure

```
internal/testutil/
├── fixtures.go          # Loader functions + FlowBuilder type
├── fixtures_test.go     # Tests for the fixtures themselves
├── flows/               # JSON flow fixtures
│   ├── anthropic_success.json
│   ├── anthropic_streaming.json
│   ├── openai_success.json
│   ├── bedrock_success.json
│   ├── gemini_success.json
│   └── error_500.json
├── sse/                 # SSE stream fixtures
│   ├── anthropic_conversation.txt
│   ├── openai_stream.txt
│   └── gemini_stream.txt
├── responses/           # Provider response bodies
│   ├── anthropic.json
│   ├── openai.json
│   ├── bedrock.json
│   └── gemini.json
└── README.md
```

## Usage

### FlowBuilder - Programmatic Flow Creation

Use `FlowBuilder` for creating flows with custom values in tests:

```go
import "github.com/HakAl/langley/internal/testutil"

func TestSomething(t *testing.T) {
    // Create a flow with defaults
    flow := testutil.NewFlow().Build()

    // Create a customized flow
    flow := testutil.NewFlow().
        WithProvider("openai").
        WithTaskID("task-123").
        WithStatus(200).
        WithTokens(100, 50).
        Streaming().
        Build()
}
```

Available builder methods:

| Method | Description |
|--------|-------------|
| `WithID(id)` | Set flow ID |
| `WithProvider(provider)` | Set provider and update host/path |
| `WithTaskID(id)` | Set task ID |
| `WithStatus(code)` | Set HTTP status code and text |
| `WithModel(model)` | Set model name |
| `WithTokens(input, output)` | Set token counts |
| `WithCacheTokens(creation, read)` | Set cache token counts |
| `WithDuration(ms)` | Set request duration |
| `WithIntegrity(status)` | Set flow integrity status |
| `WithRequestBody(body)` | Set request body |
| `WithResponseBody(body)` | Set response body |
| `Streaming()` | Mark as SSE streaming |
| `Build()` | Return the constructed `*store.Flow` |

### Loading Fixtures from Files

Use loader functions for realistic provider data:

```go
import "github.com/HakAl/langley/internal/testutil"

func TestAnthropicFlow(t *testing.T) {
    // Load a flow fixture
    flow := testutil.LoadFlow(t, "anthropic_success")

    // Load an SSE stream
    sse := testutil.LoadSSE(t, "anthropic_conversation")

    // Load a response body
    body := testutil.LoadResponse(t, "anthropic")
}
```

### Available Fixtures

#### Flows (`flows/*.json`)

| Name | Description |
|------|-------------|
| `anthropic_success` | Successful Anthropic API call with cache tokens |
| `anthropic_streaming` | Streaming Anthropic API call |
| `openai_success` | Successful OpenAI API call |
| `bedrock_success` | Successful Bedrock API call |
| `gemini_success` | Successful Gemini API call |
| `error_500` | Server error response |

#### SSE Streams (`sse/*.txt`)

| Name | Description |
|------|-------------|
| `anthropic_conversation` | Full Anthropic streaming conversation |
| `openai_stream` | OpenAI streaming response with usage |
| `gemini_stream` | Gemini streaming response |

#### Responses (`responses/*.json`)

| Name | Description |
|------|-------------|
| `anthropic` | Anthropic message response with cache tokens |
| `openai` | OpenAI chat completion response |
| `bedrock` | Bedrock Converse API response |
| `gemini` | Gemini generateContent response |

## Design Decisions

1. **Builder pattern over factory functions** - More readable and easier to customize
2. **JSON files over Go literals** - Easier to read/edit, matches real API data
3. **`testing.TB` parameter** - Works with both `*testing.T` and `*testing.B`
4. **`t.Helper()` calls** - Proper stack traces on failure
5. **Embed via `go:embed`** - No file path issues across platforms

## Adding New Fixtures

1. Add the JSON/txt file to the appropriate directory
2. The file will be automatically embedded via `go:embed`
3. Access it using the appropriate loader function
4. Add a test case in `fixtures_test.go` to verify it loads correctly

## Testing the Fixtures

```bash
go test ./internal/testutil/...
```
