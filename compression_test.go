package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-coldbrew/core/config"
)

func TestNewHTTPCompressionWrapper_NegotiatesEncoding(t *testing.T) {
	body := strings.Repeat("payload-", 256) // ~2KiB, well above the 256-byte default

	cases := []struct {
		name           string
		cfg            config.Config
		acceptEncoding string
		wantEncoding   string
	}{
		{
			name:           "zstd-preferred-default",
			cfg:            config.Config{HTTPCompressionMinSize: 256, PreferZstd: true},
			acceptEncoding: "gzip, zstd",
			wantEncoding:   "zstd",
		},
		{
			name:           "client-only-gzip",
			cfg:            config.Config{HTTPCompressionMinSize: 256, PreferZstd: true},
			acceptEncoding: "gzip",
			wantEncoding:   "gzip",
		},
		{
			name:           "zstd-disabled-falls-back-to-gzip",
			cfg:            config.Config{HTTPCompressionMinSize: 256, DisableZstdCompression: true, PreferZstd: true},
			acceptEncoding: "gzip, zstd",
			wantEncoding:   "gzip",
		},
		{
			name:           "no-accept-encoding-no-compression",
			cfg:            config.Config{HTTPCompressionMinSize: 256, PreferZstd: true},
			acceptEncoding: "",
			wantEncoding:   "",
		},
		{
			name:           "prefer-zstd-false-picks-gzip-when-equal-q",
			cfg:            config.Config{HTTPCompressionMinSize: 256, PreferZstd: false},
			acceptEncoding: "gzip, zstd",
			wantEncoding:   "gzip",
		},
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(body))
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrapper, err := newHTTPCompressionWrapper(tc.cfg)
			if err != nil {
				t.Fatalf("newHTTPCompressionWrapper: %v", err)
			}
			wrapped := wrapper(handler)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.acceptEncoding != "" {
				req.Header.Set("Accept-Encoding", tc.acceptEncoding)
			}
			rec := httptest.NewRecorder()
			wrapped.ServeHTTP(rec, req)

			got := rec.Header().Get("Content-Encoding")
			if got != tc.wantEncoding {
				t.Fatalf("Content-Encoding = %q, want %q", got, tc.wantEncoding)
			}
		})
	}
}

func TestNewHTTPCompressionWrapper_ExcludesEventStream(t *testing.T) {
	// SSE responses must never be compressed: intermediaries (proxies, CDNs)
	// buffer compressed event streams, which defeats real-time delivery for
	// EventSource clients consuming streaming gateway RPCs.
	cfg := config.Config{HTTPCompressionMinSize: 256, PreferZstd: true}
	wrapper, err := newHTTPCompressionWrapper(cfg)
	if err != nil {
		t.Fatalf("newHTTPCompressionWrapper: %v", err)
	}

	body := strings.Repeat("data: payload\n\n", 256) // well above MinSize

	cases := []struct {
		name        string
		contentType string
		wantEncoded bool
	}{
		{"plain-text-still-compresses", "text/plain", true},
		{"sse-bare", "text/event-stream", false},
		{"sse-with-charset", "text/event-stream; charset=utf-8", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = w.Write([]byte(body))
			})
			wrapped := wrapper(handler)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Accept-Encoding", "gzip, zstd")
			rec := httptest.NewRecorder()
			wrapped.ServeHTTP(rec, req)

			encoding := rec.Header().Get("Content-Encoding")
			if tc.wantEncoded && encoding == "" {
				t.Fatalf("Content-Type %q: expected compression, got none", tc.contentType)
			}
			if !tc.wantEncoded && encoding != "" {
				t.Fatalf("Content-Type %q: expected no compression, got %q", tc.contentType, encoding)
			}
		})
	}
}

func TestNewHTTPCompressionWrapper_BelowMinSize(t *testing.T) {
	cfg := config.Config{HTTPCompressionMinSize: 256, PreferZstd: true}
	wrapper, err := newHTTPCompressionWrapper(cfg)
	if err != nil {
		t.Fatalf("newHTTPCompressionWrapper: %v", err)
	}

	wrapped := wrapper(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("tiny"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip, zstd")
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty (body below MinSize)", got)
	}
}
