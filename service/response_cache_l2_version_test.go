package service

import (
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func resetResponseCacheLocalStateForTest() {
	responseCacheOnce = sync.Once{}
	responseCacheOn = false
	responseCacheRDB = nil
	responseCacheLocalMu.Lock()
	defer responseCacheLocalMu.Unlock()
	responseCacheLocalVersions = map[string]int{}
	responseCacheLocalSystems = map[string]string{}
	responseCacheLocalLastPref = map[string]string{}
	responseCacheLocalDelta = map[string][]byte{}
}

func TestVersioningAndSystemChange_LocalFallback(t *testing.T) {
	originalEnabled := common.ResponseCacheEnabled
	originalAddr := common.ResponseCacheRedisAddr
	defer func() {
		common.ResponseCacheEnabled = originalEnabled
		common.ResponseCacheRedisAddr = originalAddr
	}()
	common.ResponseCacheEnabled = true
	common.ResponseCacheRedisAddr = ""
	resetResponseCacheLocalStateForTest()

	require.Equal(t, 1, GetModelVersion("claude-sonnet-4-6"))
	require.False(t, CheckSystemChange("claude-sonnet-4-6", "system-v1"))
	require.Equal(t, 1, GetModelVersion("claude-sonnet-4-6"))
	require.False(t, CheckSystemChange("claude-sonnet-4-6", "system-v1"))
	require.True(t, CheckSystemChange("claude-sonnet-4-6", "system-v2"))
	require.Equal(t, 2, GetModelVersion("claude-sonnet-4-6"))
	require.Equal(t, 3, BumpModelVersion("claude-sonnet-4-6"))
	require.Equal(t, 3, GetModelVersion("claude-sonnet-4-6"))
}

func TestTrackPrefixAndL2DeltaCacheIsolation_LocalFallback(t *testing.T) {
	originalEnabled := common.ResponseCacheEnabled
	originalAddr := common.ResponseCacheRedisAddr
	defer func() {
		common.ResponseCacheEnabled = originalEnabled
		common.ResponseCacheRedisAddr = originalAddr
	}()
	common.ResponseCacheEnabled = true
	common.ResponseCacheRedisAddr = ""
	resetResponseCacheLocalStateForTest()

	messages1 := []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": "hi"},
		map[string]any{"role": "user", "content": "question-1"},
		map[string]any{"role": "assistant", "content": "answer-1"},
	}
	messages2 := []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": "hi"},
		map[string]any{"role": "user", "content": "question-2"},
		map[string]any{"role": "assistant", "content": "answer-2"},
	}

	// first request only records prefix, no continuation yet
	p1 := TrackPrefix(1001, "gpt-4o-mini", "sys-a", messages1)
	require.NotNil(t, p1)
	require.False(t, p1.IsContinuation)

	// second request with same prefix is continuation
	p2 := TrackPrefix(1001, "gpt-4o-mini", "sys-a", messages2)
	require.NotNil(t, p2)
	require.True(t, p2.IsContinuation)
	require.Equal(t, p1.PrefixHash, p2.PrefixHash)
	require.NotEqual(t, p1.DeltaHash, p2.DeltaHash)

	payload := []byte(`{"id":"resp-l2","choices":[{"message":{"role":"assistant","content":"cached"}}]}`)
	SetL2DeltaCache(1001, "gpt-4o-mini", p2.PrefixHash, p2.DeltaHash, payload)
	got, ok := CheckL2DeltaCache(1001, "gpt-4o-mini", p2.PrefixHash, p2.DeltaHash)
	require.True(t, ok)
	require.Equal(t, payload, got)

	// different user cannot hit same delta cache
	_, ok = CheckL2DeltaCache(1002, "gpt-4o-mini", p2.PrefixHash, p2.DeltaHash)
	require.False(t, ok)
}

func TestBuildResponseCacheKeyIncludesVersion_LocalFallback(t *testing.T) {
	originalEnabled := common.ResponseCacheEnabled
	originalAddr := common.ResponseCacheRedisAddr
	defer func() {
		common.ResponseCacheEnabled = originalEnabled
		common.ResponseCacheRedisAddr = originalAddr
	}()
	common.ResponseCacheEnabled = true
	common.ResponseCacheRedisAddr = ""
	resetResponseCacheLocalStateForTest()

	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`)
	k1 := BuildResponseCacheKey(body, "claude-sonnet-4-6", "/v1/chat/completions")
	BumpModelVersion("claude-sonnet-4-6")
	k2 := BuildResponseCacheKey(body, "claude-sonnet-4-6", "/v1/chat/completions")
	require.NotEqual(t, k1, k2)
}
