package service

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/require"
)

func TestBuildResponseCacheKeyStableAndPathSensitive(t *testing.T) {
	body := []byte(`{"model":"gpt-4o-mini","input":"hello"}`)

	key1 := BuildResponseCacheKey(body, "gpt-4o-mini", "/v1/responses")
	key2 := BuildResponseCacheKey(body, "gpt-4o-mini", "/v1/responses")
	key3 := BuildResponseCacheKey(body, "gpt-4o-mini", "/v1/chat/completions")

	require.Equal(t, key1, key2)
	require.NotEqual(t, key1, key3)
	require.Contains(t, key1, common.ResponseCachePrefix+":")
}

func TestGetResponseCacheTTLByModel(t *testing.T) {
	originalTTL := common.ResponseCacheTTLSeconds
	defer func() {
		common.ResponseCacheTTLSeconds = originalTTL
	}()

	common.ResponseCacheTTLSeconds = 3600

	require.Equal(t, time.Hour, common.GetResponseCacheTTLByModel(""))
	require.Equal(t, 24*time.Hour, common.GetResponseCacheTTLByModel("claude-opus-4-1"))
	require.Equal(t, 12*time.Hour, common.GetResponseCacheTTLByModel("gpt-4o-2024-11-20"))
	require.Equal(t, 48*time.Hour, common.GetResponseCacheTTLByModel("text-embedding-3-large"))
	require.Equal(t, 12*time.Hour, common.GetResponseCacheTTLByModel("gpt-4o-mini"))
	require.Equal(t, time.Hour, common.GetResponseCacheTTLByModel("gpt-3.5-turbo"))
}

func TestResponseCacheEmbeddingModelDetection(t *testing.T) {
	require.True(t, common.IsEmbeddingModel("text-embedding-3-large"))
	require.True(t, common.IsEmbeddingModel("bge-m3"))
	require.True(t, common.IsEmbeddingModel("e5-large-v2"))
	require.False(t, common.IsEmbeddingModel("gpt-4o-mini"))
}
