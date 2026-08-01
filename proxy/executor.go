package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/codex2api/auth"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ==================== HTTP 连接池（按账号隔离 + TTL 淘汰） ====================
//
// 设计要点：
//   - 按账号隔离：避免同一 TCP 连接被不同 token 复用（会被服务端检测）
//   - TTL 淘汰：只有活跃账号持有连接，不活跃的自动清理，几万账号也不爆内存
//   - 突发复用：每账号保留有限空闲连接，避免并发回落后重复握手

// poolEntry 包装 http.Client，追踪最后使用时间用于 TTL 淘汰
type poolEntry struct {
	client   *http.Client
	lastUsed atomic.Int64 // UnixNano 时间戳
}

func (e *poolEntry) touch() {
	e.lastUsed.Store(time.Now().UnixNano())
}

var clientPool sync.Map // map[string]*poolEntry, key = accountID|proxyURL|transportMode

// clientPoolTTL 未使用超过此时间的 Client 将被淘汰
const clientPoolTTL = 5 * time.Minute

// clientPoolCleanupInterval 清理协程执行间隔
const clientPoolCleanupInterval = 60 * time.Second

func init() {
	// 后台清理：每 60 秒扫描一次，淘汰过期的 Client
	go func() {
		ticker := time.NewTicker(clientPoolCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			evictExpiredClients()
		}
	}()
}

func evictExpiredClients() {
	cutoff := time.Now().Add(-clientPoolTTL).UnixNano()
	clientPool.Range(func(key, value any) bool {
		entry := value.(*poolEntry)
		if entry.lastUsed.Load() < cutoff {
			clientPool.Delete(key)
			entry.client.CloseIdleConnections()
		}
		return true
	})
}

const (
	codexTransportModeStandard   = "standard"
	codexTransportModeUTLSChrome = "utls_chrome"
	openAIResponsesWebSocketBeta = "responses_websockets=2026-02-06"
)

func codexTransportModeFromEnv() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODEX_TRANSPORT_MODE"))) {
	case "", "standard", "go", "default":
		return codexTransportModeStandard
	case "utls", "utls_chrome", "chrome":
		return codexTransportModeUTLSChrome
	default:
		return codexTransportModeStandard
	}
}

func clientPoolKey(account *auth.Account, proxyURL, transportMode string) string {
	return fmt.Sprintf("%d|%s|%s", account.ID(), strings.TrimSpace(proxyURL), transportMode)
}

func shouldRecyclePooledClient(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection is shutting down") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "stream error: stream id") ||
		strings.Contains(msg, "internal_error") ||
		strings.Contains(msg, "http2:")
}

func recyclePooledClient(account *auth.Account, proxyURL string) {
	key := clientPoolKey(account, proxyURL, codexTransportModeFromEnv())
	if v, ok := clientPool.LoadAndDelete(key); ok {
		v.(*poolEntry).client.CloseIdleConnections()
	}
}

func recyclePooledClientForAccount(account *auth.Account) {
	if account == nil {
		return
	}

	account.Mu().RLock()
	proxyURL := account.ProxyURL
	account.Mu().RUnlock()
	recyclePooledClient(account, proxyURL)
}

func newCodexStandardTransport(proxyURL string) http.RoundTripper {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxIdleConnsPerHost = 16
	transport.IdleConnTimeout = 90 * time.Second
	transport.ForceAttemptHTTP2 = true
	baseDialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = baseDialer.DialContext
	if err := auth.ConfigureTransportProxy(transport, proxyURL, baseDialer); err != nil {
		log.Printf("[CodexTransport] 代理配置失败，回退直连: proxy=%s err=%v", proxyURL, err)
		transport.Proxy = nil
		transport.DialContext = baseDialer.DialContext
	}
	return transport
}

func newCodexTransport(proxyURL string) http.RoundTripper {
	switch codexTransportModeFromEnv() {
	case codexTransportModeUTLSChrome:
		return NewUTLSTransport(proxyURL)
	default:
		return newCodexStandardTransport(proxyURL)
	}
}

func codexFingerprintDebugEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODEX_FINGERPRINT_DEBUG"))) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func shortHashForLog(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:6])
}

func logCodexFingerprintDebug(kind string, account *auth.Account, proxyURL string, headers http.Header) {
	if !codexFingerprintDebugEnabled() {
		return
	}
	accountID := int64(0)
	if account != nil {
		accountID = account.ID()
	}
	userAgent := strings.TrimSpace(headers.Get("User-Agent"))
	originator := strings.TrimSpace(headers.Get("Originator"))
	log.Printf("[CodexFingerprint] kind=%s account_id=%d transport_mode=%s proxy_enabled=%t official_client=%t ua_hash=%s originator=%s session_hash=%s stainless_present=%t",
		kind,
		accountID,
		codexTransportModeFromEnv(),
		strings.TrimSpace(proxyURL) != "",
		IsCodexOfficialClientByHeaders(userAgent, originator),
		shortHashForLog(userAgent),
		originator,
		shortHashForLog(headers.Get("Session_id")),
		headers.Get("X-Stainless-Package-Version") != "" ||
			headers.Get("X-Stainless-Runtime-Version") != "" ||
			headers.Get("X-Stainless-Os") != "" ||
			headers.Get("X-Stainless-Arch") != "",
	)
}

// getPooledClient 获取或创建连接池中的 HTTP Client（按账号隔离，TTL 自动淘汰）
func getPooledClient(account *auth.Account, proxyURL string) *http.Client {
	transportMode := codexTransportModeFromEnv()
	key := clientPoolKey(account, proxyURL, transportMode)
	if v, ok := clientPool.Load(key); ok {
		entry := v.(*poolEntry)
		entry.touch()
		return entry.client
	}

	transport := newCodexTransport(proxyURL)

	entry := &poolEntry{
		client: &http.Client{
			Transport:     transport,
			Timeout:       10 * time.Minute,
			CheckRedirect: noFollowUpstreamRedirect,
		},
	}
	entry.touch()

	if v, loaded := clientPool.LoadOrStore(key, entry); loaded {
		e := v.(*poolEntry)
		e.touch()
		return e.client
	}
	return entry.client
}

func noFollowUpstreamRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

// Codex 上游常量
const (
	CodexBaseURL = "https://chatgpt.com/backend-api/codex"
	Originator   = "codex_cli_rs"
)

var codexAllowedForwardHeaders = []string{
	"X-Codex-Turn-State",
	"X-Codex-Turn-Metadata",
	"X-Client-Request-Id",
	"X-Codex-Beta-Features",
}

// WebsocketExecuteFunc WebSocket 执行函数（由 wsrelay 包在 main.go 中注册，避免循环依赖）
var WebsocketExecuteFunc func(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header) (*http.Response, error)

func IsolateCodexSessionID(apiKeyID int64, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || apiKeyID <= 0 {
		return raw
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("api-key:%d:%s", apiKeyID, raw)))
	return hex.EncodeToString(sum[:8])
}

var ExecuteRequest = executeRequest

// executeRequest 向 Codex 上游发送请求
// sessionID 可选，用于 prompt cache 会话绑定
// useWebsocket 可选，如果为 true 则使用 WebSocket 连接
// headers 下游请求头，用于设备指纹学习
func executeRequest(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header, useWebsocket ...bool) (*http.Response, error) {
	// 检查是否使用 WebSocket
	if len(useWebsocket) > 0 && useWebsocket[0] && WebsocketExecuteFunc != nil {
		return WebsocketExecuteFunc(ctx, account, requestBody, sessionID, proxyOverride, apiKey, deviceCfg, headers)
	}

	if ctx == nil {
		ctx = context.Background()
	}

	account.Mu().RLock()
	accessToken := account.AccessToken
	proxyURL := account.ProxyURL
	account.Mu().RUnlock()

	// 代理池优先级: proxyOverride (来自 NextProxy) > account.ProxyURL
	if proxyOverride != "" {
		proxyURL = proxyOverride
	}

	if accessToken == "" {
		return nil, ErrNoAvailableAccount()
	}

	// ==================== Codex 请求体优化 ====================
	// 参考 CLIProxyAPI/codex_executor.go + sub2api 的实现

	// 1. 确保 instructions 字段存在（Codex 后端要求）
	if !gjson.GetBytes(requestBody, "instructions").Exists() {
		requestBody, _ = sjson.SetBytes(requestBody, "instructions", "")
	}

	// 2. 清理可能导致上游报错的多余字段
	requestBody, _ = sjson.DeleteBytes(requestBody, "prompt_cache_retention")
	requestBody, _ = sjson.DeleteBytes(requestBody, "safety_identifier")
	requestBody, _ = sjson.DeleteBytes(requestBody, "disable_response_storage")

	// 3. 注入 prompt_cache_key（如果请求体中没有，且 sessionID 不为空）
	existingCacheKey := strings.TrimSpace(gjson.GetBytes(requestBody, "prompt_cache_key").String())
	cacheKey := existingCacheKey
	if sessionID != "" {
		cacheKey = sessionID
		requestBody, _ = sjson.SetBytes(requestBody, "prompt_cache_key", cacheKey)
	}

	endpoint := CodexBaseURL + "/responses"

	// Resin 反向代理模式：改写 URL，使用标准 HTTP 客户端
	var client *http.Client
	if IsResinEnabled() {
		endpoint = BuildReverseProxyURL(endpoint)
		client = getResinHTTPClient(account)
	} else {
		client = getPooledClient(account, proxyURL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return nil, ErrInternalError("创建请求失败", err)
	}

	// ==================== 请求头（伪装 Codex CLI） ====================
	applyCodexRequestHeaders(req, account, accessToken, cacheKey, apiKey, deviceCfg, headers)

	// Resin 反代：注入账号身份头
	if IsResinEnabled() {
		req.Header.Set("X-Resin-Account", ResinAccountID(account))
	}
	logCodexFingerprintDebug("http", account, proxyURL, req.Header)

	resp, err := client.Do(req)
	if err != nil {
		if shouldRecyclePooledClient(err) {
			recyclePooledClient(account, proxyURL)
		}
		return nil, ErrUpstream(0, "请求上游失败", err)
	}

	return resp, nil
}

func ExecuteOpenAIResponsesRequest(ctx context.Context, account *auth.Account, requestBody []byte, proxyOverride string, headers http.Header) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	baseURL, apiKey := account.OpenAIResponsesCredentials()
	account.Mu().RLock()
	proxyURL := account.ProxyURL
	account.Mu().RUnlock()
	if proxyOverride != "" {
		proxyURL = proxyOverride
	}
	if baseURL == "" || apiKey == "" {
		return nil, ErrNoAvailableAccount()
	}

	endpoint := auth.OpenAIResponsesEndpoint(baseURL, "/v1/responses")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return nil, ErrInternalError("创建请求失败", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if headers != nil {
		for _, key := range []string{"OpenAI-Organization", "OpenAI-Project", "Idempotency-Key"} {
			if value := strings.TrimSpace(headers.Get(key)); value != "" {
				req.Header.Set(key, value)
			}
		}
	}

	resp, err := getPooledClient(account, proxyURL).Do(req)
	if err != nil {
		if shouldRecyclePooledClient(err) {
			recyclePooledClient(account, proxyURL)
		}
		return nil, ErrUpstream(0, "请求 OpenAI Responses API 失败", err)
	}
	return resp, nil
}

func ExecuteOpenAIResponsesWebSocketRequest(ctx context.Context, account *auth.Account, requestBody []byte, proxyOverride string, headers http.Header) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	baseURL, apiKey := account.OpenAIResponsesCredentials()
	account.Mu().RLock()
	proxyURL := account.ProxyURL
	account.Mu().RUnlock()
	if proxyOverride != "" {
		proxyURL = proxyOverride
	}
	if baseURL == "" || apiKey == "" {
		return nil, ErrNoAvailableAccount()
	}

	wsURL, err := openAIResponsesWebSocketEndpoint(auth.OpenAIResponsesEndpoint(baseURL, "/v1/responses"))
	if err != nil {
		return nil, ErrInternalError("构建 OpenAI Responses WebSocket URL 失败", err)
	}
	dialer := &websocket.Dialer{
		HandshakeTimeout:  30 * time.Second,
		EnableCompression: true,
		NetDialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	if strings.TrimSpace(proxyURL) != "" {
		proxyURLParsed, err := url.Parse(proxyURL)
		if err != nil {
			return nil, ErrInternalError("解析代理地址失败", err)
		}
		dialer.Proxy = func(req *http.Request) (*url.URL, error) {
			return proxyURLParsed, nil
		}
	}

	upstreamHeaders := http.Header{}
	upstreamHeaders.Set("Authorization", "Bearer "+apiKey)
	if beta := strings.TrimSpace(headers.Get("OpenAI-Beta")); beta != "" {
		upstreamHeaders.Set("OpenAI-Beta", beta)
	} else {
		upstreamHeaders.Set("OpenAI-Beta", openAIResponsesWebSocketBeta)
	}
	for _, key := range []string{"OpenAI-Organization", "OpenAI-Project", "Idempotency-Key"} {
		if value := strings.TrimSpace(headers.Get(key)); value != "" {
			upstreamHeaders.Set(key, value)
		}
	}

	conn, handshakeResp, err := dialer.DialContext(ctx, wsURL, upstreamHeaders)
	if err != nil {
		if handshakeResp != nil {
			return handshakeResp, nil
		}
		return nil, ErrUpstream(0, "连接 OpenAI Responses WebSocket 失败", err)
	}

	wsBody := PrepareOpenAIResponsesWebSocketBody(requestBody)
	if err := conn.WriteMessage(websocket.TextMessage, wsBody); err != nil {
		conn.Close()
		return nil, ErrUpstream(0, "发送 OpenAI Responses WebSocket 请求失败", err)
	}

	pr, pw := io.Pipe()
	go bridgeOpenAIResponsesWebSocketToSSE(ctx, conn, pw)

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       pr,
	}, nil
}

func openAIResponsesWebSocketEndpoint(httpEndpoint string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(httpEndpoint))
	if err != nil {
		return "", err
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	case "wss", "ws":
	default:
		return "", fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	return parsed.String(), nil
}

func bridgeOpenAIResponsesWebSocketToSSE(ctx context.Context, conn *websocket.Conn, pw *io.PipeWriter) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	defer close(done)
	defer conn.Close()

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			_ = pw.CloseWithError(fmt.Errorf("websocket closed before response.completed: %w", err))
			return
		}
		if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
			continue
		}
		payload := data
		var compact bytes.Buffer
		if json.Compact(&compact, data) == nil {
			payload = compact.Bytes()
		}
		if _, err := fmt.Fprintf(pw, "data: %s\n\n", payload); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		eventType := gjson.GetBytes(data, "type").String()
		if eventType == "response.completed" || eventType == "response.failed" {
			_ = pw.Close()
			return
		}
	}
}

// ExecuteCompactRequest 向 Codex 上游发送 /responses/compact 请求（非流式压缩接口）
func ExecuteCompactRequest(ctx context.Context, account *auth.Account, requestBody []byte, sessionID string, proxyOverride string, apiKey string, deviceCfg *DeviceProfileConfig, headers http.Header) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	account.Mu().RLock()
	accessToken := account.AccessToken
	proxyURL := account.ProxyURL
	account.Mu().RUnlock()

	if proxyOverride != "" {
		proxyURL = proxyOverride
	}

	if accessToken == "" {
		return nil, ErrNoAvailableAccount()
	}

	// 与 ExecuteRequest 相同的请求体优化
	if !gjson.GetBytes(requestBody, "instructions").Exists() {
		requestBody, _ = sjson.SetBytes(requestBody, "instructions", "")
	}
	requestBody, _ = sjson.DeleteBytes(requestBody, "prompt_cache_retention")
	requestBody, _ = sjson.DeleteBytes(requestBody, "safety_identifier")
	requestBody, _ = sjson.DeleteBytes(requestBody, "disable_response_storage")

	existingCacheKey := strings.TrimSpace(gjson.GetBytes(requestBody, "prompt_cache_key").String())
	cacheKey := existingCacheKey
	if sessionID != "" {
		cacheKey = sessionID
		requestBody, _ = sjson.SetBytes(requestBody, "prompt_cache_key", cacheKey)
	}

	// compact 端点
	endpoint := CodexBaseURL + "/responses/compact"

	// Resin 反向代理模式
	var client *http.Client
	if IsResinEnabled() {
		endpoint = BuildReverseProxyURL(endpoint)
		client = getResinHTTPClient(account)
	} else {
		client = getPooledClient(account, proxyURL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return nil, ErrInternalError("创建请求失败", err)
	}

	applyCodexRequestHeaders(req, account, accessToken, cacheKey, apiKey, deviceCfg, headers)

	if IsResinEnabled() {
		req.Header.Set("X-Resin-Account", ResinAccountID(account))
	}
	logCodexFingerprintDebug("compact", account, proxyURL, req.Header)

	resp, err := client.Do(req)
	if err != nil {
		if shouldRecyclePooledClient(err) {
			recyclePooledClient(account, proxyURL)
		}
		return nil, ErrUpstream(0, "请求上游失败", err)
	}

	return resp, nil
}

func codexVersionFromProfile(profile deviceProfile, fallback string) string {
	if profile.HasVersion {
		return fmt.Sprintf("%d.%d.%d", profile.Version.major, profile.Version.minor, profile.Version.patch)
	}
	return strings.TrimSpace(fallback)
}

func codexVersionFromUserAgent(userAgent, fallback string) string {
	if version, ok := parseCodexCLIVersion(userAgent); ok {
		return fmt.Sprintf("%d.%d.%d", version.major, version.minor, version.patch)
	}
	return strings.TrimSpace(fallback)
}

func codexVersionFromString(raw string) (cliVersion, bool) {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "v"))
	if raw == "" {
		return cliVersion{}, false
	}
	return parseCodexCLIVersion("codex_cli_rs/" + raw)
}

func generatedCodexClientHeaders(account *auth.Account) (string, string) {
	accountID := int64(0)
	if account != nil {
		accountID = account.ID()
	}
	profile := ProfileForAccount(accountID)
	userAgent := strings.TrimSpace(profile.UserAgent)
	version := strings.TrimSpace(profile.Version)
	if userAgent == "" {
		userAgent = latestCodexCLIUserAgentPrefix
	}
	if version == "" {
		version = codexVersionFromUserAgent(userAgent, latestCodexCLIVersion)
	}
	return userAgent, version
}

func shouldGenerateCodexClientHeaders(settings RuntimeSettings, userAgent, originator string) bool {
	switch settings.ClientCompatMode {
	case ClientCompatModeForce:
		return true
	case ClientCompatModeAuto:
		version, ok := parseCodexCLIVersion(userAgent)
		if !ok {
			return false
		}
		minVersion, ok := codexVersionFromString(settings.CodexMinCLIVersion)
		if !ok {
			minVersion, _ = codexVersionFromString(defaultCodexMinCLIVersion)
		}
		return IsCodexOfficialClientByHeaders(userAgent, originator) && version.Compare(minVersion) < 0
	default:
		return false
	}
}

func applyCodexAllowedForwardHeaders(req *http.Request, downstreamHeaders http.Header) {
	if req == nil || downstreamHeaders == nil {
		return
	}
	for _, name := range codexAllowedForwardHeaders {
		if value := strings.TrimSpace(downstreamHeaders.Get(name)); value != "" {
			req.Header.Set(name, value)
		}
	}
}

func applyCodexRequestHeaders(req *http.Request, account *auth.Account, accessToken, cacheKey, apiKey string, deviceCfg *DeviceProfileConfig, downstreamHeaders http.Header) {
	if req == nil {
		return
	}

	accountID := ""
	if account != nil {
		account.Mu().RLock()
		accountID = account.AccountID
		account.Mu().RUnlock()
	}

	var profile deviceProfile
	version := ""
	usedGeneratedHeaders := false
	if IsDeviceProfileStabilizationEnabled(deviceCfg) {
		profile = ResolveDeviceProfile(account, apiKey, downstreamHeaders, deviceCfg)
		version = codexVersionFromProfile(profile, strings.TrimSpace(deviceCfg.PackageVersion))
		if strings.TrimSpace(profile.UserAgent) != "" {
			req.Header.Set("User-Agent", profile.UserAgent)
		}
	} else {
		userAgent := strings.TrimSpace(downstreamHeaders.Get("User-Agent"))
		originator := strings.TrimSpace(downstreamHeaders.Get("Originator"))
		if shouldGenerateCodexClientHeaders(CurrentRuntimeSettings(), userAgent, originator) {
			generatedUA, generatedVersion := generatedCodexClientHeaders(account)
			req.Header.Set("User-Agent", generatedUA)
			version = generatedVersion
			usedGeneratedHeaders = true
		} else if IsCodexOfficialClientByHeaders(userAgent, originator) && userAgent != "" {
			req.Header.Set("User-Agent", userAgent)
			version = firstNonEmptyHeader(downstreamHeaders, "Version", codexVersionFromUserAgent(userAgent, latestCodexCLIVersion))
		} else {
			req.Header.Set("User-Agent", latestCodexCLIUserAgentPrefix)
			version = latestCodexCLIVersion
		}
	}
	if version == "" {
		version = latestCodexCLIVersion
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if version != "" {
		req.Header.Set("Version", version)
	}
	if originator := strings.TrimSpace(downstreamHeaders.Get("Originator")); !usedGeneratedHeaders && originator != "" && IsCodexOfficialClientByHeaders("", originator) {
		req.Header.Set("Originator", originator)
	} else {
		req.Header.Set("Originator", Originator)
	}
	applyCodexAllowedForwardHeaders(req, downstreamHeaders)
	if accountID != "" {
		req.Header.Set("Chatgpt-Account-Id", accountID)
	}
	if cacheKey != "" {
		req.Header.Set("Session_id", cacheKey)
		req.Header.Del("Conversation_id")
	}
}

// ResolveSessionID 从下游请求提取或生成 session ID
// 优先级：
//  1. Header: Session_id
//  2. Header: Conversation_id
//  3. Body:   prompt_cache_key
//  4. 基于 Bearer API Key 的确定性 UUID
//
// Idempotency-Key 是单次请求的去重键，客户端通常每次请求都会生成新值。
// 将它当作会话标识会拆散同一线程的账号亲和性。
func ResolveSessionID(headers http.Header, body []byte) string {
	if headers != nil {
		if v := getHeaderCaseInsensitive(headers, "session_id", "session-id", "x-session-id", "conversation_id", "conversation-id", "x-conversation-id"); v != "" {
			return v
		}
	}
	// 优先从 body 的 prompt_cache_key 提取
	if v := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String()); v != "" {
		return v
	}

	// 基于下游用户的 API Key 生成确定性 cache key（参考 CLIProxyAPI codex_executor.go:621）
	authHeader := ""
	if headers != nil {
		authHeader = getHeaderCaseInsensitive(headers, "authorization")
	}
	apiKey := strings.TrimPrefix(authHeader, "Bearer ")
	apiKey = strings.TrimSpace(apiKey)
	if apiKey != "" {
		return uuid.NewSHA1(uuid.NameSpaceOID, []byte("codex2api:prompt-cache:"+apiKey)).String()
	}

	// 最后兜底：生成随机 UUID
	return uuid.New().String()
}

// ResolveResponsesSessionID isolates independent Responses conversations while
// preserving explicit session identifiers and locally known response chains.
func ResolveResponsesSessionID(headers http.Header, body []byte) string {
	if headers != nil {
		if value := getHeaderCaseInsensitive(headers, "session_id", "session-id", "x-session-id", "conversation_id", "conversation-id", "x-conversation-id"); value != "" {
			return value
		}
	}
	if value := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String()); value != "" {
		return value
	}
	previousID := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String())
	if previousID != "" {
		if entry, ok := getOpenAIResponsesContinuation(previousID); ok && entry.sessionID != "" {
			return entry.sessionID
		}
		return uuid.NewSHA1(uuid.NameSpaceOID, []byte("codex2api:responses:"+previousID)).String()
	}
	return uuid.New().String()
}

func getHeaderCaseInsensitive(headers http.Header, keys ...string) string {
	if headers == nil {
		return ""
	}
	for _, key := range keys {
		if v := headers.Get(key); strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		target := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", ""), "_", ""))
		for hKey, vals := range headers {
			normKey := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(hKey, "-", ""), "_", ""))
			if normKey == target && len(vals) > 0 {
				if v := strings.TrimSpace(vals[0]); v != "" {
					return v
				}
			}
		}
	}
	return ""
}

// ReadSSEStream 从上游 SSE 响应读取事件流
// callback 返回 true 表示继续读取，false 表示停止
func ReadSSEStream(body io.Reader, callback func(data []byte) bool) error {
	// 使用 sync.Pool 复用缓冲区，减少 GC 压力
	buf := sseBufferPool.Get().([]byte)
	defer sseBufferPool.Put(buf)

	lineBufPtr := sseLineBufPool.Get().(*[]byte)
	lineBuf := (*lineBufPtr)[:0]
	defer func() {
		// 归还时限制容量，避免异常大的缓冲区长期驻留池中
		if cap(lineBuf) <= 256*1024 {
			*lineBufPtr = lineBuf[:0]
			sseLineBufPool.Put(lineBufPtr)
		}
	}()

	var dataLines [][]byte

	emitEvent := func() bool {
		if len(dataLines) == 0 {
			return true
		}

		data := bytes.Join(dataLines, []byte("\n"))
		dataLines = dataLines[:0]
		if bytes.Equal(data, []byte("[DONE]")) {
			return false
		}
		return callback(data)
	}

	for {
		n, err := body.Read(buf)
		if n > 0 {
			lineBuf = append(lineBuf, buf[:n]...)

			// 按行处理
			for {
				idx := bytes.IndexByte(lineBuf, '\n')
				if idx < 0 {
					break
				}

				line := bytes.TrimRight(lineBuf[:idx], "\r")
				lineBuf = lineBuf[idx+1:]

				if len(line) == 0 {
					if !emitEvent() {
						return nil
					}
					continue
				}

				if bytes.HasPrefix(line, []byte(":")) {
					continue
				}

				// 解析 SSE data: 前缀，支持标准多行 data 聚合
				if bytes.HasPrefix(line, []byte("data:")) {
					data := bytes.TrimPrefix(line, []byte("data:"))
					data = bytes.TrimPrefix(data, []byte(" "))
					// 使用 copy 避免底层数组共享导致的内存泄漏
					dataCopy := make([]byte, len(data))
					copy(dataCopy, data)
					dataLines = append(dataLines, dataCopy)
				}
			}

			// 缩容：已消费数据超过一半时，将剩余数据移到头部释放前端内存
			if len(lineBuf) > 0 && cap(lineBuf) > 4096 && len(lineBuf) < cap(lineBuf)/4 {
				compact := make([]byte, len(lineBuf), cap(lineBuf)/2)
				copy(compact, lineBuf)
				lineBuf = compact
			}
		}

		if err != nil {
			if err == io.EOF {
				if len(lineBuf) > 0 {
					line := bytes.TrimRight(lineBuf, "\r")
					if bytes.HasPrefix(line, []byte("data:")) {
						data := bytes.TrimPrefix(line, []byte("data:"))
						data = bytes.TrimPrefix(data, []byte(" "))
						dataCopy := make([]byte, len(data))
						copy(dataCopy, data)
						dataLines = append(dataLines, dataCopy)
					}
				}
				if !emitEvent() {
					return nil
				}
				return nil
			}
			return err
		}
	}
}

// sseBufferPool 用于复用 SSE 读取缓冲区（64KB 以适应 reasoning 模型的大 thinking block）
var sseBufferPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 64*1024)
	},
}

// sseLineBufPool 用于复用行缓冲区，减少频繁分配
var sseLineBufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 0, 64*1024)
		return &b
	},
}
