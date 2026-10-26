package configaccess

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

func newQuotaTestProvider(t *testing.T, quota *float64) *provider {
	t.Helper()
	entries := normalizeEntries([]sdkaccess.APIKeyEntry{{Key: "client-key", Name: "test", Quota: quota}})
	if len(entries) != 1 {
		t.Fatal("failed to normalize test entry")
	}
	return newProvider("test-provider", entries)
}

func quotaRequest(key string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return req
}

func TestAuthenticateWithoutQuotaAlwaysAllows(t *testing.T) {
	billing.ResetForTesting()
	t.Cleanup(billing.ResetForTesting)
	billing.AddSpendForTesting(billing.HashAPIKey("client-key"), billing.MonthKey(time.Now()), 500)

	result, authErr := newQuotaTestProvider(t, nil).Authenticate(context.Background(), quotaRequest("client-key"))
	if authErr != nil {
		t.Fatalf("unexpected error: %v", authErr)
	}
	if result == nil || result.Principal != "client-key" {
		t.Fatalf("result = %+v", result)
	}
}

func TestAuthenticateUnderQuotaAllows(t *testing.T) {
	billing.ResetForTesting()
	t.Cleanup(billing.ResetForTesting)
	quota := 10.0
	billing.AddSpendForTesting(billing.HashAPIKey("client-key"), billing.MonthKey(time.Now()), 9.99)

	if _, authErr := newQuotaTestProvider(t, &quota).Authenticate(context.Background(), quotaRequest("client-key")); authErr != nil {
		t.Fatalf("unexpected error: %v", authErr)
	}
}

func TestAuthenticateAtQuotaReturns429(t *testing.T) {
	billing.ResetForTesting()
	t.Cleanup(billing.ResetForTesting)
	quota := 10.0
	billing.AddSpendForTesting(billing.HashAPIKey("client-key"), billing.MonthKey(time.Now()), 10)

	_, authErr := newQuotaTestProvider(t, &quota).Authenticate(context.Background(), quotaRequest("client-key"))
	if authErr == nil {
		t.Fatal("expected quota error")
	}
	if authErr.StatusCode != http.StatusTooManyRequests || authErr.Code != sdkaccess.AuthErrorCodeInsufficientQuota {
		t.Fatalf("error = %+v, want 429 insufficient_quota", authErr)
	}
	if got := authErr.Message; got != "API key monthly quota exceeded ($10.0000 / $10.00)" {
		t.Fatalf("message = %q", got)
	}
}

func TestAuthenticateNonPositiveQuotaReturns429(t *testing.T) {
	billing.ResetForTesting()
	t.Cleanup(billing.ResetForTesting)
	quota := 0.0

	_, authErr := newQuotaTestProvider(t, &quota).Authenticate(context.Background(), quotaRequest("client-key"))
	if authErr == nil || authErr.Code != sdkaccess.AuthErrorCodeInsufficientQuota {
		t.Fatalf("error = %+v, want insufficient_quota", authErr)
	}
}

func TestAuthenticateRejectsUnknownKey(t *testing.T) {
	billing.ResetForTesting()
	t.Cleanup(billing.ResetForTesting)
	quota := 1.0

	_, authErr := newQuotaTestProvider(t, &quota).Authenticate(context.Background(), quotaRequest("other-key"))
	if authErr == nil || authErr.Code != sdkaccess.AuthErrorCodeInvalidCredential {
		t.Fatalf("error = %+v, want invalid_credential", authErr)
	}
}

func TestAuthenticateMissingCredentials(t *testing.T) {
	_, authErr := newQuotaTestProvider(t, nil).Authenticate(context.Background(), quotaRequest(""))
	if authErr == nil || authErr.Code != sdkaccess.AuthErrorCodeNoCredentials {
		t.Fatalf("error = %+v, want no_credentials", authErr)
	}
}
