package gemini

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestGeminiGetHandlerAppliesClientKeyModelPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const modelID = "gemini-policy-detail-model"

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "gemini-policy-detail-auth", Provider: "gemini", Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		APIKeys: []sdkconfig.APIKeyEntry{
			{Key: "denied", Models: []string{"other-model"}},
			{Key: "allowed", Models: []string{modelID}},
		},
	}, manager)
	handler := NewGeminiAPIHandler(base)

	router := gin.New()
	router.GET("/v1beta/models/*action", func(c *gin.Context) {
		c.Set("userApiKey", c.Query("key"))
		handler.GeminiGetHandler(c)
	})

	denied := httptest.NewRecorder()
	router.ServeHTTP(denied, httptest.NewRequest(http.MethodGet, "/v1beta/models/"+modelID+"?key=denied", nil))
	if denied.Code != http.StatusNotFound {
		t.Fatalf("denied detail status = %d body=%s", denied.Code, denied.Body.String())
	}

	allowed := httptest.NewRecorder()
	router.ServeHTTP(allowed, httptest.NewRequest(http.MethodGet, "/v1beta/models/"+modelID+"?key=allowed", nil))
	if allowed.Code != http.StatusOK {
		t.Fatalf("allowed detail status = %d body=%s", allowed.Code, allowed.Body.String())
	}
}
