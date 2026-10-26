package management

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// queryIndex parses an optional ?index= query parameter.
func queryIndex(c *gin.Context) (*int, bool) {
	raw := strings.TrimSpace(c.Query("index"))
	if raw == "" {
		return nil, false
	}
	index, err := strconv.Atoi(raw)
	if err != nil {
		return nil, false
	}
	return &index, true
}

// APIKeyEntryView is the Management API representation of a client API key with
// its current billing state. The billing fields are read-only.
type APIKeyEntryView struct {
	Key          string   `json:"key"`
	Name         string   `json:"name,omitempty"`
	Quota        *float64 `json:"quota,omitempty"`
	Used         float64  `json:"used"`
	CurrentMonth string   `json:"current-month"`
	KeyHash      string   `json:"key-hash"`
}

// apiKeyViews enriches configured entries with current-month billing state.
func (h *Handler) apiKeyViews(entries []config.APIKeyEntry) []APIKeyEntryView {
	now := time.Now()
	month := billing.MonthKey(now)
	views := make([]APIKeyEntryView, 0, len(entries))
	for _, entry := range entries {
		key := strings.TrimSpace(entry.Key)
		if key == "" {
			continue
		}
		keyHash := billing.HashAPIKey(key)
		views = append(views, APIKeyEntryView{
			Key:          key,
			Name:         strings.TrimSpace(entry.Name),
			Quota:        entry.Quota,
			Used:         billing.SpentUSD(keyHash, now),
			CurrentMonth: month,
			KeyHash:      keyHash,
		})
	}
	return views
}

func parseAPIKeyEntryList(data []byte) ([]config.APIKeyEntry, error) {
	var entries []config.APIKeyEntry
	if err := json.Unmarshal(data, &entries); err == nil && entries != nil {
		return entries, nil
	}
	var wrapper struct {
		Items   []config.APIKeyEntry `json:"items"`
		APIKeys []config.APIKeyEntry `json:"api-keys"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, err
	}
	if wrapper.APIKeys != nil {
		return wrapper.APIKeys, nil
	}
	if wrapper.Items != nil {
		return wrapper.Items, nil
	}
	return nil, fmt.Errorf("invalid body")
}

type apiKeyPatchBody struct {
	Old   *config.APIKeyEntry `json:"old"`
	New   *config.APIKeyEntry `json:"new"`
	Index *int                `json:"index"`
	Value *config.APIKeyEntry `json:"value"`
}

func parseAPIKeyPatch(data []byte) (apiKeyPatchBody, error) {
	var body apiKeyPatchBody
	if err := json.Unmarshal(data, &body); err != nil {
		return body, err
	}
	return body, nil
}

// applyAPIKeyEntryPatch mutates entries according to the shared PATCH contract.
func applyAPIKeyEntryPatch(entries []config.APIKeyEntry, body apiKeyPatchBody) ([]config.APIKeyEntry, error) {
	if body.Index != nil && body.Value != nil {
		index := *body.Index
		if index < 0 || index >= len(entries) {
			return nil, fmt.Errorf("invalid index")
		}
		value, err := normalizeAPIKeyEntry(*body.Value)
		if err != nil {
			return nil, err
		}
		entries[index] = value
		return entries, nil
	}
	if body.Old != nil && body.New != nil {
		value, err := normalizeAPIKeyEntry(*body.New)
		if err != nil {
			return nil, err
		}
		oldKey := strings.TrimSpace(body.Old.Key)
		for i := range entries {
			if strings.TrimSpace(entries[i].Key) == oldKey {
				entries[i] = value
				return entries, nil
			}
		}
		return append(entries, value), nil
	}
	return nil, fmt.Errorf("missing fields")
}

func normalizeAPIKeyEntry(entry config.APIKeyEntry) (config.APIKeyEntry, error) {
	key := strings.TrimSpace(entry.Key)
	if key == "" {
		return config.APIKeyEntry{}, fmt.Errorf("key is required")
	}
	normalized := config.APIKeyEntry{Key: key, Name: strings.TrimSpace(entry.Name)}
	if entry.Quota != nil {
		quota := *entry.Quota
		normalized.Quota = &quota
	}
	return normalized, nil
}

func normalizeAPIKeyEntries(entries []config.APIKeyEntry) []config.APIKeyEntry {
	if len(entries) == 0 {
		return nil
	}
	normalized := make([]config.APIKeyEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		value, err := normalizeAPIKeyEntry(entry)
		if err != nil {
			continue
		}
		if _, exists := seen[value.Key]; exists {
			continue
		}
		seen[value.Key] = struct{}{}
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

// deleteAPIKeyEntries removes entries by index or by raw key value.
func deleteAPIKeyEntries(entries []config.APIKeyEntry, index *int, value string) ([]config.APIKeyEntry, bool) {
	if index != nil {
		if *index < 0 || *index >= len(entries) {
			return entries, false
		}
		return append(entries[:*index], entries[*index+1:]...), true
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return entries, false
	}
	filtered := make([]config.APIKeyEntry, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.Key) != trimmed {
			filtered = append(filtered, entry)
		}
	}
	return filtered, true
}

// api-keys (v0)
func (h *Handler) GetAPIKeys(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c.JSON(200, gin.H{"api-keys": h.apiKeyViews(h.cfg.APIKeys)})
}

func (h *Handler) PutAPIKeys(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	entries, err := parseAPIKeyEntryList(data)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.APIKeys = normalizeAPIKeyEntries(entries)
	h.persistLocked(c)
}

func (h *Handler) PatchAPIKeys(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(400, gin.H{"error": "failed to read body"})
		return
	}
	body, err := parseAPIKeyPatch(data)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entries, err := applyAPIKeyEntryPatch(append([]config.APIKeyEntry(nil), h.cfg.APIKeys...), body)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	h.cfg.APIKeys = entries
	h.persistLocked(c)
}

func (h *Handler) DeleteAPIKeys(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	index, hasIndex := queryIndex(c)
	value := c.Query("value")
	if !hasIndex && strings.TrimSpace(value) == "" {
		c.JSON(400, gin.H{"error": "missing index or value"})
		return
	}
	entries, ok := deleteAPIKeyEntries(h.cfg.APIKeys, index, value)
	if !ok {
		c.JSON(400, gin.H{"error": "missing index or value"})
		return
	}
	h.cfg.APIKeys = entries
	h.persistLocked(c)
}
