package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"

	claudemodels "github.com/router-for-me/CLIProxyAPI/v8/internal/client/claude/models"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"golang.org/x/net/context"
)

// PluginModelRouterHost routes matching requests to a plugin executor, the router's own executor,
// or a built-in provider before model-to-provider resolution and auth selection.
type PluginModelRouterHost interface {
	RouteModel(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool)
}

type pluginModelRouterSkipHost interface {
	RouteModelExcept(context.Context, pluginapi.ModelRouteRequest, string) (pluginapi.ModelRouteResponse, bool)
}

type modelRouterDetector interface {
	HasModelRouters() bool
}

type modelRouterSkipDetector interface {
	HasModelRoutersExcept(string) bool
}

func preferExecutionProvider(providers []string, preferred string) []string {
	preferred = strings.ToLower(strings.TrimSpace(preferred))
	if preferred == "" || len(providers) < 2 {
		return providers
	}
	preferredIndex := -1
	for i := range providers {
		if strings.ToLower(strings.TrimSpace(providers[i])) == preferred {
			preferredIndex = i
			break
		}
	}
	if preferredIndex <= 0 {
		return providers
	}
	out := make([]string, 0, len(providers))
	out = append(out, providers[preferredIndex])
	out = append(out, providers[:preferredIndex]...)
	out = append(out, providers[preferredIndex+1:]...)
	return out
}

func adjustExecutionProvidersForEntryProtocol(entryProtocol string, providers []string) []string {
	if entryProtocol == Interactions {
		return preferExecutionProvider(providers, GeminiInteractions)
	}
	if supportsNativeInteractionsEntryProtocol(entryProtocol) {
		return providers
	}
	return excludeExecutionProvider(providers, GeminiInteractions)
}

func supportsNativeInteractionsEntryProtocol(entryProtocol string) bool {
	switch entryProtocol {
	case Interactions, OpenAI, OpenaiResponse, Claude, Gemini:
		return true
	default:
		return false
	}
}

func excludeExecutionProvider(providers []string, excluded string) []string {
	excluded = strings.ToLower(strings.TrimSpace(excluded))
	if excluded == "" || len(providers) == 0 {
		return providers
	}
	excludedIndex := -1
	for i := range providers {
		if strings.ToLower(strings.TrimSpace(providers[i])) == excluded {
			excludedIndex = i
			break
		}
	}
	if excludedIndex == -1 {
		return providers
	}
	out := make([]string, 0, len(providers)-1)
	out = append(out, providers[:excludedIndex]...)
	out = append(out, providers[excludedIndex+1:]...)
	return out
}

func (h *BaseAPIHandler) getRequestDetails(modelName string) (providers []string, normalizedModel string, err *interfaces.ErrorMessage) {
	return h.getRequestDetailsWithOptions(modelName, false, false)
}

func validateNativeInteractionsExecution(entryProtocol string, execOptions modelExecutionOptions, routeDecision modelRouteDecision) *interfaces.ErrorMessage {
	forcedProvider := strings.ToLower(strings.TrimSpace(execOptions.ForcedProvider))
	if forcedProvider == "" || entryProtocol != Interactions {
		return nil
	}
	if routeDecision.ExecutorPluginID != "" {
		return nativeInteractionsExecutionError()
	}
	if routeProvider := strings.ToLower(strings.TrimSpace(routeDecision.Provider)); routeProvider != "" && routeProvider != forcedProvider {
		return nativeInteractionsExecutionError()
	}
	return nil
}

func nativeInteractionsExecutionError() *interfaces.ErrorMessage {
	return &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      fmt.Errorf("agent is only supported for native interactions execution"),
	}
}

// providersForExecution resolves the providers and normalized model for a request. When a model
// router selected a built-in provider, it skips model->provider resolution and uses the router's
// provider (with an optional target model); otherwise it falls back to the registry-based path.
func (h *BaseAPIHandler) providersForExecution(modelName, originalRequestedModel string, allowImageModel bool, routeDecision modelRouteDecision, execOptions modelExecutionOptions) ([]string, string, *interfaces.ErrorMessage) {
	forcedProvider := strings.ToLower(strings.TrimSpace(execOptions.ForcedProvider))
	if forcedProvider != "" {
		if routeDecision.ExecutorPluginID != "" {
			return nil, "", nativeInteractionsExecutionError()
		}
		if routeProvider := strings.ToLower(strings.TrimSpace(routeDecision.Provider)); routeProvider != "" && routeProvider != forcedProvider {
			return nil, "", nativeInteractionsExecutionError()
		}
		normalizedModel := strings.TrimSpace(modelName)
		if normalizedModel == "" {
			normalizedModel = strings.TrimSpace(originalRequestedModel)
		}
		if errMsg := h.validateImageOnlyModel(normalizedModel, allowImageModel); errMsg != nil {
			return nil, "", errMsg
		}
		if errMsg := h.validateSpeechOnlyModel(normalizedModel, execOptions.AllowSpeechModel); errMsg != nil {
			return nil, "", errMsg
		}
		return []string{forcedProvider}, normalizedModel, nil
	}
	if routeDecision.Provider != "" {
		normalizedModel := originalRequestedModel
		if routeDecision.Model != "" {
			normalizedModel = routeDecision.Model
		}
		if errMsg := h.validateImageOnlyModel(normalizedModel, allowImageModel); errMsg != nil {
			return nil, "", errMsg
		}
		if errMsg := h.validateSpeechOnlyModel(normalizedModel, execOptions.AllowSpeechModel); errMsg != nil {
			return nil, "", errMsg
		}
		return []string{routeDecision.Provider}, normalizedModel, nil
	}
	return h.getRequestDetailsWithOptions(modelName, allowImageModel, execOptions.AllowSpeechModel)
}

func (h *BaseAPIHandler) getRequestDetailsWithOptions(modelName string, allowImageModel, allowSpeechModel bool) (providers []string, normalizedModel string, err *interfaces.ErrorMessage) {
	resolvedModelName := modelName
	initialSuffix := thinking.ParseSuffix(modelName)
	if initialSuffix.ModelName == "auto" {
		if h != nil && h.AuthManager != nil && h.AuthManager.HomeEnabled() {
			resolvedModelName = modelName
		} else {
			resolvedBase := util.ResolveAutoModel(initialSuffix.ModelName)
			if initialSuffix.HasSuffix {
				resolvedModelName = fmt.Sprintf("%s(%s)", resolvedBase, initialSuffix.RawSuffix)
			} else {
				resolvedModelName = resolvedBase
			}
		}
	} else {
		if h != nil && h.AuthManager != nil && h.AuthManager.HomeEnabled() {
			resolvedModelName = modelName
		} else {
			resolvedModelName = util.ResolveAutoModel(modelName)
		}
	}

	parsed := thinking.ParseSuffix(resolvedModelName)
	baseModel := strings.TrimSpace(parsed.ModelName)

	if errMsg := h.validateImageOnlyModel(baseModel, allowImageModel); errMsg != nil {
		return nil, "", errMsg
	}
	if errMsg := h.validateSpeechOnlyModel(baseModel, allowSpeechModel); errMsg != nil {
		return nil, "", errMsg
	}

	if h != nil && h.AuthManager != nil && h.AuthManager.HomeEnabled() {
		return []string{"home"}, resolvedModelName, nil
	}

	providers = util.GetProviderName(baseModel)
	// Fallback: if baseModel has no provider but differs from resolvedModelName,
	// try using the full model name. This handles edge cases where custom models
	// may be registered with their full suffixed name (e.g., "my-model(8192)").
	// Evaluated in Story 11.8: This fallback is intentionally preserved to support
	// custom model registrations that include thinking suffixes.
	if len(providers) == 0 && baseModel != resolvedModelName {
		providers = util.GetProviderName(resolvedModelName)
	}

	if len(providers) == 0 {
		// The client asked for a model this proxy cannot route. Report it as a request
		// error so streaming clients receive an actionable message instead of a
		// gateway failure they would keep retrying. 400 is used rather than 404 to keep
		// it distinguishable from an unregistered HTTP route.
		// The model name is client supplied, so it is inserted through sjson rather
		// than formatted into the JSON literal: an unescaped quote would otherwise
		// corrupt the body or let the caller overwrite the error code.
		body := `{"error":{"message":"","type":"invalid_request_error","code":"model_not_found","param":"model"}}`
		body, errSet := ojson.Set(body, "error.message", "unknown provider for model "+modelName)
		if errSet != nil {
			body = `{"error":{"message":"unknown provider for model","type":"invalid_request_error","code":"model_not_found","param":"model"}}`
		}
		return nil, "", &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      errors.New(body),
		}
	}

	// The thinking suffix is preserved in the model name itself, so no
	// metadata-based configuration passing is needed.
	return providers, resolvedModelName, nil
}

func (h *BaseAPIHandler) validateImageOnlyModel(modelName string, allowImageModel bool) *interfaces.ErrorMessage {
	baseModel := strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName)
	if baseModel == "" {
		baseModel = strings.TrimSpace(modelName)
	}
	if isOpenAIImageOnlyModel(baseModel) && !allowImageModel {
		return &interfaces.ErrorMessage{
			StatusCode: http.StatusServiceUnavailable,
			Error:      fmt.Errorf("model %s is only supported on /v1/images/generations and /v1/images/edits", routeModelBaseName(baseModel)),
		}
	}
	return nil
}

func isOpenAIImageOnlyModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(routeModelBaseName(model))) {
	case "gpt-image-1.5", "gpt-image-2", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "gpt-image-2.5", "gpt-image-2.6-flare", "gpt-image-2.6-sunburst", "gpt-image-2.6", "grok-imagine-image", "grok-imagine-image-quality", "grok-imagine-image-2.0":
		return true
	default:
		return false
	}
}

// validateSpeechOnlyModel rejects speech-only models outside the speech endpoints so they are
// never sent to a chat upstream.
func (h *BaseAPIHandler) validateSpeechOnlyModel(modelName string, allowSpeechModel bool) *interfaces.ErrorMessage {
	baseModel := strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName)
	if baseModel == "" {
		baseModel = strings.TrimSpace(modelName)
	}
	if isXAISpeechOnlyModel(baseModel) && !allowSpeechModel {
		return &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("model %s is only supported on /v1/audio/speech and /v1/tts", routeModelBaseName(baseModel)),
		}
	}
	return nil
}

func isXAISpeechOnlyModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(routeModelBaseName(model))) {
	case "grok-tts", "grok-voice-tts-1.0":
		return true
	default:
		return false
	}
}

func routeModelBaseName(model string) string {
	model = strings.TrimSpace(model)
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		return strings.TrimSpace(model[idx+1:])
	}
	return model
}

// ginContextFromHandlerContext recovers the Gin context carried by HTTP
// execution contexts, which lets the model policy read userApiKey set by the
// access middleware or Home dispatch.
func ginContextFromHandlerContext(ctx context.Context) *gin.Context {
	if ctx == nil {
		return nil
	}
	if ginCtx, ok := ctx.(*gin.Context); ok && ginCtx != nil {
		return ginCtx
	}
	ginCtx, _ := ctx.Value("gin").(*gin.Context)
	return ginCtx
}

// ClientAPIKeyAllowsModel reports whether apiKey may use modelID according to
// the configured access.api-keys model policy. A key with no matching entry,
// or an entry with neither Models nor ExcludedModels, is unrestricted.
func ClientAPIKeyAllowsModel(entries []config.APIKeyEntry, apiKey, modelID string) bool {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return true
	}
	for i := range entries {
		if strings.TrimSpace(entries[i].Key) == apiKey {
			return apiKeyEntryAllowsModel(&entries[i], modelID)
		}
	}
	return true
}

// clientAPIKeyEntryValue returns the access.api-keys entry matching the
// authenticated client key, or nil when the request is not key-scoped.
func (h *BaseAPIHandler) clientAPIKeyEntryValue(ctx context.Context, ginCtx *gin.Context) *config.APIKeyEntry {
	if h == nil {
		return nil
	}
	cfg := h.CurrentConfig()
	if cfg == nil || len(cfg.APIKeys) == 0 {
		return nil
	}
	key := ""
	if ginCtx != nil {
		key = strings.TrimSpace(ginCtx.GetString("userApiKey"))
	}
	if key == "" {
		key = strings.TrimSpace(coreusage.APIKeyFromContext(ctx))
	}
	if key == "" {
		return nil
	}
	for i := range cfg.APIKeys {
		if strings.TrimSpace(cfg.APIKeys[i].Key) == key {
			return &cfg.APIKeys[i]
		}
	}
	return nil
}

// clientAPIKeyRestrictsModels reports whether the authenticated client key has
// a non-empty model allowlist or denylist and therefore needs list filtering.
func (h *BaseAPIHandler) clientAPIKeyRestrictsModels(ctx context.Context, ginCtx *gin.Context) bool {
	entry := h.clientAPIKeyEntryValue(ctx, ginCtx)
	return entry != nil && (len(entry.Models) > 0 || len(entry.ExcludedModels) > 0)
}

// clientAPIKeyAllowsModel reports whether the authenticated client key may use
// modelID. Requests without a configured client key, or keys without a model
// policy, are allowed.
func (h *BaseAPIHandler) clientAPIKeyAllowsModel(ctx context.Context, ginCtx *gin.Context, modelID string) bool {
	entry := h.clientAPIKeyEntryValue(ctx, ginCtx)
	if entry == nil {
		return true
	}
	return apiKeyEntryAllowsModel(entry, modelID)
}

// ClientAPIKeyAllowsModel reports whether the authenticated client key may use
// modelID. It is the cross-package form of clientAPIKeyAllowsModel.
func (h *BaseAPIHandler) ClientAPIKeyAllowsModel(ctx context.Context, ginCtx *gin.Context, modelID string) bool {
	return h.clientAPIKeyAllowsModel(ctx, ginCtx, modelID)
}

func apiKeyEntryAllowsModel(entry *config.APIKeyEntry, modelID string) bool {
	if entry == nil {
		return true
	}
	if len(entry.Models) == 0 && len(entry.ExcludedModels) == 0 {
		return true
	}
	candidates := clientModelIDCandidates(modelID)
	if len(candidates) == 0 {
		return false
	}
	if len(entry.Models) > 0 {
		allowed := false
		for _, candidate := range candidates {
			if modelPatternsMatch(entry.Models, candidate) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	for _, candidate := range candidates {
		if modelPatternsMatch(entry.ExcludedModels, candidate) {
			return false
		}
	}
	return true
}

// clientModelIDCandidates expands a client-visible model ID into the forms a
// configured pattern may legitimately target: the raw ID, the ID without a
// Gemini "models/" prefix, the decoded Claude ID, and the same IDs without a
// thinking-level suffix.
func clientModelIDCandidates(modelID string) []string {
	raw := strings.TrimSpace(modelID)
	if raw == "" {
		return nil
	}
	out := make([]string, 0, 6)
	seen := make(map[string]struct{}, 6)
	appendCandidate := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}

	appendCandidate(raw)
	base := raw
	if strings.HasPrefix(strings.ToLower(base), "models/") {
		base = strings.TrimSpace(base[len("models/"):])
	}
	appendCandidate(base)
	resolved := claudemodels.ResolveClaudeModelIDPrefix(base)
	appendCandidate(resolved)

	for _, candidate := range []string{base, resolved} {
		appendCandidate(thinking.ParseSuffix(candidate).ModelName)
		if colon := strings.LastIndex(candidate, ":"); colon > 0 {
			if recognizedThinkingLevelToken(candidate[colon+1:]) {
				appendCandidate(candidate[:colon])
			}
		}
	}
	return out
}

func recognizedThinkingLevelToken(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if _, ok := thinking.ParseLevelSuffix(value); ok {
		return true
	}
	if _, ok := thinking.ParseSpecialSuffix(value); ok {
		return true
	}
	_, ok := thinking.ParseNumericSuffix(value)
	return ok
}

func modelPatternsMatch(patterns []string, model string) bool {
	for _, pattern := range patterns {
		if modelPatternMatches(pattern, model) {
			return true
		}
	}
	return false
}

// modelPatternMatches performs case-insensitive wildcard matching where '*'
// matches any substring.
func modelPatternMatches(pattern, model string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	model = strings.ToLower(strings.TrimSpace(model))
	if pattern == "" || model == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	pi, mi := 0, 0
	starIndex, starMatch := -1, 0
	for mi < len(model) {
		switch {
		case pi < len(pattern) && pattern[pi] == model[mi]:
			pi++
			mi++
		case pi < len(pattern) && pattern[pi] == '*':
			starIndex = pi
			starMatch = mi
			pi++
		case starIndex != -1:
			pi = starIndex + 1
			starMatch++
			mi = starMatch
		default:
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// modelNotAllowedForAPIKeyError builds the downstream error returned when a
// client API key's model policy rejects an inference request.
func modelNotAllowedForAPIKeyError(model string) *interfaces.ErrorMessage {
	message := "model is not allowed for this API key"
	if trimmed := strings.TrimSpace(model); trimmed != "" {
		message = fmt.Sprintf("model %s is not allowed for this API key", trimmed)
	}
	body, errMarshal := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"code":    "model_not_allowed",
			"param":   "model",
		},
	})
	if errMarshal != nil {
		return &interfaces.ErrorMessage{
			StatusCode: http.StatusForbidden,
			Error:      errors.New(message),
		}
	}
	return &interfaces.ErrorMessage{
		StatusCode: http.StatusForbidden,
		Error:      errors.New(string(body)),
	}
}

func cloneBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}

func (h *BaseAPIHandler) modelRouterHost() PluginModelRouterHost {
	if h == nil {
		return nil
	}
	h.runtimeMu.RLock()
	routerHost := h.ModelRouterHost
	pluginHost := h.PluginHost
	h.runtimeMu.RUnlock()
	if !isNilPluginModelRouterHost(routerHost) {
		return routerHost
	}
	if isNilPluginInterceptorHost(pluginHost) {
		return nil
	}
	router, ok := pluginHost.(PluginModelRouterHost)
	if !ok {
		return nil
	}
	return router
}

type modelRouteDecision struct {
	ExecutorPluginID string
	Provider         string
	Model            string
}

func routeModel(ctx context.Context, host PluginModelRouterHost, req pluginapi.ModelRouteRequest, skipPluginID string) (pluginapi.ModelRouteResponse, bool) {
	if host == nil {
		return pluginapi.ModelRouteResponse{}, false
	}
	skipPluginID = strings.TrimSpace(skipPluginID)
	if skipPluginID != "" {
		if skipper, ok := host.(pluginModelRouterSkipHost); ok {
			return skipper.RouteModelExcept(ctx, req, skipPluginID)
		}
		return pluginapi.ModelRouteResponse{}, false
	}
	return host.RouteModel(ctx, req)
}

func modelRoutersEnabled(host PluginModelRouterHost, skipPluginID string) bool {
	if host == nil {
		return false
	}
	skipPluginID = strings.TrimSpace(skipPluginID)
	if skipPluginID != "" {
		if _, ok := host.(pluginModelRouterSkipHost); !ok {
			return false
		}
		if detector, ok := host.(modelRouterSkipDetector); ok {
			return detector.HasModelRoutersExcept(skipPluginID)
		}
	}
	if detector, ok := host.(modelRouterDetector); ok {
		return detector.HasModelRouters()
	}
	// No detector: treat routing as disabled (same conservative default as before any
	// ModelRouter existed). Hosts that route must implement HasModelRouters (pluginhost.Host does).
	return false
}

func (h *BaseAPIHandler) applyModelRouter(ctx context.Context, handlerType, modelName string, rawJSON []byte, stream bool, execOptions modelExecutionOptions) modelRouteDecision {
	var decision modelRouteDecision
	host := h.modelRouterHost()
	if host == nil || !modelRoutersEnabled(host, execOptions.SkipRouterPluginID) {
		return decision
	}
	meta := requestExecutionMetadata(ctx)
	addCallerScopeMetadata(meta, execOptions.CallerScope)
	meta[coreexecutor.RequestedModelMetadataKey] = modelName
	addModelExecutionSourceMetadata(meta, execOptions.InternalSource)
	resp, ok := routeModel(ctx, host, pluginapi.ModelRouteRequest{
		SourceFormat:   handlerType,
		RequestedModel: modelName,
		Stream:         stream,
		Headers:        modelExecutionHeaders(ctx, execOptions.Headers),
		Query:          modelExecutionQuery(ctx, execOptions.Query),
		Body:           cloneBytes(rawJSON),
		Metadata:       meta,
	}, execOptions.SkipRouterPluginID)
	if !ok || !resp.Handled {
		return decision
	}
	switch resp.TargetKind {
	case pluginapi.ModelRouteTargetSelf, pluginapi.ModelRouteTargetExecutor:
		decision.ExecutorPluginID = strings.TrimSpace(resp.Target)
	case pluginapi.ModelRouteTargetProvider:
		decision.Provider = strings.ToLower(strings.TrimSpace(resp.Target))
		decision.Model = strings.TrimSpace(resp.TargetModel)
	}
	return decision
}
