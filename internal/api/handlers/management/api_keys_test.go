package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func writeAPIKeysTestConfig(t *testing.T, raw string) (*Handler, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return &Handler{cfg: cfg, configFilePath: path}, path
}

func apiKeysTestRouter(h *Handler) *gin.Engine {
	router := gin.New()
	router.GET("/v0/management/api-keys", h.GetAPIKeys)
	router.PUT("/v0/management/api-keys", h.PutAPIKeys)
	router.PATCH("/v0/management/api-keys", h.PatchAPIKeys)
	router.DELETE("/v0/management/api-keys", h.DeleteAPIKeys)
	router.GET("/v8/management/config/*path", h.ConfigV8)
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	router.PATCH("/v8/management/config/*path", h.ConfigV8)
	router.DELETE("/v8/management/config/*path", h.ConfigV8)
	return router
}

func serveAPIKeysRequest(t *testing.T, router *gin.Engine, method, target, body string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(method, target, strings.NewReader(body)))
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s: status=%d body=%s", method, target, recorder.Code, recorder.Body.String())
	}
	return recorder
}

func decodeAPIKeyViews(t *testing.T, recorder *httptest.ResponseRecorder) []APIKeyEntryView {
	t.Helper()
	var wrapper struct {
		APIKeys []APIKeyEntryView `json:"api-keys"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &wrapper); err != nil {
		t.Fatalf("decode views: %v (%s)", err, recorder.Body.String())
	}
	return wrapper.APIKeys
}

func TestManagementAPIKeysViewsAndMutations(t *testing.T) {
	billing.ResetForTesting()
	t.Cleanup(billing.ResetForTesting)

	h, path := writeAPIKeysTestConfig(t, `config-version: 8
server: {port: 8317}
access:
  api-keys:
    - key: key-a
      name: Alpha
      quota: 5
    - key: key-b
`)
	month := billing.MonthKey(time.Now())
	billing.AddSpendForTesting(billing.HashAPIKey("key-a"), month, 1.25)
	router := apiKeysTestRouter(h)

	views := decodeAPIKeyViews(t, serveAPIKeysRequest(t, router, http.MethodGet, "/v0/management/api-keys", "", http.StatusOK))
	if len(views) != 2 {
		t.Fatalf("views = %+v", views)
	}
	if views[0].Key != "key-a" || views[0].Name != "Alpha" || views[0].Quota == nil || *views[0].Quota != 5 {
		t.Fatalf("first view = %+v", views[0])
	}
	if views[0].Used != 1.25 || views[0].CurrentMonth != month || views[0].KeyHash != billing.HashAPIKey("key-a") {
		t.Fatalf("billing fields = %+v", views[0])
	}
	if views[1].Quota != nil || views[1].Used != 0 {
		t.Fatalf("second view = %+v", views[1])
	}

	// Read-only fields in the request must be stripped before persisting.
	serveAPIKeysRequest(t, router, http.MethodPut, "/v0/management/api-keys",
		`[{"key":"key-a","name":"Updated","quota":10,"used":99,"current-month":"1999-01","key-hash":"bogus"},{"key":"key-c"}]`,
		http.StatusOK)
	if len(h.cfg.APIKeys) != 2 || h.cfg.APIKeys[0].Key != "key-a" || h.cfg.APIKeys[0].Name != "Updated" {
		t.Fatalf("cfg entries = %+v", h.cfg.APIKeys)
	}
	if h.cfg.APIKeys[0].Quota == nil || *h.cfg.APIKeys[0].Quota != 10 {
		t.Fatalf("quota not persisted: %+v", h.cfg.APIKeys[0])
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.APIKeys) != 2 || loaded.APIKeys[1].Key != "key-c" {
		t.Fatalf("persisted entries = %+v", loaded.APIKeys)
	}
	saved, _ := os.ReadFile(path)
	for _, readOnly := range []string{"used:", "current-month:", "key-hash:"} {
		if strings.Contains(string(saved), readOnly) {
			t.Fatalf("read-only field %q persisted: %s", readOnly, saved)
		}
	}

	serveAPIKeysRequest(t, router, http.MethodPatch, "/v0/management/api-keys",
		`{"index":1,"value":{"key":"key-c2","name":"C"}}`, http.StatusOK)
	serveAPIKeysRequest(t, router, http.MethodPatch, "/v0/management/api-keys",
		`{"old":{"key":"key-a"},"new":{"key":"key-a2","quota":3}}`, http.StatusOK)
	if len(h.cfg.APIKeys) != 2 || h.cfg.APIKeys[0].Key != "key-a2" || h.cfg.APIKeys[1].Key != "key-c2" {
		t.Fatalf("patch result = %+v", h.cfg.APIKeys)
	}
	if h.cfg.APIKeys[0].Quota == nil || *h.cfg.APIKeys[0].Quota != 3 {
		t.Fatalf("patched quota = %+v", h.cfg.APIKeys[0])
	}

	serveAPIKeysRequest(t, router, http.MethodDelete, "/v0/management/api-keys?value=key-c2", "", http.StatusOK)
	serveAPIKeysRequest(t, router, http.MethodDelete, "/v0/management/api-keys?index=0", "", http.StatusOK)
	if len(h.cfg.APIKeys) != 0 {
		t.Fatalf("delete result = %+v", h.cfg.APIKeys)
	}
}

func TestManagementV8APIKeysViewsAndMutations(t *testing.T) {
	billing.ResetForTesting()
	t.Cleanup(billing.ResetForTesting)

	h, path := writeAPIKeysTestConfig(t, `config-version: 8
server: {port: 8317}
access:
  api-keys:
    - key: v8-a
      name: A
      quota: 2
`)
	month := billing.MonthKey(time.Now())
	billing.AddSpendForTesting(billing.HashAPIKey("v8-a"), month, 0.5)
	router := apiKeysTestRouter(h)

	recorder := serveAPIKeysRequest(t, router, http.MethodGet, "/v8/management/config/access/api-keys", "", http.StatusOK)
	var views []APIKeyEntryView
	if err := json.Unmarshal(recorder.Body.Bytes(), &views); err != nil {
		t.Fatalf("decode v8 views: %v (%s)", err, recorder.Body.String())
	}
	if len(views) != 1 || views[0].Key != "v8-a" || views[0].Used != 0.5 || views[0].CurrentMonth != month || views[0].KeyHash != billing.HashAPIKey("v8-a") {
		t.Fatalf("v8 view = %+v", views)
	}

	serveAPIKeysRequest(t, router, http.MethodPut, "/v8/management/config/access/api-keys",
		`[{"key":"v8-b","name":"B","quota":4,"used":99,"current-month":"1999-01","key-hash":"bogus"}]`,
		http.StatusOK)
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.APIKeys) != 1 || loaded.APIKeys[0].Key != "v8-b" || loaded.APIKeys[0].Name != "B" {
		t.Fatalf("v8 persisted entry = %+v", loaded.APIKeys)
	}
	if loaded.APIKeys[0].Quota == nil || *loaded.APIKeys[0].Quota != 4 {
		t.Fatalf("v8 persisted quota = %+v", loaded.APIKeys[0])
	}
	saved, _ := os.ReadFile(path)
	for _, readOnly := range []string{"used:", "current-month:", "key-hash:"} {
		if strings.Contains(string(saved), readOnly) {
			t.Fatalf("v8 read-only field %q persisted: %s", readOnly, saved)
		}
	}

	serveAPIKeysRequest(t, router, http.MethodPatch, "/v8/management/config/access/api-keys",
		`{"index":0,"value":{"key":"v8-c"}}`, http.StatusOK)
	serveAPIKeysRequest(t, router, http.MethodPatch, "/v8/management/config/access/api-keys",
		`{"old":{"key":"v8-c"},"new":{"key":"v8-d","name":"D"}}`, http.StatusOK)
	loaded, err = config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.APIKeys) != 1 || loaded.APIKeys[0].Key != "v8-d" || loaded.APIKeys[0].Name != "D" {
		t.Fatalf("v8 patch result = %+v", loaded.APIKeys)
	}

	serveAPIKeysRequest(t, router, http.MethodDelete, "/v8/management/config/access/api-keys?value=v8-d", "", http.StatusOK)
	loaded, err = config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.APIKeys) != 0 {
		t.Fatalf("v8 delete result = %+v", loaded.APIKeys)
	}

	serveAPIKeysRequest(t, router, http.MethodPut, "/v8/management/config/access/api-keys",
		`[{"key":"v8-e"},{"key":"v8-f"}]`, http.StatusOK)
	serveAPIKeysRequest(t, router, http.MethodDelete, "/v8/management/config/access/api-keys?index=0", "", http.StatusOK)
	loaded, err = config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.APIKeys) != 1 || loaded.APIKeys[0].Key != "v8-f" {
		t.Fatalf("v8 delete index result = %+v", loaded.APIKeys)
	}
}

func TestManagementAPIKeysRejectsInvalidPatch(t *testing.T) {
	h, _ := writeAPIKeysTestConfig(t, "config-version: 8\nserver: {port: 8317}\naccess:\n  api-keys:\n    - key: a\n")
	router := apiKeysTestRouter(h)
	serveAPIKeysRequest(t, router, http.MethodPatch, "/v0/management/api-keys", `{"index":5,"value":{"key":"b"}}`, http.StatusBadRequest)
	serveAPIKeysRequest(t, router, http.MethodPatch, "/v0/management/api-keys", `{}`, http.StatusBadRequest)
	serveAPIKeysRequest(t, router, http.MethodPut, "/v0/management/api-keys", `[{"key":""}]`, http.StatusOK)
	if len(h.cfg.APIKeys) != 0 {
		t.Fatalf("empty key should be dropped: %+v", h.cfg.APIKeys)
	}
}
