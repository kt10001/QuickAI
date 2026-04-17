package service

import "testing"

func TestNormalizeRedisEndpoint(t *testing.T) {
	tests := []struct {
		in  string
		out string
	}{
		{"127.0.0.1:6379", "127.0.0.1:6379"},
		{"localhost:6379", "127.0.0.1:6379"},
		{"127.0.0.1", "127.0.0.1:6379"},
	}
	for _, tt := range tests {
		if got := normalizeRedisEndpoint(tt.in); got != tt.out {
			t.Fatalf("normalizeRedisEndpoint(%q)=%q want %q", tt.in, got, tt.out)
		}
	}
}
