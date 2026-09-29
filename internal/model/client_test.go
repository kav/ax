// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package model_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/ax/internal/model"
	"github.com/google/ax/internal/store/memory"
	"github.com/google/ax/pkg/apis/v1alpha1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestClient_Default(t *testing.T) {
	client := model.NewDefaultClient(model.WithDisableRemote(true))
	if client.Config().Model != model.DefaultModel {
		t.Fatalf("expected default model %q, got %q", model.DefaultModel, client.Config().Model)
	}
	if client.Config().Provider != model.ProviderGoogle {
		t.Fatalf("expected provider %q, got %q", model.ProviderGoogle, client.Config().Provider)
	}
	if client.Config().Name != model.DefaultModelResourceName {
		t.Fatalf("expected default name %q, got %q", model.DefaultModelResourceName, client.Config().Name)
	}
	if client.Config().SecretKey == nil {
		t.Fatalf("expected default secretKey not nil")
	}
	if client.Config().SecretKey.Name != model.DefaultSecretName || client.Config().SecretKey.Key != model.DefaultSecretKey {
		t.Errorf("expected secretKey %s/%s, got %s/%s", model.DefaultSecretName, model.DefaultSecretKey, client.Config().SecretKey.Name, client.Config().SecretKey.Key)
	}
	if len(client.Config().Parameters) != 0 {
		t.Errorf("expected default parameters to be empty, got %v", client.Config().Parameters)
	}

	resp, err := client.Generate(context.Background(), &model.GenerateRequest{
		Prompt: "Configure Go workspace",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Model != model.DefaultModel {
		t.Errorf("expected model %q, got %q", model.DefaultModel, resp.Model)
	}
	if resp.Content == "" {
		t.Errorf("expected non-empty content")
	}
}

func TestClient_FromConfig(t *testing.T) {
	cfg := model.Config{
		Provider: "google",
		Model:    "gemini-3.8-flash",
		Parameters: map[string]any{
			"temperature":       0.3,
			"maxOutputTokens":   2048,
			"systemInstruction": "System workspace planner",
		},
		DisableRemote: true,
	}

	client := model.NewClient(cfg)
	if client.Config().Model != "gemini-3.8-flash" {
		t.Errorf("expected model gemini-3.8-flash, got %s", client.Config().Model)
	}
	if client.Config().Parameters["temperature"] != 0.3 {
		t.Errorf("expected temperature 0.3, got %v", client.Config().Parameters["temperature"])
	}

	resp, err := client.Generate(context.Background(), &model.GenerateRequest{
		Prompt: "Bootstrap workspace",
	})
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if resp.Model != "gemini-3.8-flash" {
		t.Errorf("expected gemini-3.8-flash, got %s", resp.Model)
	}
}

func TestConfigFromSpec_BaseURL(t *testing.T) {
	spec := &v1alpha1.ModelSpec{Provider: "openai", Model: "qwen3", BaseUrl: "https://models.example/v1"}
	cfg := model.ConfigFromSpec(spec)
	if cfg.BaseURL != "https://models.example/v1" {
		t.Fatalf("BaseURL = %q, want %q", cfg.BaseURL, "https://models.example/v1")
	}
	got := model.NewClient(cfg, model.WithSecretResolver(func(string, string) (string, error) { return "", nil })).Spec().GetBaseUrl()
	if got != "https://models.example/v1" {
		t.Errorf("Spec().GetBaseUrl() = %q, want %q", got, "https://models.example/v1")
	}
}

func TestClient_FromCRD(t *testing.T) {
	crd := &v1alpha1.Model{
		Metadata: &v1alpha1.ObjectMeta{
			Name:     "gemini-flash",
			Atespace: "default",
		},
		Spec: &v1alpha1.ModelSpec{
			Provider:   "google",
			Model:      "gemini-3.8-flash",
			Parameters: params(t, map[string]any{"temperature": 0.4, "maxOutputTokens": 1024}),
		},
	}

	client := model.NewClientFromCRD(crd, model.WithDisableRemote(true))
	if client.Config().Model != "gemini-3.8-flash" {
		t.Errorf("expected model gemini-3.8-flash, got %s", client.Config().Model)
	}

	resp, err := client.Generate(context.Background(), &model.GenerateRequest{
		Prompt: "Bootstrap workspace",
	})
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if resp.Model != "gemini-3.8-flash" {
		t.Errorf("expected gemini-3.8-flash, got %s", resp.Model)
	}
}

func TestClient_GeminiHTTP(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !r.URL.Query().Has("key") {
			t.Errorf("expected key in query params")
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"candidates": []map[string]interface{}{
				{
					"content": map[string]interface{}{
						"parts": []map[string]string{
							{"text": "Setup Go environment with Go 1.27"},
						},
					},
				},
			},
			"usageMetadata": map[string]int{
				"promptTokenCount":     10,
				"candidatesTokenCount": 20,
				"totalTokenCount":      30,
			},
		})
	}))
	defer ts.Close()

	client := model.NewClient(
		model.Config{
			Provider: "google",
			Model:    "gemini-3.8-flash",
			BaseURL:  ts.URL,
			APIKey:   "test-api-key",
		},
		model.WithHTTPClient(ts.Client()),
	)

	resp, err := client.Generate(context.Background(), &model.GenerateRequest{
		Prompt: "Plan Go setup",
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if resp.Content != "Setup Go environment with Go 1.27" {
		t.Errorf("unexpected content: %q", resp.Content)
	}
	if resp.Usage.TotalTokens != 30 {
		t.Errorf("expected 30 total tokens, got %d", resp.Usage.TotalTokens)
	}
}

func TestClient_OpenAIChatCompletionsHTTP(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s, want POST /v1/chat/completions", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer test-key")
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			TopP        float64 `json:"top_p"`
			Temperature float64 `json:"temperature"`
			MaxTokens   int     `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Model != "qwen3" || len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[0].Content != "Be concise" || body.Messages[1].Role != "user" || body.Messages[1].Content != "Plan a Go workspace" || body.TopP != 0.8 || body.Temperature != 0.2 || body.MaxTokens != 64 {
			t.Errorf("unexpected request body: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Use Go modules."}}],"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}}`))
	}))
	defer ts.Close()

	client := model.NewClient(model.Config{Provider: "openai", Model: "qwen3", BaseURL: ts.URL + "/v1", APIKey: "test-key", Parameters: map[string]any{"top_p": 0.8, "systemInstruction": "Be concise", "model": "malicious-model", "messages": []any{map[string]any{"role": "assistant", "content": "malicious message"}}}}, model.WithHTTPClient(ts.Client()))
	resp, err := client.Generate(context.Background(), &model.GenerateRequest{Prompt: "Plan a Go workspace", Temperature: 0.2, MaxTokens: 64})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if resp.Content != "Use Go modules." {
		t.Errorf("Content = %q", resp.Content)
	}
	if resp.Usage.PromptTokens != 12 || resp.Usage.CompletionTokens != 4 || resp.Usage.TotalTokens != 16 {
		t.Errorf("Usage = %+v", resp.Usage)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClient_OpenAIDefaultBaseURL(t *testing.T) {
	client := model.NewClient(model.Config{Provider: "openai", Model: "qwen3", APIKey: "test-key"}, model.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got, want := r.URL.String(), "https://api.openai.com/v1/chat/completions"; got != want {
			t.Errorf("URL = %q, want %q", got, want)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"choices":[]}`)), Header: make(http.Header)}, nil
	})}))
	if _, err := client.Generate(context.Background(), &model.GenerateRequest{Prompt: "test"}); err != nil {
		t.Fatal(err)
	}
}

func TestClient_OpenAIHTTPStatus(t *testing.T) {
	for _, tc := range []struct {
		status    int
		wantError string
	}{{401, ""}, {403, ""}, {429, ""}, {503, ""}, {500, "upstream failure"}} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			client := model.NewClient(model.Config{Provider: "openai", Model: "qwen3", BaseURL: "https://example.test", APIKey: "test-key"}, model.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader("upstream failure")), Header: make(http.Header)}, nil
			})}))
			resp, err := client.Generate(context.Background(), &model.GenerateRequest{Prompt: "test"})
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want containing %q", err, tc.wantError)
				}
				return
			}
			if err != nil || resp == nil || resp.Content == "" {
				t.Fatalf("expected fallback response, got response %v, error %v", resp, err)
			}
		})
	}
}

func TestClient_OpenAIFallbacks(t *testing.T) {
	t.Run("transport error", func(t *testing.T) {
		client := model.NewClient(model.Config{Provider: "openai", Model: "qwen3", BaseURL: "https://example.test", APIKey: "test-key"}, model.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("network down") })}))
		resp, err := client.Generate(context.Background(), &model.GenerateRequest{Prompt: "test"})
		if err != nil || resp == nil || resp.Content == "" {
			t.Fatalf("expected fallback response, got response %v, error %v", resp, err)
		}
	})
	t.Run("empty API key", func(t *testing.T) {
		client := model.NewClient(model.Config{Provider: "openai", Model: "qwen3", BaseURL: "https://example.test"}, model.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected HTTP request"); return nil, nil })}))
		resp, err := client.Generate(context.Background(), &model.GenerateRequest{Prompt: "test"})
		if err != nil || resp == nil || resp.Content == "" {
			t.Fatalf("expected fallback response, got response %v, error %v", resp, err)
		}
	})
}

func TestClient_DefaultModelSecretResolution(t *testing.T) {
	// 1. Resolve via in-cluster Kubernetes API simulation
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/namespaces/default/secrets/gemini-api-secret" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]string{
					"GEMINI_API_KEY": "a3ViZXJuZXRlcy1zZWNyZXQtdmFsdWUtNTU1", // base64 for "kubernetes-secret-value-555"
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	// Parse host and port from test server URL
	hostPort := strings.TrimPrefix(ts.URL, "http://")
	parts := strings.Split(hostPort, ":")
	t.Setenv("KUBERNETES_SERVICE_HOST", parts[0])
	t.Setenv("KUBERNETES_SERVICE_PORT", parts[1])

	// Create temp token file
	tmpDir := t.TempDir()
	tokenFile := filepath.Join(tmpDir, "token")
	_ = os.WriteFile(tokenFile, []byte("fake-token"), 0644)

	// 2. Resolve via WithSecretResolver (standard in-memory / test pattern)
	clientResolver := model.NewDefaultClient(
		model.WithSecretResolver(func(secretName, key string) (string, error) {
			if secretName == "gemini-api-secret" && key == "GEMINI_API_KEY" {
				return "resolved-via-custom-func-333", nil
			}
			return "", nil
		}),
		model.WithDisableRemote(true),
	)
	if clientResolver.Config().APIKey != "resolved-via-custom-func-333" {
		t.Errorf("expected APIKey from custom resolver, got %q", clientResolver.Config().APIKey)
	}
}

func TestClient_DefaultModelStore(t *testing.T) {
	ctx := context.Background()
	s := memory.NewStore()

	// Store a customized default-model CRD
	customModel := &v1alpha1.Model{
		Metadata: &v1alpha1.ObjectMeta{
			Name:     "default-model",
			Atespace: "default",
		},
		Spec: &v1alpha1.ModelSpec{
			Provider:   "google",
			Model:      "gemini-3.8-flash",
			Parameters: params(t, map[string]any{"temperature": 0.7, "maxOutputTokens": 4096}),
			SecretKey: &v1alpha1.SecretKeyRef{
				Name: "custom-gemini-secret",
				Key:  "CUSTOM_KEY",
			},
		},
	}
	if err := s.SaveModel(ctx, customModel); err != nil {
		t.Fatalf("failed to save model: %v", err)
	}

	secretResolverOpt := model.WithSecretResolver(func(secretName, key string) (string, error) {
		if secretName == "custom-gemini-secret" && key == "CUSTOM_KEY" {
			return "store-secret-resolved-999", nil
		}
		return "", nil
	})

	// Read using NewDefaultClientFromStore
	client, err := model.NewDefaultClientFromStore(ctx, s, "default", secretResolverOpt, model.WithDisableRemote(true))
	if err != nil {
		t.Fatalf("NewDefaultClientFromStore failed: %v", err)
	}

	if client.Config().Name != "default-model" {
		t.Errorf("expected name default-model, got %s", client.Config().Name)
	}
	if client.Config().Parameters["temperature"] != 0.7 {
		t.Errorf("expected temperature 0.7, got %v", client.Config().Parameters["temperature"])
	}
	if client.Config().Parameters["maxOutputTokens"] != float64(4096) {
		t.Errorf("expected maxOutputTokens 4096, got %v", client.Config().Parameters["maxOutputTokens"])
	}
	if client.Config().APIKey != "store-secret-resolved-999" {
		t.Errorf("expected APIKey 'store-secret-resolved-999', got %q", client.Config().APIKey)
	}

	// Read using WithStore option
	clientWithStore := model.NewDefaultClient(
		model.WithStore(ctx, s, "default", "default-model"),
		secretResolverOpt,
		model.WithDisableRemote(true),
	)
	if clientWithStore.Config().Parameters["temperature"] != 0.7 {
		t.Errorf("expected temperature 0.7, got %v", clientWithStore.Config().Parameters["temperature"])
	}
	if clientWithStore.Config().APIKey != "store-secret-resolved-999" {
		t.Errorf("expected APIKey 'store-secret-resolved-999', got %q", clientWithStore.Config().APIKey)
	}
}

// params builds a Struct for a ModelSpec's parameters, failing the test on
// unsupported value types.
func params(t *testing.T, m map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(m)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
