package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSmartEvict_RemovesLowValueFirst(t *testing.T) {
	resetResponseCacheLocalStateForTest()

	UpdateCacheMeta("k_opus", "claude-opus-4-6", "reverse", 200000, 50000)
	UpdateCacheMeta("k_haiku", "claude-haiku-4-5", "free", 200000, 50000)
	UpdateCacheMeta("k_mid", "gpt-4o-mini", "official", 200000, 50000)

	// Make opus obviously hot/high-value, haiku low-value.
	for i := 0; i < 5; i++ {
		RecordCacheHit("k_opus", "claude-opus-4-6")
	}
	RecordCacheHit("k_mid", "gpt-4o-mini")

	removed := SmartEvict(2)
	require.Equal(t, 1, removed)

	responseCacheLocalMu.RLock()
	defer responseCacheLocalMu.RUnlock()
	_, opusExists := responseCacheLocalMeta["k_opus"]
	_, haikuExists := responseCacheLocalMeta["k_haiku"]
	_, midExists := responseCacheLocalMeta["k_mid"]
	require.True(t, opusExists)
	require.False(t, haikuExists)
	require.True(t, midExists)
}

func TestAutoRenewHotEntries_RenewsNearExpiry(t *testing.T) {
	resetResponseCacheLocalStateForTest()

	UpdateCacheMeta("k_hot", "gpt-4o-mini", "official", 1000, 200)
	for i := 0; i < 3; i++ {
		RecordCacheHit("k_hot", "gpt-4o-mini")
	}

	responseCacheLocalMu.Lock()
	responseCacheLocalExpiry["k_hot"] = time.Now().Add(2 * time.Minute)
	before := responseCacheLocalExpiry["k_hot"]
	responseCacheLocalMu.Unlock()

	renewed := AutoRenewHotEntries(3)
	require.Equal(t, 1, renewed)

	responseCacheLocalMu.RLock()
	after := responseCacheLocalExpiry["k_hot"]
	responseCacheLocalMu.RUnlock()

	require.True(t, after.After(before))
	require.True(t, after.Sub(time.Now()) > 10*time.Minute)
}

func TestAutoRenewHotEntries_DoesNotRenewColdEntries(t *testing.T) {
	resetResponseCacheLocalStateForTest()

	UpdateCacheMeta("k_cold", "gpt-4o-mini", "official", 1000, 200)
	responseCacheLocalMu.Lock()
	exp := time.Now().Add(2 * time.Minute)
	responseCacheLocalExpiry["k_cold"] = exp
	responseCacheLocalMu.Unlock()

	renewed := AutoRenewHotEntries(3)
	require.Equal(t, 0, renewed)

	responseCacheLocalMu.RLock()
	got := responseCacheLocalExpiry["k_cold"]
	responseCacheLocalMu.RUnlock()
	require.Equal(t, exp.Unix(), got.Unix())
}
