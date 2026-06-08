package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/codex2api/api"
	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/security"
	"github.com/codex2api/security/promptfilter"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	responsesWSFirstMessageTimeout = 30 * time.Second
	responsesWSWriteTimeout        = 30 * time.Second
)

var responsesWSUpgrader = websocket.Upgrader{
	EnableCompression: true,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

var errResponsesWSClientGone = errors.New("responses websocket client disconnected")

type responsesWSCloseError struct {
	code   int
	reason string
	err    error
	status int
}

func (e *responsesWSCloseError) Error() string {
	if e == nil {
		return ""
	}
	if e.err != nil {
		return e.err.Error()
	}
	return e.reason
}

func (e *responsesWSCloseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// ResponsesWebSocket handles OpenAI Responses API WebSocket ingress.
// The client sends response.create JSON frames and receives upstream Responses
// events as JSON text frames.
func (h *Handler) ResponsesWebSocket(c *gin.Context) {
	if !isResponsesWebSocketUpgradeRequest(c.Request) {
		api.SendErrorWithStatus(c, api.NewAPIError(
			api.ErrCodeInvalidRequest,
			"WebSocket upgrade required (Upgrade: websocket)",
			api.ErrorTypeInvalidRequest,
		), http.StatusUpgradeRequired)
		return
	}

	conn, err := responsesWSUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("Responses WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()
	conn.SetReadLimit(int64(security.MaxRequestBodySize))

	for turn := 0; ; turn++ {
		if turn == 0 {
			_ = conn.SetReadDeadline(time.Now().Add(responsesWSFirstMessageTimeout))
		} else {
			_ = conn.SetReadDeadline(time.Time{})
		}

		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
				return
			}
			if turn == 0 {
				log.Printf("Responses WebSocket first message read failed: %v", err)
			}
			return
		}
		_ = conn.SetReadDeadline(time.Time{})

		if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
			apiErr := api.NewAPIError(api.ErrCodeInvalidRequest, "unsupported websocket message type", api.ErrorTypeInvalidRequest)
			_ = writeResponsesWSError(conn, apiErr)
			closeResponsesWS(conn, websocket.CloseUnsupportedData, apiErr.Message)
			return
		}

		if err := h.forwardResponsesWebSocketTurn(c, conn, payload); err != nil {
			if errors.Is(err, errResponsesWSClientGone) {
				return
			}
			var closeErr *responsesWSCloseError
			if errors.As(err, &closeErr) {
				closeResponsesWS(conn, closeErr.code, closeErr.reason)
				return
			}
			closeResponsesWS(conn, websocket.CloseInternalServerErr, "upstream websocket proxy failed")
			return
		}
	}
}

func (h *Handler) forwardResponsesWebSocketTurn(c *gin.Context, conn *websocket.Conn, rawPayload []byte) error {
	rawBody, model, apiErr := normalizeResponsesWebSocketClientPayload(rawPayload)
	if apiErr != nil {
		_ = writeResponsesWSError(conn, apiErr)
		return newResponsesWSCloseError(websocket.ClosePolicyViolation, apiErr.Message, apiErr)
	}

	validator := api.NewValidator(rawBody)
	rules := api.ResponsesAPIValidationRulesForModel(model)
	rules["model"] = append(rules["model"], api.ModelValidator(h.supportedModelIDs(c.Request.Context())))
	if result := validator.ValidateRequest(rules); !result.Valid {
		apiErr = validator.ToAPIError()
		_ = writeResponsesWSError(conn, apiErr)
		return newResponsesWSCloseError(websocket.ClosePolicyViolation, apiErr.Message, apiErr)
	}

	if len(rawBody) > security.MaxRequestBodySize {
		apiErr = api.NewAPIError(api.ErrCodeInvalidRequest, "请求体过大", api.ErrorTypeInvalidRequest)
		_ = writeResponsesWSError(conn, apiErr)
		return newResponsesWSCloseError(websocket.CloseMessageTooBig, apiErr.Message, apiErr)
	}
	if err := security.ValidateModelName(model); err != nil {
		apiErr = api.NewAPIError(api.ErrCodeInvalidParameter, "model 参数无效", api.ErrorTypeInvalidRequest)
		_ = writeResponsesWSError(conn, apiErr)
		return newResponsesWSCloseError(websocket.ClosePolicyViolation, apiErr.Message, err)
	}
	if h.inspectPromptFilterOpenAIForWebSocket(c, conn, rawBody, "/v1/responses", model) {
		return newResponsesWSCloseError(websocket.ClosePolicyViolation, "prompt blocked", nil)
	}

	rawBody = normalizeServiceTierField(rawBody)
	if err := ValidateResponsesFunctionNames(rawBody); err != nil {
		apiErr = api.NewAPIError(api.ErrCodeInvalidParameter, err.Error(), api.ErrorTypeInvalidRequest)
		_ = writeResponsesWSError(conn, apiErr)
		return newResponsesWSCloseError(websocket.ClosePolicyViolation, apiErr.Message, err)
	}

	sessionID := ResolveSessionID(c.Request.Header, rawBody)
	apiKeyID := requestAPIKeyID(c)
	affinityKey := sessionAffinityKey(sessionID, apiKeyID)
	reasoningEffort := extractReasoningEffort(rawBody)
	serviceTier := extractServiceTier(rawBody)
	if serviceTier != "" {
		c.Set("x-service-tier", serviceTier)
	}

	codexBody, expandedInputRaw := PrepareResponsesWebSocketBody(rawBody)
	if err := validateResponsesImageGenerationSizes(codexBody); err != nil {
		apiErr = api.NewAPIError(api.ErrCodeInvalidParameter, err.Error(), api.ErrorTypeInvalidRequest)
		_ = writeResponsesWSError(conn, apiErr)
		return newResponsesWSCloseError(websocket.ClosePolicyViolation, apiErr.Message, err)
	}
	effectiveModel := effectiveRequestModel(codexBody, model)
	if status, msg := h.enforceAPIKeyLimits(c, effectiveModel); status != 0 {
		errType := api.ErrorTypeRateLimit
		errCode := api.ErrCodeRateLimitReached
		closeCode := websocket.CloseTryAgainLater
		if status == http.StatusForbidden {
			errType = api.ErrorTypePermission
			errCode = api.ErrCodeInvalidRequest
			closeCode = websocket.ClosePolicyViolation
		}
		apiErr = api.NewAPIError(errCode, msg, errType)
		_ = writeResponsesWSError(conn, apiErr)
		return newResponsesWSCloseError(closeCode, apiErr.Message, apiErr)
	}

	accountFilter := accountFilterForResponsesModel(effectiveModel, modelIDInList(effectiveModel, SupportedModelIDs(c.Request.Context(), h.db)))
	accountFilter = h.withModelCooldownFilter(effectiveModel, accountFilter)

	maxRetries := h.getMaxRetries()
	maxRateLimitRetries := h.getMaxRateLimitRetries()
	generalRetries := 0
	rateLimitRetries := 0
	var lastStatusCode int
	var lastBody []byte
	retryExclusions := newRetryAccountExclusions()
	invalidEncryptedContentRetried := false
	var lastUpstreamCancel context.CancelFunc
	var activeEnd func()
	forceHTTPFallback := false
	attemptedUpstream := false
	defer func() {
		if activeEnd != nil {
			activeEnd()
		}
		if lastUpstreamCancel != nil {
			lastUpstreamCancel()
		}
	}()

	for attempt := 0; ; attempt++ {
		if activeEnd != nil {
			activeEnd()
			activeEnd = nil
		}
		pick := h.nextRetryAccountPickForSession(c.Request.Context(), affinityKey, apiKeyID, retryExclusions, accountFilter)
		account, stickyProxyURL := pick.account, pick.proxyURL
		if account == nil {
			if attemptedUpstream && c.Request.Context().Err() != nil {
				return errResponsesWSClientGone
			}
			if pick.queueFull {
				apiErr = api.NewAPIError(api.ErrCodeRateLimitReached, "本地账号调度队列已满，请稍后重试", api.ErrorTypeRateLimit)
				_ = writeResponsesWSMessage(conn, buildResponseFailedEvent(http.StatusTooManyRequests, apiErr.Message))
				return nil
			}
			if lastStatusCode == http.StatusTooManyRequests && len(lastBody) > 0 {
				apiErr = responsesWSUpstreamAPIError(lastStatusCode, lastBody)
			} else {
				apiErr = api.NewAPIError(api.ErrCodeServiceUnavailable, noAvailableAccountMessage(effectiveModel), api.ErrorTypeServer)
			}
			_ = writeResponsesWSMessage(conn, buildResponseFailedEvent(http.StatusServiceUnavailable, apiErr.Message))
			return nil
		}

		start := time.Now()
		proxyURL := h.resolveProxyForAttempt(account, stickyProxyURL)
		h.store.BindSessionAffinity(affinityKey, account, proxyURL)
		upstreamEndpoint := "/v1/responses"
		activeEnd = h.beginActiveProxyRequest(c, account, auth.ActiveRequestMeta{
			Endpoint:         "/v1/responses",
			UpstreamEndpoint: upstreamEndpoint,
			Model:            model,
			EffectiveModel:   effectiveModel,
			Stream:           true,
			StartedAt:        start,
		})

		apiKey := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		deviceCfg := h.deviceCfg
		if deviceCfg == nil {
			deviceCfg = &DeviceProfileConfig{StabilizeDeviceProfile: false}
		}
		downstreamHeaders := c.Request.Header.Clone()

		if account.IsOpenAIResponsesAPI() {
			if lastUpstreamCancel != nil {
				lastUpstreamCancel()
			}
			upstreamCtx, upstreamCancel := newDrainableUpstreamContext(c.Request.Context(), upstreamDrainTimeout)
			lastUpstreamCancel = upstreamCancel
			baseURL, _ := account.OpenAIResponsesCredentials()
			httpEndpoint := auth.OpenAIResponsesEndpoint(baseURL, "/v1/responses")
			upstreamEndpoint = httpEndpoint
			hasPreviousResponseID := strings.TrimSpace(gjson.GetBytes(rawBody, "previous_response_id").String()) != ""
			openAIResponsesBody, openAIResponsesInputRaw := prepareOpenAIResponsesHTTPBodyFromWebSocket(rawBody, false)
			if openAIResponsesInputRaw != "" {
				expandedInputRaw = openAIResponsesInputRaw
			}
			executeOpenAIResponses := ExecuteOpenAIResponsesRequest
			if hasPreviousResponseID {
				openAIResponsesBody = PrepareOpenAIResponsesWebSocketBody(rawBody)
				executeOpenAIResponses = ExecuteOpenAIResponsesWebSocketRequest
				if wsEndpoint, err := openAIResponsesWebSocketEndpoint(httpEndpoint); err == nil {
					upstreamEndpoint = wsEndpoint
				}
			}
			if activeEnd != nil {
				activeEnd()
			}
			activeEnd = h.beginActiveProxyRequest(c, account, auth.ActiveRequestMeta{
				Endpoint:         "/v1/responses",
				UpstreamEndpoint: upstreamEndpoint,
				Model:            model,
				EffectiveModel:   effectiveModel,
				Stream:           true,
				StartedAt:        start,
			})
			if verdict := h.inspectUpstreamGuardRequest(c.Request.Context(), "/v1/responses", effectiveModel, account, openAIResponsesBody, true, c.GetHeader("X-Request-Id")); shouldBlockUpstreamGuard(verdict) {
				_ = writeResponsesWSError(conn, upstreamGuardBlockAPIError(verdict))
				h.store.Release(account)
				return newResponsesWSCloseError(websocket.ClosePolicyViolation, upstreamGuardBlockMessage(verdict), nil)
			}
			resp, reqErr := executeOpenAIResponses(upstreamCtx, account, openAIResponsesBody, proxyURL, downstreamHeaders)
			attemptedUpstream = true
			durationMs := int(time.Since(start).Milliseconds())
			if reqErr == nil && hasPreviousResponseID && resp != nil && resp.StatusCode != http.StatusOK {
				errBody, _ := readUpstreamErrorBody(resp)
				if isOpenAIResponsesWebSocketUnsupported(resp.StatusCode, errBody) {
					log.Printf("Responses WebSocket OpenAI Responses upstream WS unsupported, falling back to HTTP without previous_response_id (status %d): %s", resp.StatusCode, upstreamErrorLogBody(errBody))
					upstreamEndpoint = httpEndpoint
					openAIResponsesBody, openAIResponsesInputRaw = prepareOpenAIResponsesHTTPBodyFromWebSocket(rawBody, true)
					if openAIResponsesInputRaw != "" {
						expandedInputRaw = openAIResponsesInputRaw
					}
					if activeEnd != nil {
						activeEnd()
					}
					activeEnd = h.beginActiveProxyRequest(c, account, auth.ActiveRequestMeta{
						Endpoint:         "/v1/responses",
						UpstreamEndpoint: upstreamEndpoint,
						Model:            model,
						EffectiveModel:   effectiveModel,
						Stream:           true,
						StartedAt:        start,
					})
					resp, reqErr = ExecuteOpenAIResponsesRequest(upstreamCtx, account, openAIResponsesBody, proxyURL, downstreamHeaders)
					durationMs = int(time.Since(start).Milliseconds())
				} else {
					resp.Body = io.NopCloser(bytes.NewReader(errBody))
				}
			}

			if reqErr != nil {
				if c.Request.Context().Err() != nil {
					h.store.Release(account)
					return errResponsesWSClientGone
				}
				if IsNoAvailableAccountError(reqErr) {
					h.store.Release(account)
					h.store.UnbindSessionAffinity(affinityKey, account.ID())
					retryExclusions.MarkHard(account.ID())
					attemptedUpstream = false
					log.Printf("Responses WebSocket 选中 OpenAI Responses 账号在执行前已无可用凭据，切换下一个账号重试 (attempt %d/%d, account %d)", attempt+1, maxRetries+1, account.ID())
					continue
				}
				kind := classifyTransportFailure(reqErr)
				if kind != "" {
					h.store.ReportRequestFailure(account, kind, time.Duration(durationMs)*time.Millisecond)
				}
				h.store.Release(account)
				h.store.UnbindSessionAffinity(affinityKey, account.ID())
				retryExclusions.MarkHard(account.ID())

				if !IsRetryableError(reqErr) && kind == "" {
					apiErr = api.NewAPIError(api.ErrCodeUpstreamError, reqErr.Error(), api.ErrorTypeUpstream)
					_ = writeResponsesWSMessage(conn, buildResponseFailedEvent(http.StatusBadGateway, apiErr.Message))
					return nil
				}
				log.Printf("Responses WebSocket OpenAI Responses upstream request failed (attempt %d): %v", attempt+1, reqErr)
				if shouldRetryRequestError(reqErr, &generalRetries, maxRetries) {
					continue
				}
				apiErr = api.NewAPIError(api.ErrCodeUpstreamError, reqErr.Error(), api.ErrorTypeUpstream)
				_ = writeResponsesWSMessage(conn, buildResponseFailedEvent(http.StatusBadGateway, apiErr.Message))
				return nil
			}

			if resp.StatusCode != http.StatusOK {
				errBody, _ := readUpstreamErrorBody(resp)
				resp.Body.Close()
				if kind := classifyHTTPFailure(resp.StatusCode); kind != "" {
					h.store.ReportRequestFailure(account, kind, time.Duration(durationMs)*time.Millisecond)
				}
				SyncCodexUsageState(h.store, account, resp)
				h.store.Release(account)
				h.store.UnbindSessionAffinity(affinityKey, account.ID())
				retryExclusions.MarkHard(account.ID())

				log.Printf("Responses WebSocket OpenAI Responses upstream returned error (attempt %d, status %d): %s", attempt+1, resp.StatusCode, upstreamErrorLogBody(errBody))
				logUpstreamError("/v1/responses", resp.StatusCode, model, account.ID(), errBody)
				h.logUpstreamCyberPolicy(c, "/v1/responses", model, errBody)
				decision := h.applyCooldownForModel(account, resp.StatusCode, errBody, resp, effectiveModel)
				shouldRetry := shouldRetryHTTPStatus(resp.StatusCode, &generalRetries, &rateLimitRetries, maxRetries, maxRateLimitRetries)
				h.logUsageForRequest(c, &database.UsageLogInput{
					AccountID:         account.ID(),
					Endpoint:          "/v1/responses",
					Model:             model,
					StatusCode:        resp.StatusCode,
					DurationMs:        durationMs,
					ReasoningEffort:   reasoningEffort,
					InboundEndpoint:   "/v1/responses",
					UpstreamEndpoint:  upstreamEndpoint,
					Stream:            true,
					ServiceTier:       serviceTier,
					IsRetryAttempt:    shouldRetry,
					AttemptIndex:      attempt + 1,
					UpstreamErrorKind: upstreamErrorKind(resp.StatusCode, errBody, decision),
					ErrorMessage:      usageLogErrorMessage(resp.StatusCode, errBody),
				})

				if shouldRetry {
					lastStatusCode = resp.StatusCode
					lastBody = errBody
					continue
				}

				apiErr = responsesWSUpstreamAPIError(resp.StatusCode, errBody)
				_ = writeResponsesWSMessage(conn, buildResponseFailedEvent(resp.StatusCode, apiErr.Message))
				return nil
			}

			if err := h.streamResponsesWSUpstream(c, conn, resp, account, proxyURL, affinityKey, upstreamEndpoint, model, effectiveModel, reasoningEffort, serviceTier, expandedInputRaw, start, attempt+1); err != nil {
				if errors.Is(err, errResponsesWSClientGone) {
					return err
				}
				if shouldRetryErr, ok := err.(*responsesWSCloseError); ok && shouldRetryErr.code == websocket.CloseTryAgainLater {
					h.store.UnbindSessionAffinity(affinityKey, account.ID())
					retryExclusions.MarkHard(account.ID())
					if shouldRetryRequestError(err, &generalRetries, maxRetries) {
						continue
					}
					statusCode := shouldRetryErr.status
					if statusCode < 400 {
						statusCode = http.StatusBadGateway
					}
					if err := writeResponsesWSMessage(conn, buildResponseFailedEvent(statusCode, shouldRetryErr.reason)); err != nil {
						return errResponsesWSClientGone
					}
					return nil
				}
				if closeErr, ok := err.(*responsesWSCloseError); ok {
					statusCode := closeErr.status
					if statusCode < 400 {
						statusCode = http.StatusBadGateway
					}
					if err := writeResponsesWSMessage(conn, buildResponseFailedEvent(statusCode, closeErr.reason)); err != nil {
						return errResponsesWSClientGone
					}
					return nil
				}
				return err
			}
			return nil
		}

		upstreamSessionID := IsolateCodexSessionID(apiKeyID, sessionID)

		if lastUpstreamCancel != nil {
			lastUpstreamCancel()
		}
		upstreamCtx, upstreamCancel := newDrainableUpstreamContext(c.Request.Context(), upstreamDrainTimeout)
		lastUpstreamCancel = upstreamCancel
		if verdict := h.inspectUpstreamGuardRequest(c.Request.Context(), "/v1/responses", effectiveModel, account, codexBody, true, c.GetHeader("X-Request-Id")); shouldBlockUpstreamGuard(verdict) {
			_ = writeResponsesWSError(conn, upstreamGuardBlockAPIError(verdict))
			h.store.Release(account)
			return newResponsesWSCloseError(websocket.ClosePolicyViolation, upstreamGuardBlockMessage(verdict), nil)
		}
		resp, reqErr := ExecuteRequest(upstreamCtx, account, codexBody, upstreamSessionID, proxyURL, apiKey, deviceCfg, downstreamHeaders, !forceHTTPFallback)
		attemptedUpstream = true
		durationMs := int(time.Since(start).Milliseconds())

		if reqErr != nil {
			if c.Request.Context().Err() != nil {
				h.store.Release(account)
				return errResponsesWSClientGone
			}
			if IsNoAvailableAccountError(reqErr) {
				h.store.Release(account)
				h.store.UnbindSessionAffinity(affinityKey, account.ID())
				retryExclusions.MarkHard(account.ID())
				attemptedUpstream = false
				log.Printf("Responses WebSocket 选中账号在执行前已无可用凭据，切换下一个账号重试 (attempt %d/%d, account %d)", attempt+1, maxRetries+1, account.ID())
				continue
			}
			kind := classifyTransportFailure(reqErr)
			if kind != "" && kind != "websocket_missing_terminal" {
				h.store.ReportRequestFailure(account, kind, time.Duration(durationMs)*time.Millisecond)
			}
			h.store.Release(account)
			h.store.UnbindSessionAffinity(affinityKey, account.ID())
			if kind == "websocket_missing_terminal" && websocketFallbackHTTPEnabled() {
				forceHTTPFallback = true
			} else {
				retryExclusions.MarkHard(account.ID())
			}

			if !IsRetryableError(reqErr) && classifyTransportFailure(reqErr) == "" {
				apiErr = api.NewAPIError(api.ErrCodeUpstreamError, reqErr.Error(), api.ErrorTypeUpstream)
				_ = writeResponsesWSMessage(conn, buildResponseFailedEvent(http.StatusBadGateway, apiErr.Message))
				return nil
			}
			log.Printf("Responses WebSocket upstream request failed (attempt %d): %v", attempt+1, reqErr)
			if shouldRetryRequestError(reqErr, &generalRetries, maxRetries) {
				continue
			}
			apiErr = api.NewAPIError(api.ErrCodeUpstreamError, reqErr.Error(), api.ErrorTypeUpstream)
			_ = writeResponsesWSMessage(conn, buildResponseFailedEvent(http.StatusBadGateway, apiErr.Message))
			return nil
		}

		if resp.StatusCode != http.StatusOK {
			errBody, _ := readUpstreamErrorBody(resp)
			resp.Body.Close()

			if !invalidEncryptedContentRetried && isInvalidEncryptedContentError(resp.StatusCode, errBody) {
				strippedRawBody, rawChanged := stripInvalidEncryptedContentFromResponsesBody(rawBody)
				strippedCodexBody, codexChanged := stripInvalidEncryptedContentFromResponsesBody(codexBody)
				if rawChanged || codexChanged {
					invalidEncryptedContentRetried = true
					if rawChanged {
						rawBody = strippedRawBody
					}
					if codexChanged {
						codexBody = strippedCodexBody
						expandedInputRaw = responsesInputRaw(codexBody)
					}
					log.Printf("Responses WebSocket upstream rejected encrypted_content, stripped encrypted reasoning context and retried once (attempt %d)", attempt+1)
					h.store.Release(account)
					h.store.UnbindSessionAffinity(affinityKey, account.ID())
					continue
				}
			}

			if kind := classifyHTTPFailure(resp.StatusCode); kind != "" {
				h.store.ReportRequestFailure(account, kind, time.Duration(durationMs)*time.Millisecond)
			}
			SyncCodexUsageState(h.store, account, resp)
			h.store.Release(account)
			h.store.UnbindSessionAffinity(affinityKey, account.ID())
			retryExclusions.MarkHard(account.ID())

			log.Printf("Responses WebSocket upstream returned error (attempt %d, status %d): %s", attempt+1, resp.StatusCode, upstreamErrorLogBody(errBody))
			logUpstreamError("/v1/responses", resp.StatusCode, model, account.ID(), errBody)
			h.logUpstreamCyberPolicy(c, "/v1/responses", model, errBody)
			decision := h.applyCooldownForModel(account, resp.StatusCode, errBody, resp, effectiveModel)
			shouldRetry := shouldRetryHTTPStatus(resp.StatusCode, &generalRetries, &rateLimitRetries, maxRetries, maxRateLimitRetries)
			h.logUsageForRequest(c, &database.UsageLogInput{
				AccountID:         account.ID(),
				Endpoint:          "/v1/responses",
				Model:             model,
				StatusCode:        resp.StatusCode,
				DurationMs:        durationMs,
				ReasoningEffort:   reasoningEffort,
				InboundEndpoint:   "/v1/responses",
				UpstreamEndpoint:  "/v1/responses",
				Stream:            true,
				ServiceTier:       serviceTier,
				IsRetryAttempt:    shouldRetry,
				AttemptIndex:      attempt + 1,
				UpstreamErrorKind: upstreamErrorKind(resp.StatusCode, errBody, decision),
				ErrorMessage:      usageLogErrorMessage(resp.StatusCode, errBody),
			})

			if shouldRetry {
				lastStatusCode = resp.StatusCode
				lastBody = errBody
				continue
			}

			apiErr = responsesWSUpstreamAPIError(resp.StatusCode, errBody)
			_ = writeResponsesWSMessage(conn, buildResponseFailedEvent(resp.StatusCode, apiErr.Message))
			return nil
		}

		if err := h.streamResponsesWSUpstream(c, conn, resp, account, proxyURL, affinityKey, upstreamEndpoint, model, effectiveModel, reasoningEffort, serviceTier, expandedInputRaw, start, attempt+1); err != nil {
			if errors.Is(err, errResponsesWSClientGone) {
				return err
			}
			if shouldRetryErr, ok := err.(*responsesWSCloseError); ok && shouldRetryErr.code == websocket.CloseTryAgainLater {
				h.store.UnbindSessionAffinity(affinityKey, account.ID())
				if classifyTransportFailure(err) == "websocket_missing_terminal" && websocketFallbackHTTPEnabled() {
					forceHTTPFallback = true
				} else {
					retryExclusions.MarkHard(account.ID())
				}
				if shouldRetryRequestError(err, &generalRetries, maxRetries) {
					continue
				}
				statusCode := shouldRetryErr.status
				if statusCode < 400 {
					statusCode = http.StatusBadGateway
				}
				if err := writeResponsesWSMessage(conn, buildResponseFailedEvent(statusCode, shouldRetryErr.reason)); err != nil {
					return errResponsesWSClientGone
				}
				return nil
			}
			if closeErr, ok := err.(*responsesWSCloseError); ok {
				statusCode := closeErr.status
				if statusCode < 400 {
					statusCode = http.StatusBadGateway
				}
				if err := writeResponsesWSMessage(conn, buildResponseFailedEvent(statusCode, closeErr.reason)); err != nil {
					return errResponsesWSClientGone
				}
				return nil
			}
			return err
		}
		return nil
	}
}

func (h *Handler) streamResponsesWSUpstream(
	c *gin.Context,
	conn *websocket.Conn,
	resp *http.Response,
	account *auth.Account,
	proxyURL string,
	affinityKey string,
	upstreamEndpoint string,
	model string,
	effectiveModel string,
	reasoningEffort string,
	serviceTier string,
	expandedInputRaw string,
	start time.Time,
	attemptIndex int,
) error {
	account.Mu().RLock()
	c.Set("x-account-email", account.Email)
	account.Mu().RUnlock()
	c.Set("x-account-proxy", proxyURL)
	c.Set("x-model", model)
	c.Set("x-reasoning-effort", reasoningEffort)

	var firstTokenMs int
	var usage *UsageInfo
	var actualServiceTier string
	ttftRecorded := false
	gotTerminal := false
	deltaCharCount := 0
	var readErr error
	var writeErr error
	clientGone := false
	var imageLogInfo imageUsageLogInfo
	var terminalFailurePayload []byte
	var pendingFirstTokenEvents [][]byte
	wroteAnyMessage := false
	withheldRetryableFailure := false

	readErr = ReadSSEStream(resp.Body, func(data []byte) bool {
		if verdict := h.inspectUpstreamGuardResponse(c.Request.Context(), "/v1/responses", effectiveModel, account, data, true, c.GetHeader("X-Request-Id")); shouldBlockUpstreamGuard(verdict) {
			blockEvent := buildUpstreamGuardBlockedEvent(verdict)
			terminalFailurePayload = blockEvent
			gotTerminal = true
			if !clientGone {
				if err := writeResponsesWSMessage(conn, blockEvent); err != nil {
					writeErr = err
					clientGone = true
				} else {
					wroteAnyMessage = true
				}
			}
			return false
		}
		parsed := gjson.ParseBytes(data)
		eventType := parsed.Get("type").String()
		isFirstToken := isFirstTokenEvent(eventType)
		if !ttftRecorded && isFirstToken {
			firstTokenMs = int(time.Since(start).Milliseconds())
			ttftRecorded = true
		}
		if eventType == "response.output_text.delta" {
			deltaCharCount += len(parsed.Get("delta").String())
		}
		if image, ok := extractImageFromOutputItemDone(data, model); ok {
			imageLogInfo = mergeImageUsageLogInfo(imageLogInfo, imageUsageLogInfoFromImage(image))
		}
		if eventType == "response.completed" {
			usage = extractUsageFromResult(parsed.Get("response.usage"))
			if tier := parsed.Get("response.service_tier").String(); tier != "" {
				actualServiceTier = tier
			}
			cacheCompletedResponse([]byte(expandedInputRaw), data)
			gotTerminal = true
		}
		if eventType == "response.failed" {
			terminalFailurePayload = append([]byte(nil), data...)
			gotTerminal = true
			if !wroteAnyMessage && shouldRetryResponseFailedBeforeFirstMessage(data) {
				withheldRetryableFailure = true
				return false
			}
		}
		if !clientGone {
			shouldDefer := !ttftRecorded && !gotTerminal && !isFirstToken
			currentQueued := false
			if shouldDefer {
				pendingFirstTokenEvents = append(pendingFirstTokenEvents, append([]byte(nil), data...))
				currentQueued = true
				if totalPendingWebSocketEventBytes(pendingFirstTokenEvents) <= 1024*1024 {
					return eventType != "response.completed" && eventType != "response.failed"
				}
			}
			for _, pending := range pendingFirstTokenEvents {
				if err := writeResponsesWSMessage(conn, pending); err != nil {
					writeErr = err
					clientGone = true
					break
				}
				wroteAnyMessage = true
			}
			pendingFirstTokenEvents = nil
			if !clientGone && !currentQueued {
				if err := writeResponsesWSMessage(conn, data); err != nil {
					writeErr = err
					clientGone = true
				} else {
					wroteAnyMessage = true
				}
			}
		}
		return eventType != "response.completed" && eventType != "response.failed"
	})

	totalDuration := int(time.Since(start).Milliseconds())
	outcome := classifyStreamOutcome(c.Request.Context().Err(), readErr, writeErr, gotTerminal)
	if len(terminalFailurePayload) > 0 {
		outcome = classifyResponseFailedOutcome(terminalFailurePayload)
	}
	retryBeforeDownstream := outcome.logStatusCode != http.StatusOK &&
		!wroteAnyMessage &&
		(len(terminalFailurePayload) == 0 || withheldRetryableFailure)
	if retryBeforeDownstream && len(terminalFailurePayload) > 0 {
		decision := h.applyCooldownForModel(account, outcome.logStatusCode, terminalFailurePayload, resp, effectiveModel)
		if kind := upstreamErrorKind(outcome.logStatusCode, terminalFailurePayload, decision); kind != "" {
			outcome.failureKind = kind
		}
	}
	if outcome.logStatusCode != http.StatusOK {
		log.Printf("Responses WebSocket stream ended abnormally (account %d, status %d): %s, relayed about %d chars", account.ID(), outcome.logStatusCode, outcome.failureMessage, deltaCharCount)
		if deltaCharCount > 0 && usage == nil {
			estOutputTokens := deltaCharCount / 3
			if estOutputTokens < 1 {
				estOutputTokens = 1
			}
			usage = &UsageInfo{
				OutputTokens:     estOutputTokens,
				CompletionTokens: estOutputTokens,
				TotalTokens:      estOutputTokens,
			}
		}
	}

	resolvedServiceTier := resolveServiceTier(actualServiceTier, serviceTier)
	billingServiceTier := resolveBillingServiceTier(actualServiceTier, serviceTier)
	c.Set("x-service-tier", resolvedServiceTier)
	logInput := &database.UsageLogInput{
		AccountID:          account.ID(),
		Endpoint:           "/v1/responses",
		Model:              model,
		StatusCode:         outcome.logStatusCode,
		DurationMs:         totalDuration,
		FirstTokenMs:       firstTokenMs,
		ReasoningEffort:    reasoningEffort,
		InboundEndpoint:    "/v1/responses",
		UpstreamEndpoint:   upstreamEndpoint,
		Stream:             true,
		ServiceTier:        resolvedServiceTier,
		BillingServiceTier: billingServiceTier,
	}
	if outcome.logStatusCode != http.StatusOK {
		logInput.ErrorMessage = usageLogErrorMessage(outcome.logStatusCode, []byte(outcome.failureMessage))
		logInput.UpstreamErrorKind = outcome.failureKind
		if retryBeforeDownstream {
			logInput.IsRetryAttempt = true
			logInput.AttemptIndex = attemptIndex
		}
	}
	if usage != nil {
		logInput.PromptTokens = usage.PromptTokens
		logInput.CompletionTokens = usage.CompletionTokens
		logInput.TotalTokens = usage.TotalTokens
		logInput.InputTokens = usage.InputTokens
		logInput.OutputTokens = usage.OutputTokens
		logInput.ReasoningTokens = usage.ReasoningTokens
		logInput.CachedTokens = usage.CachedTokens
	}
	applyImageUsageLogInfo(logInput, imageLogInfo)
	h.logUsageForRequest(c, logInput)

	resp.Body.Close()
	SyncCodexUsageState(h.store, account, resp)
	if outcome.penalize {
		recyclePooledClient(account, proxyURL)
		h.store.ReportRequestFailure(account, outcome.failureKind, time.Duration(totalDuration)*time.Millisecond)
		h.store.UnbindSessionAffinity(affinityKey, account.ID())
	} else if outcome.logStatusCode == http.StatusOK {
		h.store.ClearModelCooldown(account, effectiveModel)
		h.store.ReportRequestSuccessTTFT(account, time.Duration(firstTokenMs)*time.Millisecond, time.Duration(totalDuration)*time.Millisecond)
	}
	h.store.Release(account)

	if writeErr != nil {
		return errResponsesWSClientGone
	}
	if retryBeforeDownstream {
		apiErr := api.NewAPIError(api.ErrCodeUpstreamError, outcome.failureMessage, api.ErrorTypeUpstream)
		return newResponsesWSCloseErrorWithStatus(websocket.CloseTryAgainLater, apiErr.Message, errors.New(outcome.failureMessage), outcome.logStatusCode)
	}
	if outcome.logStatusCode != http.StatusOK {
		_ = writeResponsesWSMessage(conn, buildResponseFailedEvent(outcome.logStatusCode, outcome.failureMessage))
		return nil
	}
	return nil
}

func totalPendingWebSocketEventBytes(events [][]byte) int {
	total := 0
	for _, event := range events {
		total += len(event)
	}
	return total
}

func normalizeResponsesWebSocketClientPayload(raw []byte) ([]byte, string, *api.APIError) {
	trimmed := []byte(strings.TrimSpace(string(raw)))
	if len(trimmed) == 0 {
		return nil, "", api.NewAPIError(api.ErrCodeInvalidRequest, "empty websocket request payload", api.ErrorTypeInvalidRequest)
	}
	if len(trimmed) > security.MaxRequestBodySize {
		return nil, "", api.NewAPIError(api.ErrCodeInvalidRequest, "请求体过大", api.ErrorTypeInvalidRequest)
	}
	if !gjson.ValidBytes(trimmed) {
		return nil, "", api.NewAPIError(api.ErrCodeInvalidRequest, "invalid websocket request payload", api.ErrorTypeInvalidRequest)
	}

	eventType := strings.TrimSpace(gjson.GetBytes(trimmed, "type").String())
	normalized := trimmed
	switch eventType {
	case "":
		eventType = "response.create"
		var err error
		normalized, err = sjson.SetBytes(normalized, "type", eventType)
		if err != nil {
			return nil, "", api.NewAPIError(api.ErrCodeInvalidRequest, "invalid websocket request payload", api.ErrorTypeInvalidRequest)
		}
	case "response.create":
	case "response.append":
		return nil, "", api.NewAPIError(api.ErrCodeInvalidRequest, "response.append is not supported; use response.create with previous_response_id", api.ErrorTypeInvalidRequest)
	default:
		return nil, "", api.NewAPIError(api.ErrCodeInvalidRequest, fmt.Sprintf("unsupported websocket request type: %s", eventType), api.ErrorTypeInvalidRequest)
	}

	model := strings.TrimSpace(gjson.GetBytes(normalized, "model").String())
	if model == "" {
		return nil, "", api.NewAPIError(api.ErrCodeMissingField, "model is required in response.create payload", api.ErrorTypeInvalidRequest)
	}
	previousResponseID := strings.TrimSpace(gjson.GetBytes(normalized, "previous_response_id").String())
	if strings.HasPrefix(previousResponseID, "msg_") {
		return nil, "", api.NewAPIError(api.ErrCodeInvalidParameter, "previous_response_id must be a response.id (resp_*), not a message id", api.ErrorTypeInvalidRequest)
	}

	return normalized, model, nil
}

func stripResponsesWebSocketEnvelope(raw []byte) []byte {
	if len(raw) == 0 || !gjson.ValidBytes(raw) {
		return raw
	}
	if !gjson.GetBytes(raw, "type").Exists() {
		return raw
	}
	stripped, err := sjson.DeleteBytes(raw, "type")
	if err != nil {
		return raw
	}
	return stripped
}

func prepareOpenAIResponsesHTTPBodyFromWebSocket(raw []byte, dropPreviousResponseID bool) ([]byte, string) {
	body := stripResponsesWebSocketEnvelope(raw)
	if dropPreviousResponseID {
		body, _ = expandPreviousResponse(body)
		body, _ = sjson.DeleteBytes(body, "previous_response_id")
	}
	body = PrepareOpenAIResponsesBody(body)
	return body, responsesInputRaw(body)
}

func isOpenAIResponsesWebSocketUnsupported(status int, body []byte) bool {
	if status == http.StatusUpgradeRequired || status == http.StatusNotFound {
		return true
	}
	return status == http.StatusBadRequest &&
		strings.Contains(strings.ToLower(string(body)), "websocket upgrade required")
}

func (h *Handler) inspectPromptFilterOpenAIForWebSocket(c *gin.Context, conn *websocket.Conn, rawBody []byte, endpoint string, model string) bool {
	if h == nil || h.store == nil {
		return false
	}
	cfg := h.store.GetPromptFilterConfig()
	verdict := promptfilter.Inspect(rawBody, endpoint, cfg)
	h.logPromptFilterVerdict(c, endpoint, model, "local_filter", "", verdict)
	if verdict.Action != promptfilter.ActionBlock {
		return false
	}
	_ = writeResponsesWSError(conn, api.NewAPIError(
		api.ErrorCode("prompt_blocked"),
		"Request contains content blocked by prompt filter",
		api.ErrorTypeInvalidRequest,
	))
	return true
}

func isResponsesWebSocketUpgradeRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return false
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(r.Header.Get("Connection"))), "upgrade")
}

func writeResponsesWSError(conn *websocket.Conn, apiErr *api.APIError) error {
	if apiErr == nil {
		apiErr = api.NewAPIError(api.ErrCodeServerError, "Internal server error", api.ErrorTypeServer)
	}
	payload, err := json.Marshal(struct {
		Type  string        `json:"type"`
		Error *api.APIError `json:"error"`
	}{
		Type:  "error",
		Error: apiErr,
	})
	if err != nil {
		return err
	}
	return writeResponsesWSMessage(conn, payload)
}

func writeResponsesWSMessage(conn *websocket.Conn, payload []byte) error {
	if conn == nil {
		return errResponsesWSClientGone
	}
	_ = conn.SetWriteDeadline(time.Now().Add(responsesWSWriteTimeout))
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func closeResponsesWS(conn *websocket.Conn, code int, reason string) {
	if conn == nil {
		return
	}
	reason = truncateWebSocketCloseReason(reason)
	msg := websocket.FormatCloseMessage(code, reason)
	_ = conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(responsesWSWriteTimeout))
}

func truncateWebSocketCloseReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) <= 120 {
		return reason
	}
	return reason[:120]
}

func newResponsesWSCloseError(code int, reason string, err error) error {
	return newResponsesWSCloseErrorWithStatus(code, reason, err, 0)
}

func newResponsesWSCloseErrorWithStatus(code int, reason string, err error, status int) error {
	return &responsesWSCloseError{
		code:   code,
		reason: truncateWebSocketCloseReason(reason),
		err:    err,
		status: status,
	}
}

func responsesWSUpstreamAPIError(statusCode int, body []byte) *api.APIError {
	message := usageLogErrorMessage(statusCode, body)
	if strings.TrimSpace(message) == "" {
		message = fmt.Sprintf("upstream returned HTTP %d", statusCode)
	}
	errCode := api.ErrCodeUpstreamError
	errType := api.ErrorTypeUpstream
	switch statusCode {
	case http.StatusTooManyRequests:
		errCode = api.ErrCodeRateLimitReached
		errType = api.ErrorTypeRateLimit
	case http.StatusUnauthorized, http.StatusForbidden:
		errCode = api.ErrCodeInvalidAuth
		errType = api.ErrorTypeAuthentication
	case http.StatusBadRequest:
		errCode = api.ErrCodeInvalidRequest
		errType = api.ErrorTypeInvalidRequest
	}
	return api.NewAPIError(errCode, message, errType)
}
