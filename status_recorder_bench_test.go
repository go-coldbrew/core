package core

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// sink prevents compiler from optimizing away allocations.
var sink http.ResponseWriter

func BenchmarkStatusRecorder_Direct(b *testing.B) {
	w := httptest.NewRecorder()
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
	w := httptest.NewRecorder()
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
