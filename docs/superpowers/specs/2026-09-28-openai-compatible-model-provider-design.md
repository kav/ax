# OpenAI-Compatible Model Provider Design

**Date:** 2026-09-28  
**Status:** Approved for specification review

## Goal

Allow AX workspace planning to call OpenAI and OpenAI-compatible model endpoints, including Synthetic Labs when its endpoint implements Chat Completions. Configure the provider once through the existing `Model` resource and reuse AX's Kubernetes Secret reference for credentials.

## Public Model Configuration

Add an optional `baseUrl` field to `ModelSpec`. For example:

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

For `provider: openai`, `baseUrl` defaults to `https://api.openai.com/v1` when omitted. Compatible services set it to their API root; AX trims trailing slashes and appends `/chat/completions`. Credentials continue to use `secretKey`; no API key is stored in the Model resource. `parameters` remains the provider-specific generation-parameter map.

## Request Flow

The model client dispatches `provider: openai` to a Chat Completions adapter. It sends a non-streaming JSON POST with Bearer authentication, the configured model, a system message when present, and the user prompt. Existing typed generation fields and `parameters` are mapped into the request, with per-request typed values taking precedence. The response's first choice supplies generated text; prompt, completion, and total token usage map to AX's existing `UsageStats`.

The adapter uses the existing HTTP client and context, does not add an SDK dependency, and follows the current Gemini client's remote/fallback behavior. Gemini dispatch and wire format remain unchanged. Unsupported provider names continue to return a clear error.

## Compatibility Boundary and Non-Goals

The custom endpoint must accept the OpenAI Chat Completions path, Bearer API keys, and request/response schema. This supports Synthetic Labs only if its endpoint meets that contract. A Synthetic-specific protocol, Anthropic API implementation, OpenAI Responses API, streaming, tool calls, custom headers, and per-task model selection are out of scope.

## Validation

- Add `baseUrl` to the protobuf Model schema as an optional, backward-compatible field and preserve it through Model YAML/JSON conversion.
- Unit-test default and custom endpoint selection, path construction, auth header, request messages/model/parameters, response content and usage, and error/fallback cases with `httptest`.
- Add a Model manifest round-trip test for `baseUrl` and document OpenAI plus OpenAI-compatible endpoint examples.
- Run `go test ./...` and `go vet ./...`.

## Delivery

Develop on `kav/ax` branch `feat/openai-compatible-model-provider`, based on upstream `main`. No cluster changes are part of this feature.
