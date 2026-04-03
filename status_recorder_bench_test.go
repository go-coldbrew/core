package core

import (
	"net/http"
	"sync"
	"testing"
)

// noopResponseWriter is a minimal ResponseWriter that discards output,
// isolating statusRecorder overhead from buffer growth.
type noopResponseWriter struct{}

func (noopResponseWriter) Header() http.Header        { return http.Header{} }
func (noopResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (noopResponseWriter) WriteHeader(int)             {}

// sink prevents compiler from optimizing away allocations.
var sink http.ResponseWriter

func BenchmarkStatusRecorder_Direct(b *testing.B) {
	w := noopResponseWriter{}
	body := []byte("hello")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		sink = rec // force heap escape
		rec.WriteHeader(http.StatusOK)
		rec.Write(body)
	}
}

func BenchmarkStatusRecorder_Pool(b *testing.B) {
	pool := sync.Pool{
		New: func() any {
			return &statusRecorder{}
		},
	}
	w := noopResponseWriter{}
	body := []byte("hello")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		rec := pool.Get().(*statusRecorder)
		rec.ResponseWriter = w
		rec.status = http.StatusOK
		rec.wroteHeader = false
		sink = rec // force heap escape
		rec.WriteHeader(http.StatusOK)
		rec.Write(body)
		rec.ResponseWriter = nil
		pool.Put(rec)
	}
}
