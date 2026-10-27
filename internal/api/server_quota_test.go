package api

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAuthMiddlewareQuotaExceededReturns429(t *testing.T) {
	billing.ResetForTesting()
	t.Cleanup(billing.ResetForTesting)

	quota := 2.5
	cfg := &proxyconfig.Config{SDKConfig: sdkconfig.SDKConfig{
		APIKeys: []sdkconfig.APIKeyEntry{{Key: "quota-key", Name: "quota", Quota: &quota}},
	}}
	server := newTestServerWithConfig(t, cfg)
	billing.AddSpendForTesting(billing.HashAPIKey("quota-key"), billing.MonthKey(time.Now()), quota)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer quota-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (%s)", err, recorder.Body.String())
	}
	if body.Error.Type != "rate_limit_error" || body.Error.Code != "insufficient_quota" {
		t.Fatalf("error envelope = %+v", body.Error)
	}
	if body.Error.Message != "API key monthly quota exceeded ($2.5000 / $2.50)" {
		t.Fatalf("message = %q", body.Error.Message)
	}
}

func TestAuthMiddlewareUnderQuotaPasses(t *testing.T) {
	billing.ResetForTesting()
	t.Cleanup(billing.ResetForTesting)

	quota := 2.5
	cfg := &proxyconfig.Config{SDKConfig: sdkconfig.SDKConfig{
		APIKeys: []sdkconfig.APIKeyEntry{{Key: "quota-key", Quota: &quota}},
	}}
	server := newTestServerWithConfig(t, cfg)
	billing.AddSpendForTesting(billing.HashAPIKey("quota-key"), billing.MonthKey(time.Now()), 1.0)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer quota-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code == http.StatusTooManyRequests {
		t.Fatalf("under-quota request rejected: %s", recorder.Body.String())
	}
}

type quotaResponseBody struct {
	Name         string   `json:"name"`
	KeyHash      string   `json:"key_hash"`
	CurrentMonth string   `json:"current_month"`
	Quota        *float64 `json:"quota"`
	Used         float64  `json:"used"`
	Remaining    *float64 `json:"remaining"`
	Exceeded     bool     `json:"exceeded"`
}

func newQuotaTestServer(t *testing.T, entries ...sdkconfig.APIKeyEntry) *Server {
	t.Helper()
	billing.ResetForTesting()
	t.Cleanup(billing.ResetForTesting)
	return newTestServerWithConfig(t, &proxyconfig.Config{SDKConfig: sdkconfig.SDKConfig{APIKeys: entries}})
}

func performQuotaRequest(t *testing.T, server *Server, method, target string, prepare func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if prepare != nil {
		prepare(req)
	}
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)
	return recorder
}

func decodeQuotaBody(t *testing.T, recorder *httptest.ResponseRecorder) (quotaResponseBody, map[string]json.RawMessage) {
	t.Helper()
	var body quotaResponseBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (%s)", err, recorder.Body.String())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &fields); err != nil {
		t.Fatalf("decode fields: %v (%s)", err, recorder.Body.String())
	}
	return body, fields
}

func requireQuotaFloat(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

func TestQuotaEndpointRejectsMissingOrInvalidAPIKey(t *testing.T) {
	server := newQuotaTestServer(t, sdkconfig.APIKeyEntry{Key: "known-key", Name: "known"})

	tests := []struct {
		name        string
		target      string
		prepare     func(*http.Request)
		wantMessage string
	}{
		{name: "missing credentials", target: "/v1/quota", wantMessage: "Missing API key"},
		{name: "missing credentials alias", target: "/v1/usage", wantMessage: "Missing API key"},
		{
			name:        "unknown bearer token",
			target:      "/v1/quota",
			prepare:     func(req *http.Request) { req.Header.Set("Authorization", "Bearer unknown-key") },
			wantMessage: "Invalid API key",
		},
		{
			name:        "unknown x-api-key",
			target:      "/v1/quota",
			prepare:     func(req *http.Request) { req.Header.Set("X-Api-Key", "unknown-key") },
			wantMessage: "Invalid API key",
		},
		{
			name:        "unknown query key",
			target:      "/v1/usage?key=unknown-key",
			wantMessage: "Invalid API key",
		},
		{
			name:        "unknown auth token",
			target:      "/v1/usage?auth_token=unknown-key",
			wantMessage: "Invalid API key",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := performQuotaRequest(t, server, http.MethodGet, test.target, test.prepare)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 body=%s", recorder.Code, recorder.Body.String())
			}
			var body struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
					Code    string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v (%s)", err, recorder.Body.String())
			}
			if body.Error.Message != test.wantMessage {
				t.Fatalf("message = %q, want %q", body.Error.Message, test.wantMessage)
			}
			if body.Error.Type != "authentication_error" || body.Error.Code != "invalid_api_key" {
				t.Fatalf("error envelope = %+v", body.Error)
			}
		})
	}
}

func TestQuotaEndpointReportsPartialUsage(t *testing.T) {
	quota := 20.0
	server := newQuotaTestServer(t, sdkconfig.APIKeyEntry{Key: "quota-key", Name: "team-key", Quota: &quota})
	billing.AddSpendForTesting(billing.HashAPIKey("quota-key"), billing.MonthKey(time.Now()), 3.45)

	sources := []struct {
		name    string
		target  string
		prepare func(*http.Request)
	}{
		{
			name:    "authorization bearer",
			target:  "/v1/quota",
			prepare: func(req *http.Request) { req.Header.Set("Authorization", "Bearer quota-key") },
		},
		{
			name:    "x-api-key",
			target:  "/v1/quota",
			prepare: func(req *http.Request) { req.Header.Set("X-Api-Key", "quota-key") },
		},
		{
			name:    "x-goog-api-key",
			target:  "/v1/quota",
			prepare: func(req *http.Request) { req.Header.Set("X-Goog-Api-Key", "quota-key") },
		},
		{
			name:   "query key",
			target: "/v1/quota?key=quota-key",
		},
		{
			name:   "query auth token",
			target: "/v1/quota?auth_token=quota-key",
		},
		{
			name:   "usage alias with bearer",
			target: "/v1/usage",
			prepare: func(req *http.Request) {
				req.Header.Set("Authorization", "Bearer quota-key")
			},
		},
		{
			name:   "lower precedence source still matches",
			target: "/v1/quota",
			prepare: func(req *http.Request) {
				req.Header.Set("Authorization", "Bearer unknown-key")
				req.Header.Set("X-Api-Key", "quota-key")
			},
		},
	}

	for _, source := range sources {
		t.Run(source.name, func(t *testing.T) {
			recorder := performQuotaRequest(t, server, http.MethodGet, source.target, source.prepare)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 body=%s", recorder.Code, recorder.Body.String())
			}
			body, fields := decodeQuotaBody(t, recorder)
			if body.Name != "team-key" {
				t.Fatalf("name = %q, want team-key", body.Name)
			}
			if body.KeyHash != billing.HashAPIKey("quota-key") {
				t.Fatalf("key_hash = %q", body.KeyHash)
			}
			if body.CurrentMonth != billing.MonthKey(time.Now()) {
				t.Fatalf("current_month = %q", body.CurrentMonth)
			}
			if body.Quota == nil {
				t.Fatal("quota = null, want 20")
			}
			requireQuotaFloat(t, "quota", *body.Quota, 20)
			requireQuotaFloat(t, "used", body.Used, 3.45)
			if body.Remaining == nil {
				t.Fatal("remaining = null, want 16.55")
			}
			requireQuotaFloat(t, "remaining", *body.Remaining, 16.55)
			if body.Exceeded {
				t.Fatal("exceeded = true, want false")
			}
			if _, ok := fields["quota"]; !ok {
				t.Fatal("quota field missing from response")
			}
			if strings.Contains(recorder.Body.String(), "quota-key") {
				t.Fatalf("response leaked the raw API key: %s", recorder.Body.String())
			}
		})
	}
}

func TestQuotaEndpointServesExhaustedQuota(t *testing.T) {
	t.Run("spend at quota", func(t *testing.T) {
		quota := 2.5
		server := newQuotaTestServer(t, sdkconfig.APIKeyEntry{Key: "quota-key", Name: "spent", Quota: &quota})
		billing.AddSpendForTesting(billing.HashAPIKey("quota-key"), billing.MonthKey(time.Now()), quota)

		modelsRecorder := performQuotaRequest(t, server, http.MethodGet, "/v1/models", func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer quota-key")
		})
		if modelsRecorder.Code != http.StatusTooManyRequests {
			t.Fatalf("/v1/models status = %d, want 429 body=%s", modelsRecorder.Code, modelsRecorder.Body.String())
		}

		recorder := performQuotaRequest(t, server, http.MethodGet, "/v1/quota", func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer quota-key")
		})
		if recorder.Code != http.StatusOK {
			t.Fatalf("/v1/quota status = %d, want 200 body=%s", recorder.Code, recorder.Body.String())
		}
		body, _ := decodeQuotaBody(t, recorder)
		if body.Quota == nil || body.Remaining == nil {
			t.Fatalf("quota/remaining must be non-null: quota=%v remaining=%v", body.Quota, body.Remaining)
		}
		requireQuotaFloat(t, "quota", *body.Quota, 2.5)
		requireQuotaFloat(t, "used", body.Used, 2.5)
		requireQuotaFloat(t, "remaining", *body.Remaining, 0)
		if !body.Exceeded {
			t.Fatal("exceeded = false, want true")
		}
	})

	t.Run("zero quota", func(t *testing.T) {
		quota := 0.0
		server := newQuotaTestServer(t, sdkconfig.APIKeyEntry{Key: "zero-key", Quota: &quota})

		modelsRecorder := performQuotaRequest(t, server, http.MethodGet, "/v1/models", func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer zero-key")
		})
		if modelsRecorder.Code != http.StatusTooManyRequests {
			t.Fatalf("/v1/models status = %d, want 429 body=%s", modelsRecorder.Code, modelsRecorder.Body.String())
		}

		recorder := performQuotaRequest(t, server, http.MethodGet, "/v1/quota", func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer zero-key")
		})
		if recorder.Code != http.StatusOK {
			t.Fatalf("/v1/quota status = %d, want 200 body=%s", recorder.Code, recorder.Body.String())
		}
		body, fields := decodeQuotaBody(t, recorder)
		if _, ok := fields["name"]; ok {
			t.Fatalf("name should be omitted when empty: %s", recorder.Body.String())
		}
		if body.Quota == nil || body.Remaining == nil {
			t.Fatalf("quota/remaining must be non-null: quota=%v remaining=%v", body.Quota, body.Remaining)
		}
		requireQuotaFloat(t, "quota", *body.Quota, 0)
		requireQuotaFloat(t, "remaining", *body.Remaining, 0)
		if !body.Exceeded {
			t.Fatal("exceeded = false, want true")
		}
	})
}

func TestQuotaEndpointReportsUnlimitedKey(t *testing.T) {
	server := newQuotaTestServer(t, sdkconfig.APIKeyEntry{Key: "unlimited-key"})
	billing.AddSpendForTesting(billing.HashAPIKey("unlimited-key"), billing.MonthKey(time.Now()), 987.65)

	recorder := performQuotaRequest(t, server, http.MethodGet, "/v1/quota", func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer unlimited-key")
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", recorder.Code, recorder.Body.String())
	}
	body, fields := decodeQuotaBody(t, recorder)
	if body.Quota != nil {
		t.Fatalf("quota = %v, want null", *body.Quota)
	}
	if body.Remaining != nil {
		t.Fatalf("remaining = %v, want null", *body.Remaining)
	}
	requireQuotaFloat(t, "used", body.Used, 987.65)
	if body.Exceeded {
		t.Fatal("exceeded = true, want false")
	}
	if raw, ok := fields["quota"]; !ok || string(raw) != "null" {
		t.Fatalf("quota field = %s, want null", raw)
	}
	if raw, ok := fields["remaining"]; !ok || string(raw) != "null" {
		t.Fatalf("remaining field = %s, want null", raw)
	}
}
