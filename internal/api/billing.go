package api

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// configureBilling publishes the current usage database and client-key names to
// the process-wide billing store. It is safe to call on every config reload.
func configureBilling(cfg *config.Config) {
	names := make(map[string]string)
	dsn := ""
	if cfg != nil {
		dsn = strings.TrimSpace(cfg.UsagePostgresDSN)
		for _, entry := range cfg.APIKeys {
			key := strings.TrimSpace(entry.Key)
			if key == "" {
				continue
			}
			names[billing.HashAPIKey(key)] = strings.TrimSpace(entry.Name)
		}
	}
	billing.Configure(dsn, names)
}
