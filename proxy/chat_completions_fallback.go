package proxy

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// chatRequestHasImage 判断下游 chat/completions 请求是否包含图片内容块。
// 只识别 OpenAI Chat 标准的 image_url；Cuckoo 的附件就是这样发送的。
func chatRequestHasImage(rawBody []byte) bool {
	messages := gjson.GetBytes(rawBody, "messages")
	if !messages.IsArray() {
		return false
	}
	hasImage := false
	messages.ForEach(func(_, message gjson.Result) bool {
		content := message.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() == "image_url" {
				hasImage = true
				return false
			}
			return true
		})
		return !hasImage
	})
	return hasImage
}

// isImageContentRejection 判断上游 400 是否因为图片内容块不被识别。
// 只在图片请求上使用，避免把普通 400 误判成协议回退。
func isImageContentRejection(statusCode int, errBody []byte) bool {
	if statusCode != http.StatusBadRequest {
		return false
	}
	message := strings.ToLower(string(errBody))
	return strings.Contains(message, "unknown") ||
		strings.Contains(message, "image") ||
		strings.Contains(message, "vision") ||
		strings.Contains(message, "multimodal")
}

// tryChatCompletionsFallback 把原始 chat 请求体直接转发到同一账号的
// /v1/chat/completions，并把上游 chat JSON/SSE 原样回传给下游。
//
// 背景：Agnes 的 /v1/responses 只支持文本；图片必须走 chat completions。
// 该回退只在 Responses 明确因图片内容块返回 400 时触发，不影响正常请求。
func (h *Handler) tryChatCompletionsFallback(
	c *gin.Context,
	ctx context.Context,
	rawBody []byte,
	model string,
	mappedModel string,
	account *auth.Account,
	proxyURL string,
	isStream bool,
	reasoningEffort string,
	serviceTier string,
	attempt int,
	start time.Time,
) bool {
	if account == nil || !account.IsOpenAIResponsesAPI() {
		return false
	}
	baseURL, apiKey := account.OpenAIResponsesCredentials()
	if baseURL == "" || apiKey == "" {
		return false
	}

	payload := rawBody
	if mappedModel != "" && mappedModel != model {
		payload = replaceModelInJSONBody(payload, mappedModel)
	}
	endpoint := auth.OpenAIResponsesEndpoint(baseURL, "/v1/chat/completions")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	if isStream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json, text/event-stream")
	}

	resp, err := getPooledClient(account, proxyURL).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxUpstreamErrorBodyBytes))
		return false
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	usage, err := h.relayChatFallback(c, resp.Body, contentType, account, model, proxyURL, reasoningEffort, isStream)
	if err != nil {
		return false
	}

	h.logUsageForRequest(c, withUsage(&database.UsageLogInput{
		AccountID:        account.ID(),
		Endpoint:         "/v1/chat/completions",
		Model:            model,
		StatusCode:       http.StatusOK,
		DurationMs:       int(time.Since(start).Milliseconds()),
		ReasoningEffort:  reasoningEffort,
		InboundEndpoint:  "/v1/chat/completions",
		UpstreamEndpoint: "/v1/chat/completions",
		Stream:           isStream,
		ServiceTier:      serviceTier,
		IsRetryAttempt:   true,
		AttemptIndex:     attempt + 2,
	}, usage))
	return true
}

func (h *Handler) relayChatFallback(
	c *gin.Context,
	body io.Reader,
	contentType string,
	account *auth.Account,
	model string,
	proxyURL string,
	reasoningEffort string,
	isStream bool,
) (*UsageInfo, error) {
	if !isStream {
		data, err := io.ReadAll(body)
		if err != nil {
			return nil, err
		}
		h.setFallbackResponseContext(c, account, model, proxyURL, reasoningEffort)
		c.Data(http.StatusOK, contentType, data)
		return usageFromChatResponse(data), nil
	}

	h.setFallbackResponseContext(c, account, model, proxyURL, reasoningEffort)
	c.Header("Content-Type", contentType)
	c.Status(http.StatusOK)
	return h.copyChatStream(c, body), nil
}

func (h *Handler) setFallbackResponseContext(c *gin.Context, account *auth.Account, model, proxyURL, reasoningEffort string) {
	account.Mu().RLock()
	email := account.Email
	account.Mu().RUnlock()
	c.Set("x-account-email", email)
	c.Set("x-account-proxy", proxyURL)
	c.Set("x-model", model)
	c.Set("x-reasoning-effort", reasoningEffort)
}

func withUsage(input *database.UsageLogInput, usage *UsageInfo) *database.UsageLogInput {
	if input == nil || usage == nil {
		return input
	}
	input.PromptTokens = usage.PromptTokens
	input.CompletionTokens = usage.CompletionTokens
	input.TotalTokens = usage.TotalTokens
	input.InputTokens = usage.InputTokens
	input.OutputTokens = usage.OutputTokens
	input.ReasoningTokens = usage.ReasoningTokens
	input.CachedTokens = usage.CachedTokens
	return input
}

func usageFromChatResponse(body []byte) *UsageInfo {
	usage := gjson.GetBytes(body, "usage")
	if !usage.Exists() {
		return nil
	}
	prompt := int(usage.Get("prompt_tokens").Int())
	completion := int(usage.Get("completion_tokens").Int())
	if prompt == 0 {
		prompt = int(usage.Get("input_tokens").Int())
	}
	if completion == 0 {
		completion = int(usage.Get("output_tokens").Int())
	}
	if prompt == 0 && completion == 0 {
		return nil
	}
	return newUsageInfo(prompt, completion, int(usage.Get("reasoning_tokens").Int()), int(usage.Get("cached_tokens").Int()))
}

// copyChatStream 原样透传 chat SSE，同时尽量从最后一个 usage 事件里取统计。
func (h *Handler) copyChatStream(c *gin.Context, body io.Reader) *UsageInfo {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil
	}
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	writer := newStreamFlushWriter(c.Writer, flusher)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	var usage *UsageInfo
	for scanner.Scan() {
		line := scanner.Bytes()
		if err := writer.WriteBytes(append(line, '\n')); err != nil {
			return usage
		}
		if parsed := usageFromChatSSELine(line); parsed != nil {
			usage = parsed
		}
	}
	_ = writer.Flush()
	return usage
}

func usageFromChatSSELine(line []byte) *UsageInfo {
	text := strings.TrimSpace(string(line))
	if !strings.HasPrefix(text, "data:") {
		return nil
	}
	text = strings.TrimSpace(strings.TrimPrefix(text, "data:"))
	if text == "" || text == "[DONE]" || text[0] != '{' {
		return nil
	}
	return usageFromChatResponse([]byte(text))
}
