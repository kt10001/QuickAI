package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
)

type cachedRelayResponse struct {
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers"`
	Body       []byte              `json:"body"`
	CachedAt   int64               `json:"cached_at"`
}

type PrefixInfo struct {
	UserID         int
	PrefixHash     string
	DeltaHash      string
	DeltaMessages  []any
	IsContinuation bool
}

type CacheMeta struct {
	Model        string    `json:"model"`
	ChannelType  string    `json:"channel_type"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	HitCount     int       `json:"hit_count"`
	CreatedAt    time.Time `json:"created_at"`
	LastHitAt    time.Time `json:"last_hit_at"`
}

var (
	responseCacheOnce sync.Once
	responseCacheRDB  *redis.Client
	responseCacheOn   bool

	responseCacheLocalMu       sync.RWMutex
	responseCacheLocalVersions = map[string]int{}
	responseCacheLocalSystems  = map[string]string{}
	responseCacheLocalLastPref = map[string]string{}
	responseCacheLocalDelta    = map[string][]byte{}
	responseCacheLocalMeta     = map[string]CacheMeta{}
	responseCacheLocalExpiry   = map[string]time.Time{}
)

const (
	responseCacheVersionPrefix    = "relay:version"
	responseCacheSystemHashPrefix = "relay:system_hash"
	responseCacheL2Prefix         = "relay:l2:delta"
)

var cacheChannelTypeWeight = map[string]float64{
	"reverse":  2.0,
	"official": 1.0,
	"free":     0.3,
}

func cacheModelCostWeight(model string) float64 {
	switch {
	case strings.HasPrefix(model, "claude-opus"):
		return 15.0
	case strings.HasPrefix(model, "claude-sonnet"):
		return 3.0
	case strings.HasPrefix(model, "claude-haiku"):
		return 0.8
	case strings.HasPrefix(model, "gpt-4o"):
		return 2.5
	case strings.HasPrefix(model, "o3"):
		return 10.0
	case strings.HasPrefix(model, "text-embedding"):
		return 0.13
	default:
		return 1.0
	}
}

func computeCacheValue(meta CacheMeta) float64 {
	channelWeight, ok := cacheChannelTypeWeight[meta.ChannelType]
	if !ok {
		channelWeight = 1.0
	}
	totalTokens := float64(meta.InputTokens + meta.OutputTokens)
	if totalTokens <= 0 {
		totalTokens = 1
	}
	hitCount := float64(meta.HitCount)
	if hitCount <= 0 {
		hitCount = 1
	}
	return hitCount * cacheModelCostWeight(meta.Model) * (totalTokens / 1_000_000) * channelWeight
}

func UpdateCacheMeta(cacheKey, model, channelType string, inputTokens, outputTokens int) {
	if cacheKey == "" || model == "" {
		return
	}
	now := time.Now()
	meta := CacheMeta{
		Model:        model,
		ChannelType:  channelType,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		HitCount:     0,
		CreatedAt:    now,
		LastHitAt:    now,
	}
	ttl := common.GetResponseCacheTTLByModel(model)
	responseCacheLocalMu.Lock()
	defer responseCacheLocalMu.Unlock()
	responseCacheLocalMeta[cacheKey] = meta
	responseCacheLocalExpiry[cacheKey] = now.Add(ttl)
}

func RecordCacheHit(cacheKey, model string) {
	if cacheKey == "" {
		return
	}
	responseCacheLocalMu.Lock()
	defer responseCacheLocalMu.Unlock()
	meta, ok := responseCacheLocalMeta[cacheKey]
	if !ok {
		return
	}
	meta.HitCount++
	meta.LastHitAt = time.Now()
	if model != "" {
		meta.Model = model
	}
	responseCacheLocalMeta[cacheKey] = meta
	remaining := time.Until(responseCacheLocalExpiry[cacheKey])
	if remaining > 0 && remaining < 5*time.Minute {
		responseCacheLocalExpiry[cacheKey] = time.Now().Add(common.GetResponseCacheTTLByModel(meta.Model))
	}
}

// SmartEvict keeps high-value cache entries and evicts low-value ones when over maxEntries.
func SmartEvict(maxEntries int) int {
	if maxEntries <= 0 {
		return 0
	}
	responseCacheLocalMu.Lock()
	defer responseCacheLocalMu.Unlock()
	if len(responseCacheLocalMeta) <= maxEntries {
		return 0
	}
	type kv struct {
		key   string
		score float64
	}
	items := make([]kv, 0, len(responseCacheLocalMeta))
	for k, m := range responseCacheLocalMeta {
		items = append(items, kv{key: k, score: computeCacheValue(m)})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].score < items[j].score })
	toRemove := len(items) - maxEntries
	for i := 0; i < toRemove; i++ {
		delete(responseCacheLocalMeta, items[i].key)
		delete(responseCacheLocalExpiry, items[i].key)
	}
	return toRemove
}

// AutoRenewHotEntries extends TTL for hot entries that are close to expiration.
func AutoRenewHotEntries(minHits int) int {
	if minHits <= 0 {
		minHits = 1
	}
	now := time.Now()
	renewed := 0
	responseCacheLocalMu.Lock()
	defer responseCacheLocalMu.Unlock()
	for key, meta := range responseCacheLocalMeta {
		if meta.HitCount < minHits {
			continue
		}
		remaining := responseCacheLocalExpiry[key].Sub(now)
		if remaining > 0 && remaining < 5*time.Minute {
			responseCacheLocalExpiry[key] = now.Add(common.GetResponseCacheTTLByModel(meta.Model))
			renewed++
		}
	}
	return renewed
}

func initResponseCache() {
	if !common.ResponseCacheEnabled {
		common.SysLog("response cache disabled by CACHE_ENABLED")
		responseCacheOn = false
		return
	}
	if common.ResponseCacheRedisAddr == "" {
		common.SysError("response cache redis addr is empty (CACHE_REDIS_ADDR), cache disabled")
		responseCacheOn = false
		return
	}
	if !common.ResponseCacheAllowShared && isSharedWithPrimaryRedis(common.ResponseCacheRedisAddr) {
		common.SysError("response cache redis shares the same instance as primary redis; cache disabled for safety. Set CACHE_ALLOW_SHARED_REDIS=true to override")
		responseCacheOn = false
		return
	}
	responseCacheRDB = redis.NewClient(&redis.Options{
		Addr:     common.ResponseCacheRedisAddr,
		Password: common.ResponseCacheRedisPassword,
		DB:       0,
		PoolSize: 50,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := responseCacheRDB.Ping(ctx).Err(); err != nil {
		common.SysError("response cache redis ping failed: " + err.Error())
		responseCacheOn = false
		return
	}
	responseCacheOn = true
	common.SysLog("response cache redis connected")
}

func normalizeRedisEndpoint(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
		port = "6379"
		if strings.Contains(addr, ":") && !strings.Contains(addr, "]") {
			parts := strings.Split(addr, ":")
			if len(parts) == 2 && parts[1] != "" {
				host = parts[0]
				port = parts[1]
			}
		}
	}
	host = strings.Trim(host, "[]")
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" {
		host = "127.0.0.1"
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	p, convErr := strconv.Atoi(port)
	if convErr != nil || p <= 0 {
		p = 6379
	}
	return net.JoinHostPort(host, strconv.Itoa(p))
}

func isSharedWithPrimaryRedis(cacheAddr string) bool {
	conn := strings.TrimSpace(os.Getenv("REDIS_CONN_STRING"))
	if conn == "" {
		return false
	}
	opt, err := redis.ParseURL(conn)
	if err != nil {
		return false
	}
	return normalizeRedisEndpoint(opt.Addr) == normalizeRedisEndpoint(cacheAddr)
}

func responseCacheEnabled() bool {
	responseCacheOnce.Do(initResponseCache)
	return responseCacheOn
}

func BuildResponseCacheKey(requestBody []byte, model string, path string) string {
	version := GetModelVersion(model)
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|v%d|%s", path, model, version, string(requestBody))))
	return fmt.Sprintf("%s:%s", common.ResponseCachePrefix, hex.EncodeToString(hash[:]))
}

func sha256Hex(raw string) string {
	hash := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(hash[:])
}

func buildSystemHashKey(model string) string {
	return fmt.Sprintf("%s:%s", responseCacheSystemHashPrefix, model)
}

func buildVersionKey(model string) string {
	return fmt.Sprintf("%s:%s", responseCacheVersionPrefix, model)
}

func GetModelVersion(model string) int {
	if model == "" {
		return 1
	}
	if !responseCacheEnabled() {
		responseCacheLocalMu.RLock()
		defer responseCacheLocalMu.RUnlock()
		if v, ok := responseCacheLocalVersions[model]; ok && v > 0 {
			return v
		}
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	version, err := responseCacheRDB.Get(ctx, buildVersionKey(model)).Int()
	if err == redis.Nil {
		return 1
	}
	if err != nil || version <= 0 {
		return 1
	}
	return version
}

func BumpModelVersion(model string) int {
	if model == "" {
		return 1
	}
	if !responseCacheEnabled() {
		responseCacheLocalMu.Lock()
		defer responseCacheLocalMu.Unlock()
		if _, ok := responseCacheLocalVersions[model]; !ok || responseCacheLocalVersions[model] <= 0 {
			responseCacheLocalVersions[model] = 1
		}
		responseCacheLocalVersions[model]++
		if responseCacheLocalVersions[model] <= 0 {
			responseCacheLocalVersions[model] = 1
		}
		return responseCacheLocalVersions[model]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	key := buildVersionKey(model)
	current := 1
	if v, err := responseCacheRDB.Get(ctx, key).Int(); err == nil && v > 0 {
		current = v
	}
	next := current + 1
	if err := responseCacheRDB.Set(ctx, key, next, 0).Err(); err != nil {
		return current
	}
	return next
}

func CheckSystemChange(model string, system any) bool {
	if model == "" {
		return false
	}
	systemRaw, err := common.Marshal(system)
	if err != nil {
		return false
	}
	currentHash := sha256Hex(string(systemRaw))
	if !responseCacheEnabled() {
		responseCacheLocalMu.Lock()
		defer responseCacheLocalMu.Unlock()
		prev := responseCacheLocalSystems[model]
		if prev == "" {
			responseCacheLocalSystems[model] = currentHash
			return false
		}
		if prev == currentHash {
			return false
		}
		responseCacheLocalSystems[model] = currentHash
		if _, ok := responseCacheLocalVersions[model]; !ok || responseCacheLocalVersions[model] <= 0 {
			responseCacheLocalVersions[model] = 1
		}
		responseCacheLocalVersions[model]++
		if responseCacheLocalVersions[model] <= 0 {
			responseCacheLocalVersions[model] = 1
		}
		return true
	}
	key := buildSystemHashKey(model)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	prevHash, err := responseCacheRDB.Get(ctx, key).Result()
	if err == redis.Nil {
		_ = responseCacheRDB.Set(ctx, key, currentHash, 0).Err()
		return false
	}
	if err != nil {
		return false
	}
	if prevHash == currentHash {
		return false
	}
	_ = responseCacheRDB.Set(ctx, key, currentHash, 0).Err()
	BumpModelVersion(model)
	return true
}

func TrackPrefix(userID int, model string, system any, messages []any) *PrefixInfo {
	if userID <= 0 || model == "" || len(messages) < 2 {
		return nil
	}
	systemRaw, err := common.Marshal(system)
	if err != nil {
		return nil
	}
	prefixMessages := messages[:len(messages)-2]
	deltaMessages := messages[len(messages)-2:]
	prefixRaw, err := common.Marshal(prefixMessages)
	if err != nil {
		return nil
	}
	deltaRaw, err := common.Marshal(deltaMessages)
	if err != nil {
		return nil
	}
	prefixBase := fmt.Sprintf("u%d|%s|%s|%s", userID, model, sha256Hex(string(systemRaw)), string(prefixRaw))
	prefixHash := sha256Hex(prefixBase)
	deltaHash := sha256Hex(string(deltaRaw))

	lastPrefixKey := fmt.Sprintf("%s:last:%d:%s", responseCacheL2Prefix, userID, model)
	prevPrefix := ""
	if responseCacheEnabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		prevPrefix, _ = responseCacheRDB.Get(ctx, lastPrefixKey).Result()
		ttl := common.GetResponseCacheTTLByModel(model)
		_ = responseCacheRDB.Set(ctx, lastPrefixKey, prefixHash, ttl).Err()
	} else {
		responseCacheLocalMu.Lock()
		prevPrefix = responseCacheLocalLastPref[lastPrefixKey]
		responseCacheLocalLastPref[lastPrefixKey] = prefixHash
		responseCacheLocalMu.Unlock()
	}

	return &PrefixInfo{
		UserID:         userID,
		PrefixHash:     prefixHash,
		DeltaHash:      deltaHash,
		DeltaMessages:  deltaMessages,
		IsContinuation: prevPrefix == prefixHash && prevPrefix != "",
	}
}

func buildL2DeltaKey(userID int, model string, prefixHash, deltaHash string) string {
	return fmt.Sprintf("%s:u%d:%s:%s:%s", responseCacheL2Prefix, userID, strings.ToLower(model), prefixHash, deltaHash)
}

func CheckL2DeltaCache(userID int, model, prefixHash, deltaHash string) ([]byte, bool) {
	if userID <= 0 || model == "" || prefixHash == "" || deltaHash == "" {
		return nil, false
	}
	key := buildL2DeltaKey(userID, model, prefixHash, deltaHash)
	if responseCacheEnabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		raw, err := responseCacheRDB.Get(ctx, key).Bytes()
		if err != nil {
			return nil, false
		}
		return raw, true
	}
	responseCacheLocalMu.RLock()
	defer responseCacheLocalMu.RUnlock()
	raw, ok := responseCacheLocalDelta[key]
	if !ok || len(raw) == 0 {
		return nil, false
	}
	cp := make([]byte, len(raw))
	copy(cp, raw)
	return cp, true
}

func SetL2DeltaCache(userID int, model, prefixHash, deltaHash string, responseBody []byte) {
	if userID <= 0 || model == "" || prefixHash == "" || deltaHash == "" || len(responseBody) == 0 {
		return
	}
	if len(responseBody) > common.ResponseCacheMaxBodyBytes {
		return
	}
	key := buildL2DeltaKey(userID, model, prefixHash, deltaHash)
	if responseCacheEnabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		ttl := common.GetResponseCacheTTLByModel(model)
		_ = responseCacheRDB.Set(ctx, key, responseBody, ttl).Err()
		return
	}
	responseCacheLocalMu.Lock()
	defer responseCacheLocalMu.Unlock()
	cp := make([]byte, len(responseBody))
	copy(cp, responseBody)
	responseCacheLocalDelta[key] = cp
}

func GetCachedRelayResponse(c *gin.Context, key string) (*cachedRelayResponse, error) {
	if !responseCacheEnabled() {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	raw, err := responseCacheRDB.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		logger.LogError(c, "response cache get failed: "+err.Error())
		return nil, err
	}
	var item cachedRelayResponse
	if err = common.Unmarshal(raw, &item); err != nil {
		logger.LogError(c, "response cache decode failed: "+err.Error())
		return nil, err
	}
	return &item, nil
}

func SetCachedRelayResponse(c *gin.Context, key string, model string, statusCode int, headers map[string][]string, body []byte) {
	if !responseCacheEnabled() {
		return
	}
	if len(body) == 0 || len(body) > common.ResponseCacheMaxBodyBytes {
		return
	}
	item := cachedRelayResponse{
		StatusCode: statusCode,
		Headers:    headers,
		Body:       body,
		CachedAt:   time.Now().Unix(),
	}
	raw, err := common.Marshal(item)
	if err != nil {
		logger.LogError(c, "response cache encode failed: "+err.Error())
		return
	}
	ttl := common.GetResponseCacheTTLByModel(model)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = responseCacheRDB.Set(ctx, key, raw, ttl).Err(); err != nil {
		logger.LogError(c, "response cache set failed: "+err.Error())
	}
}
