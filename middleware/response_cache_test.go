package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestShouldCacheRelayRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		method       string
		path         string
		body         []byte
		req          map[string]any
		expectCache  bool
		expectModel  string
		cacheEnabled bool
	}{
		{
			name:         "chat completions cacheable",
			method:       "POST",
			path:         "/v1/chat/completions",
			body:         []byte(`{"model":"gpt-4o-mini","temperature":0.2}`),
			req:          map[string]any{"model": "gpt-4o-mini", "temperature": 0.2},
			expectCache:  true,
			expectModel:  "gpt-4o-mini",
			cacheEnabled: true,
		},
		{
			name:         "stream request cacheable",
			method:       "POST",
			path:         "/v1/chat/completions",
			body:         []byte(`{"model":"gpt-4o-mini","stream":true}`),
			req:          map[string]any{"model": "gpt-4o-mini", "stream": true},
			expectCache:  true,
			expectModel:  "gpt-4o-mini",
			cacheEnabled: true,
		},
		{
			name:         "high temperature non embedding not cacheable",
			method:       "POST",
			path:         "/v1/chat/completions",
			body:         []byte(`{"model":"gpt-4o-mini","temperature":0.9}`),
			req:          map[string]any{"model": "gpt-4o-mini", "temperature": 0.9},
			expectCache:  false,
			cacheEnabled: true,
		},
		{
			name:         "embedding high temperature still cacheable",
			method:       "POST",
			path:         "/v1/embeddings",
			body:         []byte(`{"model":"text-embedding-3-large","temperature":0.9}`),
			req:          map[string]any{"model": "text-embedding-3-large", "temperature": 0.9},
			expectCache:  true,
			expectModel:  "text-embedding-3-large",
			cacheEnabled: true,
		},
		{
			name:         "unsupported path not cacheable",
			method:       "POST",
			path:         "/v1/audio/speech",
			body:         []byte(`{"model":"gpt-4o-mini"}`),
			req:          map[string]any{"model": "gpt-4o-mini"},
			expectCache:  false,
			cacheEnabled: true,
		},
		{
			name:         "image generations with seed cacheable",
			method:       "POST",
			path:         "/v1/images/generations",
			body:         []byte(`{"model":"dall-e-3","prompt":"cat","seed":1}`),
			req:          map[string]any{"model": "dall-e-3", "seed": 1.0},
			expectCache:  true,
			expectModel:  "dall-e-3",
			cacheEnabled: true,
		},
		{
			name:         "image generations without seed not cacheable",
			method:       "POST",
			path:         "/v1/images/generations",
			body:         []byte(`{"model":"dall-e-3","prompt":"cat"}`),
			req:          map[string]any{"model": "dall-e-3"},
			expectCache:  false,
			cacheEnabled: true,
		},
		{
			name:         "cache disabled not cacheable",
			method:       "POST",
			path:         "/v1/chat/completions",
			body:         []byte(`{"model":"gpt-4o-mini"}`),
			req:          map[string]any{"model": "gpt-4o-mini"},
			expectCache:  false,
			cacheEnabled: false,
		},
		{
			name:         "only post cacheable",
			method:       "GET",
			path:         "/v1/chat/completions",
			body:         []byte(`{"model":"gpt-4o-mini"}`),
			req:          map[string]any{"model": "gpt-4o-mini"},
			expectCache:  false,
			cacheEnabled: true,
		},
	}

	originalEnabled := common.ResponseCacheEnabled
	originalMaxBodyBytes := common.ResponseCacheMaxBodyBytes
	defer func() {
		common.ResponseCacheEnabled = originalEnabled
		common.ResponseCacheMaxBodyBytes = originalMaxBodyBytes
	}()

	common.ResponseCacheMaxBodyBytes = 1024

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			common.ResponseCacheEnabled = tt.cacheEnabled

			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(tt.method, tt.path, nil)

			cacheable, model := shouldCacheRelayRequest(ctx, tt.req, tt.body)
			require.Equal(t, tt.expectCache, cacheable)
			require.Equal(t, tt.expectModel, model)
		})
	}
}
