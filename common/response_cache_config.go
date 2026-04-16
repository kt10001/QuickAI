package common

import (
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	ResponseCacheEnabled       = true
	ResponseCacheRedisAddr     = ""
	ResponseCacheRedisPassword = ""
	ResponseCachePrefix        = "relay:cache"
	ResponseCacheTTLSeconds    = 21600
	ResponseCacheMaxBodyBytes  = 2 << 20 // 2MB
)

func init() {
	if v := strings.TrimSpace(os.Getenv("CACHE_ENABLED")); v != "" {
		ResponseCacheEnabled = strings.ToLower(v) != "false"
	}
	if v := strings.TrimSpace(os.Getenv("CACHE_REDIS_ADDR")); v != "" {
		ResponseCacheRedisAddr = v
	}
	if v := os.Getenv("CACHE_REDIS_PASSWORD"); v != "" {
		ResponseCacheRedisPassword = v
	}
	if v := strings.TrimSpace(os.Getenv("CACHE_PREFIX")); v != "" {
		ResponseCachePrefix = v
	}
	if v := strings.TrimSpace(os.Getenv("CACHE_TTL_SECONDS")); v != "" {
		if i, err := strconv.Atoi(v); err == nil && i > 0 {
			ResponseCacheTTLSeconds = i
		}
	}
	if v := strings.TrimSpace(os.Getenv("CACHE_MAX_BODY_BYTES")); v != "" {
		if i, err := strconv.Atoi(v); err == nil && i > 0 {
			ResponseCacheMaxBodyBytes = i
		}
	}
}

func GetResponseCacheTTLByModel(model string) time.Duration {
	base := time.Duration(ResponseCacheTTLSeconds) * time.Second
	if model == "" {
		return base
	}
	// 模型成本越高，缓存保留越久；前缀匹配足够覆盖版本后缀。
	switch {
	case strings.HasPrefix(model, "claude-opus"):
		return 24 * time.Hour
	case strings.HasPrefix(model, "claude-sonnet"), strings.HasPrefix(model, "gpt-4o"), strings.HasPrefix(model, "o3"), strings.HasPrefix(model, "gemini-2.5-pro"):
		return 12 * time.Hour
	case strings.HasPrefix(model, "text-embedding"), strings.HasPrefix(model, "bge-"):
		return 48 * time.Hour
	default:
		return base
	}
}

func IsEmbeddingModel(model string) bool {
	for _, prefix := range []string{"text-embedding", "bge-", "e5-"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

func IsImageModel(model string) bool {
	for _, prefix := range []string{"dall-e", "midjourney", "flux", "stable-diffusion"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

func ShouldCacheRelayModel(model string, temperature float64, hasSeed bool) bool {
	if !ResponseCacheEnabled {
		return false
	}
	if IsEmbeddingModel(model) {
		return true
	}
	if IsImageModel(model) {
		return hasSeed
	}
	return temperature <= 0.5
}
