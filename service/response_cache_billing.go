package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	modelrepo "github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

const cacheHitChargePrefix = "relay:cache:charge"

func extractUsageTokensFromCachedBody(body []byte) (int, int, bool) {
	var data map[string]any
	if err := common.Unmarshal(body, &data); err != nil {
		return 0, 0, false
	}
	usage, ok := data["usage"].(map[string]any)
	if !ok {
		return 0, 0, false
	}
	var promptTokens, completionTokens int
	if v, ok := usage["prompt_tokens"].(float64); ok {
		promptTokens = int(v)
	}
	if v, ok := usage["completion_tokens"].(float64); ok {
		completionTokens = int(v)
	}
	if v, ok := usage["input_tokens"].(float64); ok {
		promptTokens = int(v)
	}
	if v, ok := usage["output_tokens"].(float64); ok {
		completionTokens = int(v)
	}
	if promptTokens == 0 && completionTokens == 0 {
		return 0, 0, false
	}
	return promptTokens, completionTokens, true
}

func buildCacheHitChargeKey(c *gin.Context, cacheKey string) string {
	requestID := c.GetString(common.RequestIdKey)
	if requestID == "" {
		requestID = fmt.Sprintf("%d:%d", c.GetInt("id"), time.Now().UnixNano())
	}
	hash := sha256.Sum256([]byte(cacheKey + "|" + requestID))
	return fmt.Sprintf("%s:%s", cacheHitChargePrefix, hex.EncodeToString(hash[:]))
}

func tryAcquireCacheHitChargeLock(c *gin.Context, lockKey string, ttl time.Duration) bool {
	if responseCacheEnabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
		defer cancel()
		ok, err := responseCacheRDB.SetNX(ctx, lockKey, "1", ttl).Result()
		if err == nil {
			return ok
		}
		logger.LogError(c, "cache hit billing lock failed: "+err.Error())
		return false
	}
	responseCacheLocalMu.Lock()
	defer responseCacheLocalMu.Unlock()
	if exp, ok := responseCacheLocalExpiry[lockKey]; ok && exp.After(time.Now()) {
		return false
	}
	responseCacheLocalExpiry[lockKey] = time.Now().Add(ttl)
	return true
}

func ConsumeCacheHitQuota(c *gin.Context, cacheKey, model string, cachedBody []byte) {
	if !common.ResponseCacheHitBilling {
		return
	}
	userID := c.GetInt("id")
	tokenID := c.GetInt("token_id")
	tokenKey := c.GetString("token_key")
	channelID := c.GetInt("channel_id")
	if userID <= 0 || model == "" || cacheKey == "" {
		return
	}

	// Safety-first: subscription billing has additional lifecycle semantics; skip in minimal mode.
	if hasSub, _ := modelrepo.HasActiveUserSubscription(userID); hasSub {
		logger.LogInfo(c, fmt.Sprintf("cache hit billing skipped: subscription user_id=%d model=%s", userID, model))
		return
	}

	lockKey := buildCacheHitChargeKey(c, cacheKey)
	if !tryAcquireCacheHitChargeLock(c, lockKey, 10*time.Minute) {
		return
	}

	promptTokens, completionTokens, ok := extractUsageTokensFromCachedBody(cachedBody)
	if !ok {
		logger.LogInfo(c, fmt.Sprintf("cache hit billing skipped: missing usage model=%s", model))
		return
	}

	modelRatio, ok, _ := ratio_setting.GetModelRatio(model)
	if !ok {
		modelRatio = 15
	}
	completionRatio := ratio_setting.GetCompletionRatio(model)
	usingGroup := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	if usingGroup == "" {
		usingGroup = c.GetString("group")
	}
	groupRatio := ratio_setting.GetGroupRatio(usingGroup)
	if groupRatio <= 0 {
		groupRatio = 1
	}
	quotaFloat := (float64(promptTokens) + float64(completionTokens)*completionRatio) * modelRatio * groupRatio
	quota := int(math.Round(quotaFloat))
	if quota <= 0 {
		return
	}

	if err := modelrepo.DecreaseUserQuota(userID, quota, false); err != nil {
		logger.LogError(c, "cache hit billing user quota failed: "+err.Error())
		return
	}
	if tokenID > 0 && tokenKey != "" {
		if err := modelrepo.DecreaseTokenQuota(tokenID, tokenKey, quota); err != nil {
			// best-effort rollback user quota to keep balance consistent
			_ = modelrepo.IncreaseUserQuota(userID, quota, false)
			logger.LogError(c, "cache hit billing token quota failed: "+err.Error())
			return
		}
	}
	modelrepo.UpdateUserUsedQuotaAndRequestCount(userID, quota)
	if channelID > 0 {
		modelrepo.UpdateChannelUsedQuota(channelID, quota)
	}
	logger.LogInfo(c, fmt.Sprintf("cache hit billing consumed: quota=%s model=%s prompt=%d completion=%d", logger.FormatQuota(quota), model, promptTokens, completionTokens))
}
