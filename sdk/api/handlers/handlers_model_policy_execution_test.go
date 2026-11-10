package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func restrictedClientKeyContext(key string) context.Context {
	return coreusage.WithAPIKey(context.Background(), key)
}

func assertModelNotAllowedError(t *testing.T, errMsgStatus int, errMsg error) {
	t.Helper()
	if errMsgStatus != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", errMsgStatus, http.StatusForbidden)
	}
	if errMsg == nil || !strings.Contains(errMsg.Error(), "model_not_allowed") {
		t.Fatalf("error = %v", errMsg)
	}
}

func TestExecuteWithAuthManagerBlocksDisallowedClientKeyModel(t *testing.T) {
	model := "handler-policy-blocked-model"
	executor := &interceptorCaptureExecutor{}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{
		APIKeys: []sdkconfig.APIKeyEntry{{Key: "key-a", Models: []string{"allowed-*"}}},
	})

	body, headers, errMsg := handler.ExecuteWithAuthManager(restrictedClientKeyContext("key-a"), OpenAI, model, []byte(`{"model":"`+model+`"}`), "")
	if body != nil || headers != nil || errMsg == nil {
		t.Fatalf("disallowed execute returned body=%q headers=%v error=%#v", body, headers, errMsg)
	}
	assertModelNotAllowedError(t, errMsg.StatusCode, errMsg.Error)
	if req, _ := executor.captured(); req.Model != "" {
		t.Fatalf("upstream executor ran for disallowed model: %#v", req)
	}
}

func TestExecuteWithAuthManagerBlocksDeniedClientKeyModel(t *testing.T) {
	model := "handler-policy-denied-preview"
	executor := &interceptorCaptureExecutor{}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{
		APIKeys: []sdkconfig.APIKeyEntry{{Key: "key-a", ExcludedModels: []string{"*-preview"}}},
	})

	_, _, errMsg := handler.ExecuteWithAuthManager(restrictedClientKeyContext("key-a"), OpenAI, model, []byte(`{"model":"`+model+`"}`), "")
	if errMsg == nil {
		t.Fatal("denied model executed")
	}
	assertModelNotAllowedError(t, errMsg.StatusCode, errMsg.Error)
}

func TestExecuteWithAuthManagerAllowsSuffixedModelForClientKey(t *testing.T) {
	model := "handler-policy-allowed-model"
	executor := &interceptorCaptureExecutor{}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{
		APIKeys: []sdkconfig.APIKeyEntry{{Key: "key-a", Models: []string{"handler-policy-*"}}},
	})

	body, _, errMsg := handler.ExecuteWithAuthManager(restrictedClientKeyContext("key-a"), OpenAI, model+"(high)", []byte(`{"model":"`+model+`(high)"}`), "")
	if errMsg != nil {
		t.Fatalf("allowed execute error = %#v", errMsg)
	}
	if string(body) != "ok" {
		t.Fatalf("allowed execute body = %q", body)
	}
	req, _ := executor.captured()
	if req.Model != model+"(high)" {
		t.Fatalf("executed model = %q", req.Model)
	}
}

func TestExecuteWithAuthManagerAllowsUnrestrictedClientKey(t *testing.T) {
	model := "handler-policy-unrestricted-model"
	executor := &interceptorCaptureExecutor{}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{
		APIKeys: []sdkconfig.APIKeyEntry{{Key: "key-a", Name: "unrestricted"}},
	})

	body, _, errMsg := handler.ExecuteWithAuthManager(restrictedClientKeyContext("key-a"), OpenAI, model, []byte(`{"model":"`+model+`"}`), "")
	if errMsg != nil || string(body) != "ok" {
		t.Fatalf("unrestricted execute body=%q error=%#v", body, errMsg)
	}
}

func TestCountAndStreamBlockDisallowedClientKeyModel(t *testing.T) {
	model := "handler-policy-count-stream-model"
	executor := &interceptorCaptureExecutor{}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{
		APIKeys: []sdkconfig.APIKeyEntry{{Key: "key-a", Models: []string{"allowed-*"}}},
	})
	ctx := restrictedClientKeyContext("key-a")

	_, _, errMsg := handler.ExecuteCountWithAuthManager(ctx, OpenAI, model, []byte(`{"model":"`+model+`"}`), "")
	if errMsg == nil {
		t.Fatal("count executed disallowed model")
	}
	assertModelNotAllowedError(t, errMsg.StatusCode, errMsg.Error)

	dataChan, headers, errChan := handler.ExecuteStreamWithAuthManager(ctx, OpenAI, model, []byte(`{"model":"`+model+`","stream":true}`), "")
	if dataChan != nil || headers != nil || errChan == nil {
		t.Fatalf("disallowed stream returned data=%v headers=%v errors=%v", dataChan, headers, errChan)
	}
	streamErr := <-errChan
	if streamErr == nil {
		t.Fatal("disallowed stream produced no error")
	}
	assertModelNotAllowedError(t, streamErr.StatusCode, streamErr.Error)
	if req, _ := executor.captured(); req.Model != "" {
		t.Fatalf("upstream executor ran for disallowed model: %#v", req)
	}
}

func ginContextWithUserAPIKey(key string) context.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("userApiKey", key)
	return context.WithValue(context.Background(), "gin", c)
}

func TestExecuteWithAuthManagerReadsClientKeyFromGinContext(t *testing.T) {
	model := "handler-policy-gin-context-model"
	executor := &interceptorCaptureExecutor{}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{
		APIKeys: []sdkconfig.APIKeyEntry{{Key: "key-a", Models: []string{"allowed-*"}}},
	})

	_, _, errMsg := handler.ExecuteWithAuthManager(ginContextWithUserAPIKey("key-a"), OpenAI, model, []byte(`{"model":"`+model+`"}`), "")
	if errMsg == nil {
		t.Fatal("gin-carried restricted key executed disallowed model")
	}
	assertModelNotAllowedError(t, errMsg.StatusCode, errMsg.Error)
}

func TestInternalModelExecutionBypassesClientKeyPolicy(t *testing.T) {
	model := "handler-policy-internal-model"
	executor := &interceptorCaptureExecutor{}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{
		APIKeys: []sdkconfig.APIKeyEntry{{Key: "key-a", Models: []string{"allowed-*"}}},
	})

	resp, errMsg := handler.ExecuteModel(restrictedClientKeyContext("key-a"), ModelExecutionRequest{
		EntryProtocol: OpenAI,
		ExitProtocol:  OpenAI,
		Model:         model,
		Body:          []byte(`{"model":"` + model + `"}`),
	})
	if errMsg != nil {
		t.Fatalf("internal execution error = %#v", errMsg)
	}
	if string(resp.Body) != "ok" {
		t.Fatalf("internal execution body = %q", resp.Body)
	}
}
