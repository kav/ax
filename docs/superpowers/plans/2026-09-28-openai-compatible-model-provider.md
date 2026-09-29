# OpenAI-Compatible Model Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add OpenAI Chat Completions support with an optional endpoint URL so AX can use OpenAI and compatible endpoints such as Synthetic Labs.

**Architecture:** Extend the existing `ModelSpec` with an optional `baseUrl`, carry it through AX's model configuration conversion, and dispatch `provider: openai` to a small HTTP adapter in the existing model client. Reuse the existing HTTP client, SecretKey reference, provider parameters, and Gemini fallback behavior; do not add an SDK dependency.

**Tech Stack:** Go 1.27, protobuf/protojson, `net/http`, `httptest`, YAML v3.

---

## Files to Change

- `pkg/apis/v1alpha1/ax.proto` — add the optional public `ModelSpec.base_url` field.
- `pkg/apis/v1alpha1/ax.pb.go` — regenerate protobuf Go types and descriptors from `ax.proto`.
- `pkg/apis/v1alpha1/types_test.go` — ensure `baseUrl` survives Model YAML round trips.
- `internal/model/client.go` — map `baseUrl` into runtime `Config`, dispatch `openai`, and implement Chat Completions requests/responses.
- `internal/model/client_test.go` — cover endpoint selection, request/auth mapping, response usage, and errors.
- `docs/manifests.md` — document OpenAI and compatible endpoints and avoid claiming Anthropic runtime support in this release.

## Task 1: Add `baseUrl` to the Model API

**Files:** `pkg/apis/v1alpha1/ax.proto`, generated `pkg/apis/v1alpha1/ax.pb.go`, `pkg/apis/v1alpha1/types_test.go`, `internal/model/client.go`

- [x] **Step 1: Add a failing YAML round-trip test** in `pkg/apis/v1alpha1/types_test.go`:

```go
func TestModel_BaseURLYAMLRoundTrip(t *testing.T) {
	const input = `apiVersion: ax.io/v1alpha1
kind: Model
metadata:
  name: synthetic-model
  atespace: default
spec:
  provider: openai
  model: qwen3
  baseUrl: https://models.example/v1
  secretKey:
    name: synthetic-api-secret
    key: OPENAI_API_KEY
`
	var got v1alpha1.Model
	if err := yaml.Unmarshal([]byte(input), &got); err != nil {
		t.Fatalf("unmarshal Model with baseUrl: %v", err)
	}
	out, err := yaml.Marshal(&got)
	if err != nil {
		t.Fatalf("marshal Model: %v", err)
	}
	if !strings.Contains(string(out), "baseUrl: https://models.example/v1") {
		t.Fatalf("Model YAML lost baseUrl:\n%s", out)
	}
}
```

- [x] **Step 2: Run the new test and confirm the current schema rejects `baseUrl`.**

Run: `go test ./pkg/apis/v1alpha1 -run '^TestModel_BaseURLYAMLRoundTrip$' -count=1`

Expected: FAIL during YAML decoding because `baseUrl` is not yet a `ModelSpec` field.

- [x] **Step 3: Add the backward-compatible field and regenerate Go protobuf code.** In `ax.proto`, add `string base_url = 8;` to `ModelSpec`. Install the matching generator if needed, then run from the AX repository root:

```bash
brew install protobuf
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
PATH="$(go env GOPATH)/bin:$PATH" protoc --go_out=. --go_opt=paths=source_relative pkg/apis/v1alpha1/ax.proto
```

The gRPC service did not change, so `ax_grpc.pb.go` does not need regeneration.

- [x] **Step 4: Add a failing runtime configuration round-trip test** in `internal/model/client_test.go`:

```go
func TestConfigFromSpec_BaseURL(t *testing.T) {
	spec := &v1alpha1.ModelSpec{
		Provider: "openai",
		Model:    "qwen3",
		BaseUrl:  "https://models.example/v1",
	}
	cfg := model.ConfigFromSpec(spec)
	if cfg.BaseURL != "https://models.example/v1" {
		t.Fatalf("ConfigFromSpec BaseURL = %q", cfg.BaseURL)
	}
	resolver := model.WithSecretResolver(func(string, string) (string, error) { return "", nil })
	if got := model.NewClient(cfg, resolver).Spec().GetBaseUrl(); got != cfg.BaseURL {
		t.Fatalf("Spec baseUrl = %q, want %q", got, cfg.BaseURL)
	}
}
```

Run: `go test ./internal/model -run '^TestConfigFromSpec_BaseURL$' -count=1`

Expected before the conversion change: FAIL because `Config.BaseURL` is empty.

- [x] **Step 5: Implement both conversion directions.** In `ConfigFromSpec`, set `BaseURL: spec.GetBaseUrl()`. In `Client.Spec()`, set `BaseUrl: c.cfg.BaseURL` on the returned `ModelSpec`.

- [x] **Step 6: Re-run both targeted tests and commit.**

Run: `go test ./pkg/apis/v1alpha1 ./internal/model -run 'TestModel_BaseURLYAMLRoundTrip|TestConfigFromSpec_BaseURL' -count=1`

Expected: PASS. Commit with `feat(model): expose provider base URL`.

## Task 2: Implement and Test the OpenAI Chat Completions Adapter

**Files:** `internal/model/client.go`, `internal/model/client_test.go`

- [x] **Step 1: Add a failing HTTP adapter test.** Add this test to `internal/model/client_test.go`; it uses only the existing `net/http`, `httptest`, and JSON dependencies:

```go
func TestClient_OpenAIChatCompletionsHTTP(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s, want POST /v1/chat/completions", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		var body struct {
			Model       string `json:"model"`
			Messages    []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			TopP        float64 `json:"top_p"`
			Temperature float64 `json:"temperature"`
			MaxTokens   int     `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if body.Model != "qwen3" || body.TopP != 0.8 || body.Temperature != 0.2 || body.MaxTokens != 64 {
			t.Errorf("request fields = %+v", body)
		}
		if len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[0].Content != "Be concise" || body.Messages[1].Role != "user" || body.Messages[1].Content != "Plan a Go workspace" {
			t.Errorf("messages = %+v", body.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Use Go modules."}}],"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}}`))
	}))
	defer ts.Close()

	client := model.NewClient(model.Config{
		Provider: "openai",
		Model:    "qwen3",
		BaseURL:  ts.URL + "/v1",
		APIKey:   "test-key",
		Parameters: map[string]any{
			"top_p":             0.8,
			"systemInstruction": "Be concise",
		},
	}, model.WithHTTPClient(ts.Client()))
	resp, err := client.Generate(context.Background(), &model.GenerateRequest{
		Prompt: "Plan a Go workspace", Temperature: 0.2, MaxTokens: 64,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Content != "Use Go modules." || resp.Usage.PromptTokens != 12 || resp.Usage.CompletionTokens != 4 || resp.Usage.TotalTokens != 16 {
		t.Fatalf("response = %+v", resp)
	}
}
```

Run: `go test ./internal/model -run '^TestClient_OpenAIChatCompletionsHTTP$' -count=1`

Expected: FAIL with `unsupported provider "openai"`.

- [x] **Step 2: Add OpenAI provider constants and dispatch.** Define `ProviderOpenAI = "openai"` and `DefaultOpenAIBaseURL = "https://api.openai.com/v1"`. Route `provider: openai` to `generateOpenAI`; retain existing Google handling and unknown-provider errors.

- [x] **Step 3: Implement `generateOpenAI`.** Use this request construction in the adapter; the surrounding function follows the same context, HTTP, and error/fallback patterns as `generateGoogle`:

```go
baseURL := strings.TrimRight(c.cfg.BaseURL, "/")
if baseURL == "" {
	baseURL = DefaultOpenAIBaseURL
}
messages := make([]map[string]string, 0, 2)
if req.SystemInstruction != "" {
	messages = append(messages, map[string]string{"role": "system", "content": req.SystemInstruction})
}
messages = append(messages, map[string]string{"role": "user", "content": req.Prompt})
payload := map[string]any{"model": req.Model, "messages": messages}
for key, value := range c.cfg.Parameters {
	if key != systemInstructionParam && key != "model" && key != "messages" {
		payload[key] = value
	}
}
if req.Temperature > 0 {
	payload["temperature"] = req.Temperature
}
if req.MaxTokens > 0 {
	payload["max_tokens"] = req.MaxTokens
}
```

POST the JSON to `baseURL + "/chat/completions"`, set `Authorization: Bearer <APIKey>`, and decode `choices[0].message.content` plus `usage.prompt_tokens`, `usage.completion_tokens`, and `usage.total_tokens`. If the response has no choices, return empty content as Gemini currently does. For 401/403/429/503 and transport failures return the existing local fallback; return a descriptive error for other non-2xx responses or malformed JSON.

- [x] **Step 4: Add default URL and error-path tests.** Add a small transport adapter and default URL test to `client_test.go`:

```go
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClient_OpenAIDefaultBaseURL(t *testing.T) {
	client := model.NewClient(model.Config{
		Provider: "openai", Model: "gpt-4.1-mini", APIKey: "test-key",
	}, model.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got, want := r.URL.String(), "https://api.openai.com/v1/chat/completions"; got != want {
			t.Errorf("URL = %q, want %q", got, want)
		}
		body := `{"choices":[{"message":{"content":"ok"}}],"usage":{}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}))
	if _, err := client.Generate(context.Background(), &model.GenerateRequest{Prompt: "hello"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
}
```

Import `io` and `errors` in `client_test.go`. Add this table-driven status test:

```go
func TestClient_OpenAIHTTPStatusFallback(t *testing.T) {
	for _, tc := range []struct {
		status  int
		wantErr bool
	}{{401, false}, {403, false}, {429, false}, {503, false}, {500, true}} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("upstream failure"))
			}))
			defer ts.Close()
			client := model.NewClient(model.Config{Provider: "openai", Model: "qwen3", BaseURL: ts.URL, APIKey: "test-key"})
			resp, err := client.Generate(context.Background(), &model.GenerateRequest{Prompt: "hello"})
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "upstream failure") {
					t.Fatalf("Generate error = %v, want upstream response body", err)
				}
				return
			}
			if err != nil || resp == nil || resp.Content == "" {
				t.Fatalf("Generate = (%v, %v), want fallback response", resp, err)
			}
		})
	}
}
```

Add one test where a `roundTripFunc` returns `errors.New("offline")`, and one where `APIKey` is empty and `WithSecretResolver` returns an empty key; both must return the same non-empty fallback response as Gemini.

- [x] **Step 5: Run model-client tests and commit.**

Run: `go test ./internal/model -count=1`

Expected: PASS. Commit with `feat(model): support OpenAI-compatible chat completions`.

## Task 3: Document Provider Configuration and Verify the Package

**Files:** `docs/manifests.md`, `pkg/apis/v1alpha1/types_test.go`, `internal/model/client_test.go`

- [x] **Step 1: Document the OpenAI Model.** Add this example to `docs/manifests.md` and explain that omitting `baseUrl` uses OpenAI's public API root:

```bash
kubectl create secret generic openai-api-secret --from-literal=OPENAI_API_KEY="$OPENAI_API_KEY"
```

```yaml
apiVersion: ax.io/v1alpha1
kind: Model
metadata:
  name: default-model
  atespace: default
spec:
  provider: openai
  model: gpt-4.1-mini
  baseUrl: https://api.openai.com/v1
  secretKey:
    name: openai-api-secret
    key: OPENAI_API_KEY
```

- [x] **Step 2: Document compatible services.** Add a second example using `provider: openai`, `model: qwen3`, `baseUrl: https://models.example/v1`, and a `synthetic-api-secret` SecretKeyRef. State that users replace the illustrative URL with their service URL and that the endpoint must implement OpenAI Chat Completions with Bearer authentication. Replace the current Anthropic setup instructions with a note that Anthropic is not implemented in this release.

- [x] **Step 3: Verify API and adapter tests.**

Run: `go test ./pkg/apis/v1alpha1 ./internal/model -count=1`

Expected: PASS, including YAML round-trip, default/custom endpoint, request/response, usage, and fallback cases.

- [x] **Step 4: Run repository checks.**

Run: `go test ./... && go vet ./...`

Expected: all Go tests pass and `go vet` exits successfully. No new module dependency is expected.

- [x] **Step 5: Commit the documentation.** Commit with `docs: document OpenAI-compatible model configuration`.
