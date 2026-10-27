package configaccess

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Register ensures the config-access provider is available to the access manager.
func Register(cfg *sdkconfig.SDKConfig) {
	if cfg == nil {
		sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeConfigAPIKey)
		return
	}

	entries := normalizeEntries(cfg.APIKeys)
	if len(entries) == 0 {
		sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeConfigAPIKey)
		return
	}

	sdkaccess.RegisterProvider(
		sdkaccess.AccessProviderTypeConfigAPIKey,
		newProvider(sdkaccess.DefaultAccessProviderName, entries),
	)
}

type provider struct {
	name    string
	entries map[string]apiKeyEntry
}

type apiKeyEntry struct {
	key     string
	name    string
	quota   *float64
	keyHash string
}

func newProvider(name string, entries []apiKeyEntry) *provider {
	providerName := strings.TrimSpace(name)
	if providerName == "" {
		providerName = sdkaccess.DefaultAccessProviderName
	}
	byKey := make(map[string]apiKeyEntry, len(entries))
	for _, entry := range entries {
		if entry.key == "" {
			continue
		}
		byKey[entry.key] = entry
	}
	return &provider{name: providerName, entries: byKey}
}

func (p *provider) Identifier() string {
	if p == nil || p.name == "" {
		return sdkaccess.DefaultAccessProviderName
	}
	return p.name
}

func (p *provider) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if p == nil {
		return nil, sdkaccess.NewNotHandledError()
	}
	if len(p.entries) == 0 {
		return nil, sdkaccess.NewNotHandledError()
	}
	candidates, provided := RequestAPIKeyCandidates(r)
	if !provided {
		return nil, sdkaccess.NewNoCredentialsError()
	}

	for _, candidate := range candidates {
		if candidate.Value == "" {
			continue
		}
		entry, ok := p.entries[candidate.Value]
		if !ok {
			continue
		}
		if entry.quota != nil {
			spent := billing.SpentUSD(entry.keyHash, time.Now())
			if *entry.quota <= 0 || spent >= *entry.quota {
				return nil, sdkaccess.NewInsufficientQuotaError(billing.FormatMonthlyQuotaMessage(spent, *entry.quota))
			}
		}
		return &sdkaccess.Result{
			Provider:  p.Identifier(),
			Principal: candidate.Value,
			Metadata: map[string]string{
				"source": candidate.Source,
			},
		}, nil
	}

	return nil, sdkaccess.NewInvalidCredentialError()
}

// APIKeyCandidate is one client credential source in the precedence order the
// config access provider evaluates it.
type APIKeyCandidate struct {
	// Value is the raw credential; empty when the source was not provided.
	Value string
	// Source identifies the credential source in authentication metadata.
	Source string
}

// RequestAPIKeyCandidates extracts client credential candidates from r in the
// same precedence order used for config API keys. The bool reports whether the
// request supplied any credential source at all, which distinguishes a missing
// credential from an invalid one.
func RequestAPIKeyCandidates(r *http.Request) ([]APIKeyCandidate, bool) {
	if r == nil {
		return nil, false
	}
	authHeader := r.Header.Get("Authorization")
	authHeaderGoogle := r.Header.Get("X-Goog-Api-Key")
	authHeaderAnthropic := r.Header.Get("X-Api-Key")
	queryKey := ""
	queryAuthToken := ""
	if r.URL != nil {
		queryKey = r.URL.Query().Get("key")
		queryAuthToken = r.URL.Query().Get("auth_token")
	}
	if authHeader == "" && authHeaderGoogle == "" && authHeaderAnthropic == "" && queryKey == "" && queryAuthToken == "" {
		return nil, false
	}

	return []APIKeyCandidate{
		{Value: extractBearerToken(authHeader), Source: "authorization"},
		{Value: authHeaderGoogle, Source: "x-goog-api-key"},
		{Value: authHeaderAnthropic, Source: "x-api-key"},
		{Value: queryKey, Source: "query-key"},
		{Value: queryAuthToken, Source: "query-auth-token"},
	}, true
}

func extractBearerToken(header string) string {
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return header
	}
	if strings.ToLower(parts[0]) != "bearer" {
		return header
	}
	return strings.TrimSpace(parts[1])
}

func normalizeEntries(entries []sdkaccess.APIKeyEntry) []apiKeyEntry {
	if len(entries) == 0 {
		return nil
	}
	normalized := make([]apiKeyEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		trimmedKey := strings.TrimSpace(entry.Key)
		if trimmedKey == "" {
			continue
		}
		if _, exists := seen[trimmedKey]; exists {
			continue
		}
		seen[trimmedKey] = struct{}{}
		var quota *float64
		if entry.Quota != nil {
			value := *entry.Quota
			quota = &value
		}
		normalized = append(normalized, apiKeyEntry{
			key:     trimmedKey,
			name:    strings.TrimSpace(entry.Name),
			quota:   quota,
			keyHash: billing.HashAPIKey(trimmedKey),
		})
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}
