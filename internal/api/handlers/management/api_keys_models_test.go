package management

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestManagementAPIKeyModelPolicyLifecycle(t *testing.T) {
	billing.ResetForTesting()
	t.Cleanup(billing.ResetForTesting)

	h, path := writeAPIKeysTestConfig(t, `config-version: 8
server: {port: 8317}
access:
  api-keys:
    - key: key-a
      name: Alpha
      models: [" GPT-* ", "gpt-*"]
      excluded-models: [" *-PREVIEW "]
`)
	router := apiKeysTestRouter(h)

	views := decodeAPIKeyViews(t, serveAPIKeysRequest(t, router, http.MethodGet, "/v0/management/api-keys", "", http.StatusOK))
	if len(views) != 1 {
		t.Fatalf("views = %+v", views)
	}
	if len(views[0].Models) != 1 || views[0].Models[0] != "gpt-*" {
		t.Fatalf("view models = %#v", views[0].Models)
	}
	if len(views[0].ExcludedModels) != 1 || views[0].ExcludedModels[0] != "*-preview" {
		t.Fatalf("view excluded-models = %#v", views[0].ExcludedModels)
	}

	// A PATCH that omits the policy fields keeps the existing restrictions.
	serveAPIKeysRequest(t, router, http.MethodPatch, "/v0/management/api-keys",
		`{"index":0,"value":{"key":"key-a","name":"Renamed","quota":7}}`, http.StatusOK)
	if len(h.cfg.APIKeys) != 1 {
		t.Fatalf("patch entries = %+v", h.cfg.APIKeys)
	}
	entry := h.cfg.APIKeys[0]
	if entry.Name != "Renamed" || len(entry.Models) != 1 || entry.Models[0] != "gpt-*" || len(entry.ExcludedModels) != 1 {
		t.Fatalf("patch stripped policy: %+v", entry)
	}

	// An explicit empty array clears the policy.
	serveAPIKeysRequest(t, router, http.MethodPatch, "/v0/management/api-keys",
		`{"old":{"key":"key-a"},"new":{"key":"key-a","models":[]}}`, http.StatusOK)
	entry = h.cfg.APIKeys[0]
	if entry.Models != nil {
		t.Fatalf("explicit empty models not cleared: %#v", entry.Models)
	}

	// PUT normalizes incoming patterns before persisting.
	serveAPIKeysRequest(t, router, http.MethodPut, "/v0/management/api-keys",
		`[{"key":"key-b","models":[" CLAUDE-* ","claude-*"],"excluded-models":["*-PREVIEW"]}]`, http.StatusOK)
	loaded, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatalf("reload config: %v", errLoad)
	}
	if len(loaded.APIKeys) != 1 || loaded.APIKeys[0].Key != "key-b" {
		t.Fatalf("persisted entries = %+v", loaded.APIKeys)
	}
	if len(loaded.APIKeys[0].Models) != 1 || loaded.APIKeys[0].Models[0] != "claude-*" {
		t.Fatalf("persisted models = %#v", loaded.APIKeys[0].Models)
	}
	if len(loaded.APIKeys[0].ExcludedModels) != 1 || loaded.APIKeys[0].ExcludedModels[0] != "*-preview" {
		t.Fatalf("persisted excluded-models = %#v", loaded.APIKeys[0].ExcludedModels)
	}
}
