package middleware

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

func shouldCacheRelayRequest(c *gin.Context, req map[string]any, body []byte) (bool, string) {
	if !common.ResponseCacheEnabled || c.Request.Method != http.MethodPost {
		return false, ""
	}
	if len(body) == 0 || len(body) > common.ResponseCacheMaxBodyBytes {
		return false, ""
	}
	path := c.Request.URL.Path
	if !(strings.HasPrefix(path, "/v1/chat/completions") ||
		strings.HasPrefix(path, "/v1/completions") ||
		strings.HasPrefix(path, "/v1/responses") ||
		strings.HasPrefix(path, "/v1/embeddings") ||
		strings.HasPrefix(path, "/v1/images/generations")) {
		return false, ""
	}
	model, _ := req["model"].(string)
	temp, _ := req["temperature"].(float64)
	_, hasSeed := req["seed"]
	if !common.ShouldCacheRelayModel(model, temp, hasSeed) {
		return false, ""
	}
	return true, model
}

type captureWriter struct {
	gin.ResponseWriter
	body bytes.Buffer
}

func (w *captureWriter) Write(data []byte) (int, error) {
	_, _ = w.body.Write(data)
	return w.ResponseWriter.Write(data)
}

func (w *captureWriter) WriteString(s string) (int, error) {
	_, _ = w.body.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}

// ReadFrom captures fast-path io.Copy writes that bypass Write.
func (w *captureWriter) ReadFrom(r io.Reader) (int64, error) {
	tr := io.TeeReader(r, &w.body)
	return io.Copy(w.ResponseWriter, tr)
}

type streamCaptureWriter struct {
	gin.ResponseWriter
	buf bytes.Buffer
}

func (w *streamCaptureWriter) Write(data []byte) (int, error) {
	_, _ = w.buf.Write(data)
	return w.ResponseWriter.Write(data)
}

func (w *streamCaptureWriter) WriteString(s string) (int, error) {
	_, _ = w.buf.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}

// ReadFrom captures fast-path io.Copy writes that bypass Write.
func (w *streamCaptureWriter) ReadFrom(r io.Reader) (int64, error) {
	tr := io.TeeReader(r, &w.buf)
	return io.Copy(w.ResponseWriter, tr)
}

func returnCachedResponse(c *gin.Context, data []byte, isStream bool) {
	if !isStream {
		c.Data(http.StatusOK, "application/json", data)
		return
	}
	returnCachedAsStream(c, data)
}

func returnCachedAsStream(c *gin.Context, data []byte) {
	var resp map[string]any
	if err := common.Unmarshal(data, &resp); err != nil {
		c.Data(http.StatusOK, "application/json", data)
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	w := c.Writer
	if _, hasContent := resp["content"]; hasContent {
		writeAnthropicStream(w, resp)
	} else if _, hasChoices := resp["choices"]; hasChoices {
		writeOpenAIStream(w, resp)
	} else {
		c.Data(http.StatusOK, "application/json", data)
		return
	}
	w.Flush()
}

func writeOpenAIStream(w gin.ResponseWriter, resp map[string]any) {
	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		return
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	content, _ := message["content"].(string)
	role, _ := message["role"].(string)
	if role == "" {
		role = "assistant"
	}
	id, _ := resp["id"].(string)
	model, _ := resp["model"].(string)

	writeSSE(w, map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]string{"role": role},
			"finish_reason": nil,
		}},
	})
	writeSSE(w, map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]string{"content": content},
			"finish_reason": nil,
		}},
	})
	finishReason := "stop"
	if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
		finishReason = fr
	}
	writeSSE(w, map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]string{},
			"finish_reason": finishReason,
		}},
	})
	if usage, ok := resp["usage"]; ok {
		writeSSE(w, map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []any{},
			"usage":   usage,
		})
	}
	_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
}

func writeAnthropicStream(w gin.ResponseWriter, resp map[string]any) {
	writeSSE(w, map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":          resp["id"],
			"type":        "message",
			"role":        "assistant",
			"content":     []any{},
			"model":       resp["model"],
			"stop_reason": nil,
			"usage":       resp["usage"],
		},
	})
	content, _ := resp["content"].([]any)
	for idx, block := range content {
		blockMap, _ := block.(map[string]any)
		if blockMap["type"] != "text" {
			continue
		}
		text, _ := blockMap["text"].(string)
		writeSSE(w, map[string]any{
			"type":          "content_block_start",
			"index":         idx,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
		writeSSE(w, map[string]any{
			"type":  "content_block_delta",
			"index": idx,
			"delta": map[string]any{"type": "text_delta", "text": text},
		})
		writeSSE(w, map[string]any{
			"type":  "content_block_stop",
			"index": idx,
		})
	}
	writeSSE(w, map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": resp["stop_reason"]},
		"usage": map[string]any{"output_tokens": 0},
	})
	writeSSE(w, map[string]any{"type": "message_stop"})
}

func writeSSE(w gin.ResponseWriter, data any) {
	jsonData, err := common.Marshal(data)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", getEventType(data), string(jsonData))
	w.Flush()
}

func getEventType(data any) string {
	m, ok := data.(map[string]any)
	if !ok {
		return "message"
	}
	t, ok := m["type"].(string)
	if !ok || t == "" {
		return "message"
	}
	return t
}

func rebuildFromSSE(sseData []byte) map[string]any {
	lines := strings.Split(string(sseData), "\n")
	var fullContent strings.Builder
	var respID, respModel, finishReason string
	var usage map[string]any
	isAnthropic := false

	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var event map[string]any
		if err := common.UnmarshalJsonStr(data, &event); err != nil {
			continue
		}
		if eventType, ok := event["type"].(string); ok && eventType != "" {
			isAnthropic = true
			switch eventType {
			case "message_start":
				if msg, ok := event["message"].(map[string]any); ok {
					respID, _ = msg["id"].(string)
					respModel, _ = msg["model"].(string)
					if u, ok := msg["usage"].(map[string]any); ok {
						usage = u
					}
				}
			case "content_block_delta":
				if delta, ok := event["delta"].(map[string]any); ok {
					if text, ok := delta["text"].(string); ok {
						fullContent.WriteString(text)
					}
				}
			case "message_delta":
				if delta, ok := event["delta"].(map[string]any); ok {
					finishReason, _ = delta["stop_reason"].(string)
				}
				if u, ok := event["usage"].(map[string]any); ok {
					if usage == nil {
						usage = u
					} else {
						for k, v := range u {
							usage[k] = v
						}
					}
				}
			}
			continue
		}

		if id, ok := event["id"].(string); ok && id != "" {
			respID = id
		}
		if m, ok := event["model"].(string); ok && m != "" {
			respModel = m
		}
		if choices, ok := event["choices"].([]any); ok {
			for _, ch := range choices {
				choice, _ := ch.(map[string]any)
				if delta, ok := choice["delta"].(map[string]any); ok {
					if content, ok := delta["content"].(string); ok {
						fullContent.WriteString(content)
					}
				}
				if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
					finishReason = fr
				}
			}
		}
		if u, ok := event["usage"].(map[string]any); ok {
			usage = u
		}
	}

	if fullContent.Len() == 0 {
		return nil
	}
	if isAnthropic {
		return map[string]any{
			"id":    respID,
			"type":  "message",
			"role":  "assistant",
			"model": respModel,
			"content": []map[string]any{{
				"type": "text",
				"text": fullContent.String(),
			}},
			"stop_reason": finishReason,
			"usage":       usage,
		}
	}
	return map[string]any{
		"id":      respID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   respModel,
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": fullContent.String(),
			},
			"finish_reason": finishReason,
		}},
		"usage": usage,
	}
}

// ResponseCache performs L1 exact-match response caching for relay requests.
func ResponseCache() gin.HandlerFunc {
	return func(c *gin.Context) {
		bodyStorage, err := common.GetBodyStorage(c)
		if err != nil {
			c.Next()
			return
		}
		requestBody, err := bodyStorage.Bytes()
		if err != nil {
			c.Next()
			return
		}

		var reqMap map[string]any
		if err = common.Unmarshal(requestBody, &reqMap); err != nil {
			c.Next()
			return
		}

		cacheable, model := shouldCacheRelayRequest(c, reqMap, requestBody)
		if !cacheable {
			c.Next()
			return
		}
		isStream, _ := reqMap["stream"].(bool)
		userID := c.GetInt("id")
		if system, ok := reqMap["system"]; ok {
			service.CheckSystemChange(model, system)
		}

		var prefixInfo *service.PrefixInfo
		if common.IsImageModel(model) || common.IsEmbeddingModel(model) {
			prefixInfo = nil
		} else if msgRaw, ok := reqMap["messages"].([]any); ok {
			prefixInfo = service.TrackPrefix(userID, model, reqMap["system"], msgRaw)
		}

		if prefixInfo != nil && prefixInfo.IsContinuation {
			if deltaData, deltaHit := service.CheckL2DeltaCache(prefixInfo.UserID, model, prefixInfo.PrefixHash, prefixInfo.DeltaHash); deltaHit {
				c.Header("X-Cache", "HIT-L2-DELTA")
				logger.LogInfo(c, fmt.Sprintf("response cache hit: layer=L2 upstream_cost=0 model=%s", model))
				service.ConsumeCacheHitQuota(c, "l2:"+prefixInfo.PrefixHash+":"+prefixInfo.DeltaHash, model, deltaData)
				returnCachedResponse(c, deltaData, isStream)
				c.Abort()
				return
			}
		}

		cacheKey := service.BuildResponseCacheKey(requestBody, model, c.Request.URL.Path)
		if hit, getErr := service.GetCachedRelayResponse(c, cacheKey); getErr == nil && hit != nil {
			service.RecordCacheHit(cacheKey, model)
			for k, vals := range hit.Headers {
				if strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Transfer-Encoding") {
					continue
				}
				for _, v := range vals {
					c.Writer.Header().Add(k, v)
				}
			}
			c.Header("X-Cache", "HIT-L1")
			logger.LogInfo(c, fmt.Sprintf("response cache hit: layer=L1 upstream_cost=0 model=%s", model))
			service.ConsumeCacheHitQuota(c, cacheKey, model, hit.Body)
			returnCachedResponse(c, hit.Body, isStream)
			c.Abort()
			return
		}

		c.Header("X-Cache", "MISS")
		if isStream {
			recorder := &streamCaptureWriter{ResponseWriter: c.Writer}
			c.Writer = recorder
			c.Next()
			if _, seekErr := bodyStorage.Seek(0, io.SeekStart); seekErr == nil {
				c.Request.Body = io.NopCloser(bodyStorage)
			}
			if c.Writer.Status() >= 200 && c.Writer.Status() < 300 && recorder.buf.Len() > 0 {
				if rebuilt := rebuildFromSSE(recorder.buf.Bytes()); rebuilt != nil {
					if raw, err := common.Marshal(rebuilt); err == nil {
						service.SetCachedRelayResponse(c, cacheKey, model, c.Writer.Status(), map[string][]string{"Content-Type": {"application/json"}}, raw)
						channelType := c.GetString("channel_type")
						if channelType == "" {
							channelType = "official"
						}
						service.UpdateCacheMeta(cacheKey, model, channelType, 0, 0)
						if prefixInfo != nil && prefixInfo.IsContinuation {
							service.SetL2DeltaCache(prefixInfo.UserID, model, prefixInfo.PrefixHash, prefixInfo.DeltaHash, raw)
						}
					}
				}
			}
			return
		}

		recorder := &captureWriter{ResponseWriter: c.Writer}
		c.Writer = recorder
		c.Next()

		// restore body pointer for downstream retries after middleware chain.
		if _, seekErr := bodyStorage.Seek(0, io.SeekStart); seekErr == nil {
			c.Request.Body = io.NopCloser(bodyStorage)
		}

		if c.Writer.Status() >= 200 && c.Writer.Status() < 300 && recorder.body.Len() > 0 {
			headersCopy := make(map[string][]string, len(c.Writer.Header()))
			for k, vals := range c.Writer.Header() {
				if strings.EqualFold(k, "X-Cache") {
					continue
				}
				cp := make([]string, len(vals))
				copy(cp, vals)
				headersCopy[k] = cp
			}
			service.SetCachedRelayResponse(c, cacheKey, model, c.Writer.Status(), headersCopy, recorder.body.Bytes())
			channelType := c.GetString("channel_type")
			if channelType == "" {
				channelType = "official"
			}
			service.UpdateCacheMeta(cacheKey, model, channelType, 0, 0)
			if prefixInfo != nil && prefixInfo.IsContinuation {
				service.SetL2DeltaCache(prefixInfo.UserID, model, prefixInfo.PrefixHash, prefixInfo.DeltaHash, recorder.body.Bytes())
			}
		}
	}
}
