package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/url"
	"strings"
	"time"
)

const (
	responsesTransportCacheNamespace        = "responses-upstream-transport"
	responsesTransportCacheTimeout          = 300 * time.Millisecond
	responsesWSUnsupportedPreferenceTTL     = 30 * time.Minute
	responsesWSMissingTerminalPreferenceTTL = 2 * time.Minute
	responsesTransportHTTPMode              = "http"
)

type responsesTransportPreference struct {
	Mode string `json:"mode"`
}

func normalizeResponsesCapabilityEndpoint(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return strings.ToLower(strings.TrimSpace(endpoint))
	}

	switch strings.ToLower(parsed.Scheme) {
	case "ws":
		parsed.Scheme = "http"
	case "wss":
		parsed.Scheme = "https"
	default:
		parsed.Scheme = strings.ToLower(parsed.Scheme)
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""

	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	parsed.Host = host
	if port != "" {
		parsed.Host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	if parsed.Path != "/" {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
	}
	return parsed.String()
}

func responsesTransportCacheKey(endpoint string) string {
	sum := sha256.Sum256([]byte(normalizeResponsesCapabilityEndpoint(endpoint)))
	return "v1:" + hex.EncodeToString(sum[:])
}

func runtimeCacheContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, responsesTransportCacheTimeout)
}

func (h *Handler) prefersResponsesHTTP(ctx context.Context, endpoint string) bool {
	if h == nil || h.cache == nil || strings.TrimSpace(endpoint) == "" {
		return false
	}
	key := responsesTransportCacheKey(endpoint)
	cacheCtx, cancel := runtimeCacheContext(ctx)
	raw, ok, err := h.cache.GetRuntime(cacheCtx, responsesTransportCacheNamespace, key)
	cancel()
	if err != nil {
		log.Printf("responses_transport_cache op=get result=fail_open key=%s err=%v", key, err)
		return false
	}
	if !ok {
		return false
	}
	var preference responsesTransportPreference
	if err := json.Unmarshal(raw, &preference); err != nil {
		log.Printf("responses_transport_cache op=get result=invalid key=%s err=%v", key, err)
		return false
	}
	return preference.Mode == responsesTransportHTTPMode
}

func (h *Handler) cacheResponsesHTTPPreference(ctx context.Context, endpoint string, ttl time.Duration) {
	if h == nil || h.cache == nil || strings.TrimSpace(endpoint) == "" || ttl <= 0 {
		return
	}
	key := responsesTransportCacheKey(endpoint)
	payload, err := json.Marshal(responsesTransportPreference{Mode: responsesTransportHTTPMode})
	if err != nil {
		return
	}
	cacheCtx, cancel := runtimeCacheContext(ctx)
	err = h.cache.SetRuntime(cacheCtx, responsesTransportCacheNamespace, key, payload, ttl)
	cancel()
	if err != nil {
		log.Printf("responses_transport_cache op=set result=fail_open key=%s ttl=%s err=%v", key, ttl, err)
		return
	}
	log.Printf("responses_transport_cache op=set result=ok key=%s mode=http ttl=%s", key, ttl)
}
