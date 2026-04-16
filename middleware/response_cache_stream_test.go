package middleware

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestT11_OpenAIStreamReplayFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)

	resp := map[string]any{
		"id":    "chatcmpl-t11",
		"model": "gpt-4o-mini",
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "hello from cache",
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     10,
			"completion_tokens": 4,
		},
	}
	raw, err := common.Marshal(resp)
	require.NoError(t, err)

	returnCachedAsStream(ctx, raw)

	body := rec.Body.String()
	require.Contains(t, body, "data: [DONE]")
	require.Contains(t, body, "\"object\":\"chat.completion.chunk\"")
	require.Contains(t, body, "\"content\":\"hello from cache\"")

	rebuilt := rebuildFromSSE([]byte(body))
	require.NotNil(t, rebuilt)
	choices, ok := rebuilt["choices"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, choices, 1)
	message, ok := choices[0]["message"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "hello from cache", message["content"])
}

func TestT12_AnthropicStreamReplayEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)

	resp := map[string]any{
		"id":    "msg-t12",
		"model": "claude-sonnet-4-6",
		"content": []map[string]any{
			{"type": "text", "text": "anthropic cached text"},
		},
		"stop_reason": "end_turn",
		"usage": map[string]any{
			"input_tokens":  100,
			"output_tokens": 20,
		},
	}
	raw, err := common.Marshal(resp)
	require.NoError(t, err)

	returnCachedAsStream(ctx, raw)

	body := rec.Body.String()
	require.Contains(t, body, "event: message_start")
	require.Contains(t, body, "event: content_block_delta")
	require.Contains(t, body, "event: message_stop")

	rebuilt := rebuildFromSSE([]byte(body))
	require.NotNil(t, rebuilt)
	content, ok := rebuilt["content"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	require.Equal(t, "anthropic cached text", content[0]["text"])
}

func TestT13_LargeStreamReplayNoLoss(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)

	largeContent := strings.Repeat("0123456789ABCDEF", 800) // 12.8KB
	resp := map[string]any{
		"id":    "chatcmpl-t13",
		"model": "gpt-4o-mini",
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": largeContent,
				},
				"finish_reason": "stop",
			},
		},
	}
	raw, err := common.Marshal(resp)
	require.NoError(t, err)

	returnCachedAsStream(ctx, raw)
	rebuilt := rebuildFromSSE(rec.Body.Bytes())
	require.NotNil(t, rebuilt)

	choices, ok := rebuilt["choices"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, choices, 1)
	message, ok := choices[0]["message"].(map[string]any)
	require.True(t, ok)
	rebuiltContent, ok := message["content"].(string)
	require.True(t, ok)
	require.Equal(t, len(largeContent), len(rebuiltContent))
	require.Equal(t, largeContent, rebuiltContent)
}

