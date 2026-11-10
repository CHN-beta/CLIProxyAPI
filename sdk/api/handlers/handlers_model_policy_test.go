package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	claudemodels "github.com/router-for-me/CLIProxyAPI/v8/internal/client/claude/models"
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func newModelPolicyHandler(entries ...sdkconfig.APIKeyEntry) *BaseAPIHandler {
	return NewBaseAPIHandlers(&sdkconfig.SDKConfig{APIKeys: entries}, nil)
}

func newModelListContext(apiKey string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	if apiKey != "" {
		c.Set("userApiKey", apiKey)
	}
	return c, recorder
}

func modelIDsFromListBody(t *testing.T, body []byte, field string) []string {
	t.Helper()
	var envelope map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &envelope); errUnmarshal != nil {
		t.Fatalf("decode model list: %v (%s)", errUnmarshal, body)
	}
	var entries []map[string]any
	if errUnmarshal := json.Unmarshal(envelope[field], &entries); errUnmarshal != nil {
		t.Fatalf("decode model list field %q: %v (%s)", field, errUnmarshal, body)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		for _, key := range []string{"id", "name"} {
			if value, ok := entry[key].(string); ok && value != "" {
				ids = append(ids, value)
				break
			}
		}
	}
	return ids
}

func TestWriteModelListResponseFiltersOpenAIModelsByClientKeyPolicy(t *testing.T) {
	handler := newModelPolicyHandler(
		sdkconfig.APIKeyEntry{Key: "restricted", Models: []string{"gpt-*"}, ExcludedModels: []string{"*-preview"}},
		sdkconfig.APIKeyEntry{Key: "unrestricted"},
	)
	payload := gin.H{
		"object": "list",
		"data": []map[string]any{
			{"id": "gpt-5.5"},
			{"id": "gpt-5.5-preview"},
			{"id": "gemini-4-argon"},
		},
	}

	c, recorder := newModelListContext("restricted")
	handler.WriteModelListResponse(c, OpenAI, payload)
	got := modelIDsFromListBody(t, recorder.Body.Bytes(), "data")
	if len(got) != 1 || got[0] != "gpt-5.5" {
		t.Fatalf("restricted /v1/models ids = %v", got)
	}

	c, recorder = newModelListContext("unrestricted")
	handler.WriteModelListResponse(c, OpenAI, payload)
	got = modelIDsFromListBody(t, recorder.Body.Bytes(), "data")
	if len(got) != 3 {
		t.Fatalf("unrestricted /v1/models ids = %v", got)
	}
}

func TestWriteModelListResponseFiltersGeminiModelsByClientKeyPolicy(t *testing.T) {
	handler := newModelPolicyHandler(
		sdkconfig.APIKeyEntry{Key: "restricted", Models: []string{"gemini-2.5-*"}},
	)
	payload := gin.H{
		"models": []map[string]any{
			{"name": "models/gemini-2.5-pro"},
			{"name": "models/gemini-3-flash"},
			{"name": "models/gemini-2.5-flash"},
		},
	}

	c, recorder := newModelListContext("restricted")
	handler.WriteModelListResponse(c, Gemini, payload)
	got := modelIDsFromListBody(t, recorder.Body.Bytes(), "models")
	if len(got) != 2 || got[0] != "models/gemini-2.5-pro" || got[1] != "models/gemini-2.5-flash" {
		t.Fatalf("restricted /v1beta/models ids = %v", got)
	}

	c, recorder = newModelListContext("")
	handler.WriteModelListResponse(c, Gemini, payload)
	got = modelIDsFromListBody(t, recorder.Body.Bytes(), "models")
	if len(got) != 3 {
		t.Fatalf("keyless /v1beta/models ids = %v", got)
	}
}

func TestWriteModelListResponseMatchesCloakedAndSuffixedModels(t *testing.T) {
	cloakedCode := claudemodels.EnsureClaudeModelIDPrefix("code")
	handler := newModelPolicyHandler(
		sdkconfig.APIKeyEntry{Key: "claude-key", Models: []string{"code"}},
		sdkconfig.APIKeyEntry{Key: "gemini-key", Models: []string{"gemini-4-argon"}},
	)

	c, recorder := newModelListContext("claude-key")
	handler.WriteModelListResponse(c, Claude, gin.H{
		"data": []map[string]any{
			{"id": cloakedCode},
			{"id": claudemodels.EnsureClaudeModelIDPrefix("other-model")},
		},
		"first_id": claudemodels.EnsureClaudeModelIDPrefix("other-model"),
		"last_id":  claudemodels.EnsureClaudeModelIDPrefix("other-model"),
	})
	got := modelIDsFromListBody(t, recorder.Body.Bytes(), "data")
	if len(got) != 1 || got[0] != cloakedCode {
		t.Fatalf("cloaked ids = %v", got)
	}
	var cursor struct {
		FirstID string `json:"first_id"`
		LastID  string `json:"last_id"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &cursor); errUnmarshal != nil {
		t.Fatalf("decode cursor: %v", errUnmarshal)
	}
	if cursor.FirstID != cloakedCode || cursor.LastID != cloakedCode {
		t.Fatalf("cursors = %+v", cursor)
	}

	c, recorder = newModelListContext("gemini-key")
	handler.WriteModelListResponse(c, Gemini, gin.H{
		"models": []map[string]any{
			{"name": "models/gemini-4-argon"},
			{"name": "models/gemini-4-argon-high"},
		},
	})
	got = modelIDsFromListBody(t, recorder.Body.Bytes(), "models")
	if len(got) != 1 || got[0] != "models/gemini-4-argon" {
		t.Fatalf("gemini ids = %v", got)
	}
}

func TestWriteModelListResponseFiltersModelDetailLookup(t *testing.T) {
	handler := newModelPolicyHandler(
		sdkconfig.APIKeyEntry{Key: "restricted", Models: []string{"gpt-5.5"}},
	)
	payload := gin.H{
		"object": "list",
		"data": []map[string]any{
			{"id": "gpt-5.5"},
			{"id": "gemini-4-argon"},
		},
	}

	c, recorder := newModelListContext("restricted")
	c.Set(ModelDetailIDContextKey, "gemini-4-argon")
	handler.WriteModelListResponse(c, OpenAI, payload)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("disallowed detail status = %d body=%s", recorder.Code, recorder.Body.Bytes())
	}

	c, recorder = newModelListContext("restricted")
	c.Set(ModelDetailIDContextKey, "gpt-5.5")
	handler.WriteModelListResponse(c, OpenAI, payload)
	if recorder.Code != http.StatusOK {
		t.Fatalf("allowed detail status = %d body=%s", recorder.Code, recorder.Body.Bytes())
	}
	if !strings.Contains(recorder.Body.String(), `"id":"gpt-5.5"`) {
		t.Fatalf("allowed detail body = %s", recorder.Body.Bytes())
	}
}

func TestWriteModelListResponseFiltersCompactCodexPayload(t *testing.T) {
	handler := newModelPolicyHandler(
		sdkconfig.APIKeyEntry{Key: "restricted", Models: []string{"gpt-*"}},
	)
	payload := []byte(`{"models":[{"slug":"gpt-5.5"},{"slug":"gemini-4-argon"}]}`)

	c, recorder := newModelListContext("restricted")
	handler.WriteModelListResponse(c, OpenAI, payload)
	var envelope struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &envelope); errUnmarshal != nil {
		t.Fatalf("decode compact payload: %v (%s)", errUnmarshal, recorder.Body.Bytes())
	}
	if len(envelope.Models) != 1 || envelope.Models[0].Slug != "gpt-5.5" {
		t.Fatalf("compact model slugs = %+v", envelope.Models)
	}
}

func TestClientAPIKeyAllowsModelPatternSemantics(t *testing.T) {
	entries := []sdkconfig.APIKeyEntry{
		{Key: "both", Models: []string{"gpt-*"}, ExcludedModels: []string{"*-preview"}},
		{Key: "allow", Models: []string{"code"}},
		{Key: "deny", ExcludedModels: []string{"secret-*"}},
	}
	for _, tc := range []struct {
		name  string
		key   string
		model string
		want  bool
	}{
		{"allowlist match", "allow", "code", true},
		{"allowlist match with thinking suffix", "allow", "code(high)", true},
		{"allowlist match with colon suffix", "allow", "code:high", true},
		{"allowlist mismatch", "allow", "other", false},
		{"denylist match", "deny", "secret-model", false},
		{"denylist permits others", "deny", "public-model", true},
		{"denylist wins over allowlist", "both", "gpt-5.5-preview", false},
		{"allowlist permits non-denied", "both", "gpt-5.5", true},
		{"unknown key is unrestricted", "missing", "anything", true},
		{"models prefix stripped", "allow", "models/code", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClientAPIKeyAllowsModel(entries, tc.key, tc.model); got != tc.want {
				t.Fatalf("ClientAPIKeyAllowsModel(%q, %q) = %v, want %v", tc.key, tc.model, got, tc.want)
			}
		})
	}
}
