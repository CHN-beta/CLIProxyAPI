package api

import (
	"crypto/subtle"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	configaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
)

// clientQuotaResponse is the self-service quota and usage payload for one
// client API key. The raw key is never echoed back.
type clientQuotaResponse struct {
	Name         string   `json:"name,omitempty"`
	KeyHash      string   `json:"key_hash"`
	CurrentMonth string   `json:"current_month"`
	Quota        *float64 `json:"quota"`
	Used         float64  `json:"used"`
	Remaining    *float64 `json:"remaining"`
	Exceeded     bool     `json:"exceeded"`
}

// quotaKeyEntry is a config API key snapshot taken under the config lock so the
// response cannot race with a concurrent config reload or management edit.
type quotaKeyEntry struct {
	key   string
	name  string
	quota *float64
}

// handleClientQuota serves the client API key's own monthly quota and usage.
// It is registered outside AuthMiddleware on purpose: a key whose quota is
// exhausted must still be able to read its usage instead of receiving 429.
func (s *Server) handleClientQuota(c *gin.Context) {
	candidates, provided := configaccess.RequestAPIKeyCandidates(c.Request)
	if !provided {
		abortQuotaAuth(c, "Missing API key")
		return
	}
	entry, ok := s.lookupQuotaAPIKey(candidates)
	if !ok {
		abortQuotaAuth(c, "Invalid API key")
		return
	}

	now := time.Now()
	keyHash := billing.HashAPIKey(entry.key)
	used := billing.SpentUSD(keyHash, now)

	response := clientQuotaResponse{
		Name:         entry.name,
		KeyHash:      keyHash,
		CurrentMonth: billing.MonthKey(now),
		Used:         used,
	}
	if entry.quota != nil {
		quota := *entry.quota
		remaining := math.Max(0, quota-used)
		response.Quota = &quota
		response.Remaining = &remaining
		response.Exceeded = used >= quota
	}

	c.JSON(http.StatusOK, response)
}

// lookupQuotaAPIKey resolves the first credential candidate that matches a
// configured API key, preserving the config access provider's source order.
func (s *Server) lookupQuotaAPIKey(candidates []configaccess.APIKeyCandidate) (quotaKeyEntry, bool) {
	if s == nil {
		return quotaKeyEntry{}, false
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	if s.cfg == nil {
		return quotaKeyEntry{}, false
	}

	for _, candidate := range candidates {
		value := candidate.Value
		if value == "" {
			continue
		}
		for i := range s.cfg.APIKeys {
			configured := strings.TrimSpace(s.cfg.APIKeys[i].Key)
			if configured == "" {
				continue
			}
			if subtle.ConstantTimeCompare([]byte(configured), []byte(value)) != 1 {
				continue
			}
			entry := quotaKeyEntry{
				key:  configured,
				name: strings.TrimSpace(s.cfg.APIKeys[i].Name),
			}
			if limit := s.cfg.APIKeys[i].Quota; limit != nil {
				quota := *limit
				entry.quota = &quota
			}
			return entry, true
		}
	}

	return quotaKeyEntry{}, false
}

// abortQuotaAuth writes the standard OpenAI-compatible authentication error.
func abortQuotaAuth(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{
		"message": message,
		"type":    "authentication_error",
		"code":    "invalid_api_key",
	}})
}
