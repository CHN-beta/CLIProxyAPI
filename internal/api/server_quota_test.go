package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
