package otel

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// mockSpanProcessor is a test double that records spans passed to OnEnd.
type mockSpanProcessor struct {
	mu    sync.Mutex
	spans []sdktrace.ReadOnlySpan
}

func (m *mockSpanProcessor) OnStart(ctx context.Context, s sdktrace.ReadWriteSpan) {}

func (m *mockSpanProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spans = append(m.spans, s)
}

func (m *mockSpanProcessor) Shutdown(ctx context.Context) error { return nil }

func (m *mockSpanProcessor) ForceFlush(ctx context.Context) error { return nil }

func (m *mockSpanProcessor) getSpans() []sdktrace.ReadOnlySpan {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]sdktrace.ReadOnlySpan, len(m.spans))
	copy(result, m.spans)
	return result
}

// createTestSpan creates a span with the given name and ends it immediately.
func createTestSpan(tp *sdktrace.TracerProvider, name string) {
	_, span := tp.Tracer("test").Start(context.Background(), name)
	span.End()
}

func TestSpanProcessor_FilterByName(t *testing.T) {
	mock := &mockSpanProcessor{}
	processor := NewSpanProcessor(mock, SpanProcessorConfig{
		FilterSpanNames: []string{"ServeHTTP", "ignored"},
	})

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(processor),
	)

	// Create spans
	createTestSpan(tp, "ServeHTTP")       // filtered
	createTestSpan(tp, "MyHandler")       // kept
	createTestSpan(tp, "ignored")         // filtered
	createTestSpan(tp, "/api/v1/users")   // kept

	spans := mock.getSpans()
	if len(spans) != 2 {
		t.Errorf("expected 2 spans, got %d", len(spans))
	}

	names := make(map[string]bool)
	for _, s := range spans {
		names[s.Name()] = true
	}
	if names["ServeHTTP"] || names["ignored"] {
		t.Error("filtered spans should not appear")
	}
	if !names["MyHandler"] || !names["/api/v1/users"] {
		t.Error("expected spans missing")
	}
}

func TestSpanProcessor_ShortGRPCFormat(t *testing.T) {
	mock := &mockSpanProcessor{}
	processor := NewSpanProcessor(mock, SpanProcessorConfig{
		GRPCSpanNameFormat: "short",
	})

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(processor),
	)

	// gRPC-style span names
	createTestSpan(tp, "/pkg.Service/V0GetStats")
	createTestSpan(tp, "/another.pkg.Svc/DoThing")
	createTestSpan(tp, "NotGRPC") // should remain unchanged

	spans := mock.getSpans()
	if len(spans) != 3 {
		t.Fatalf("expected 3 spans, got %d", len(spans))
	}

	names := make(map[string]bool)
	for _, s := range spans {
		names[s.Name()] = true
	}

	// Short format extracts method name
	if !names["V0GetStats"] {
		t.Error("expected V0GetStats")
	}
	if !names["DoThing"] {
		t.Error("expected DoThing")
	}
	if !names["NotGRPC"] {
		t.Error("expected NotGRPC unchanged")
	}
}

func TestSpanProcessor_FullGRPCFormat(t *testing.T) {
	mock := &mockSpanProcessor{}
	processor := NewSpanProcessor(mock, SpanProcessorConfig{
		GRPCSpanNameFormat: "full",
	})

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(processor),
	)

	createTestSpan(tp, "/pkg.Service/V0GetStats")

	spans := mock.getSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name() != "/pkg.Service/V0GetStats" {
		t.Errorf("expected full name, got %s", spans[0].Name())
	}
}

func TestSpanProcessor_CustomFilter(t *testing.T) {
	mock := &mockSpanProcessor{}
	processor := NewSpanProcessor(mock, SpanProcessorConfig{})

	// Add custom filter that drops spans with "debug" in name
	processor.AddFilter(SpanFilterFunc(func(s sdktrace.ReadOnlySpan) bool {
		return s.Name() == "debug-span"
	}))

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(processor),
	)

	createTestSpan(tp, "debug-span") // filtered by custom filter
	createTestSpan(tp, "normal-span")

	spans := mock.getSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name() != "normal-span" {
		t.Errorf("expected normal-span, got %s", spans[0].Name())
	}
}

func TestSpanProcessor_CustomTransformer(t *testing.T) {
	mock := &mockSpanProcessor{}
	processor := NewSpanProcessor(mock, SpanProcessorConfig{})

	// Add custom transformer that prefixes names
	processor.AddTransformer(SpanTransformerFunc(func(s sdktrace.ReadOnlySpan) string {
		if s.Name() == "transform-me" {
			return "prefix:" + s.Name()
		}
		return "" // no transformation
	}))

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(processor),
	)

	createTestSpan(tp, "transform-me")
	createTestSpan(tp, "leave-alone")

	spans := mock.getSpans()
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}

	names := make(map[string]bool)
	for _, s := range spans {
		names[s.Name()] = true
	}

	if !names["prefix:transform-me"] {
		t.Error("expected transformed name prefix:transform-me")
	}
	if !names["leave-alone"] {
		t.Error("expected leave-alone unchanged")
	}
}

func TestSpanProcessor_TransformerOverridesShortFormat(t *testing.T) {
	mock := &mockSpanProcessor{}
	processor := NewSpanProcessor(mock, SpanProcessorConfig{
		GRPCSpanNameFormat: "short",
	})

	// Custom transformer wins over built-in short format
	processor.AddTransformer(SpanTransformerFunc(func(s sdktrace.ReadOnlySpan) string {
		return "custom-name"
	}))

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(processor),
	)

	createTestSpan(tp, "/pkg.Service/Method")

	spans := mock.getSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name() != "custom-name" {
		t.Errorf("expected custom-name, got %s", spans[0].Name())
	}
}

func TestSpanProcessor_ConcurrentAddFilter(t *testing.T) {
	mock := &mockSpanProcessor{}
	processor := NewSpanProcessor(mock, SpanProcessorConfig{})

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(processor),
	)

	var wg sync.WaitGroup
	// Concurrent filter additions
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			processor.AddFilter(SpanFilterFunc(func(s sdktrace.ReadOnlySpan) bool {
				return false
			}))
		}()
	}

	// Concurrent span creation
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			createTestSpan(tp, "span-"+string(rune('a'+n)))
		}(i)
	}

	wg.Wait()
	// Just verify no race/panic
}

func TestSpanProcessor_ForceFlushAndShutdown(t *testing.T) {
	mock := &mockSpanProcessor{}
	processor := NewSpanProcessor(mock, SpanProcessorConfig{})

	ctx := context.Background()
	if err := processor.ForceFlush(ctx); err != nil {
		t.Errorf("ForceFlush failed: %v", err)
	}
	if err := processor.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown failed: %v", err)
	}
}

func TestRenamedSpan_PreservesOtherFields(t *testing.T) {
	// Create a real span to wrap
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
	)

	ctx, span := tp.Tracer("test").Start(context.Background(), "original-name",
		trace.WithAttributes(attribute.String("key", "value")),
	)
	span.SetStatus(codes.Error, "test error")
	span.End()
	_ = ctx

	// Get the exported span
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}

	// Wrap it with renamedSpan
	original := spans[0].Snapshot()
	renamed := &renamedSpan{ReadOnlySpan: original, name: "new-name"}

	// Verify name is changed
	if renamed.Name() != "new-name" {
		t.Errorf("expected new-name, got %s", renamed.Name())
	}

	// Verify other fields preserved
	if renamed.SpanContext().TraceID() != original.SpanContext().TraceID() {
		t.Error("SpanContext TraceID not preserved")
	}
	if renamed.SpanContext().SpanID() != original.SpanContext().SpanID() {
		t.Error("SpanContext SpanID not preserved")
	}
	if renamed.StartTime() != original.StartTime() {
		t.Error("StartTime not preserved")
	}
	if renamed.EndTime() != original.EndTime() {
		t.Error("EndTime not preserved")
	}
	if renamed.Status().Code != original.Status().Code {
		t.Error("Status not preserved")
	}
}

func TestSpanProcessor_EmptyFilterNames(t *testing.T) {
	mock := &mockSpanProcessor{}
	processor := NewSpanProcessor(mock, SpanProcessorConfig{
		FilterSpanNames: []string{}, // empty list
	})

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(processor),
	)

	createTestSpan(tp, "any-span")

	spans := mock.getSpans()
	if len(spans) != 1 {
		t.Errorf("expected 1 span with empty filter list, got %d", len(spans))
	}
}

func TestSpanProcessor_MultipleFiltersAllChecked(t *testing.T) {
	mock := &mockSpanProcessor{}
	processor := NewSpanProcessor(mock, SpanProcessorConfig{
		FilterSpanNames: []string{"first"},
	})

	// Add another filter
	processor.AddFilter(SpanFilterFunc(func(s sdktrace.ReadOnlySpan) bool {
		return s.Name() == "second"
	}))

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(processor),
	)

	createTestSpan(tp, "first")  // filtered by name list
	createTestSpan(tp, "second") // filtered by custom filter
	createTestSpan(tp, "third")  // kept

	spans := mock.getSpans()
	if len(spans) != 1 {
		t.Errorf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name() != "third" {
		t.Errorf("expected third, got %s", spans[0].Name())
	}
}

// TestSpanFilterFunc verifies the function adapter implements the interface.
func TestSpanFilterFunc_Interface(t *testing.T) {
	var f SpanFilter = SpanFilterFunc(func(s sdktrace.ReadOnlySpan) bool {
		return true
	})
	_ = f // compile-time check
}

// TestSpanTransformerFunc verifies the function adapter implements the interface.
func TestSpanTransformerFunc_Interface(t *testing.T) {
	var f SpanTransformer = SpanTransformerFunc(func(s sdktrace.ReadOnlySpan) string {
		return "transformed"
	})
	_ = f // compile-time check
}

// TestSpanProcessor_FilterDurationBased demonstrates filtering spans by duration.
func TestSpanProcessor_FilterDurationBased(t *testing.T) {
	mock := &mockSpanProcessor{}
	processor := NewSpanProcessor(mock, SpanProcessorConfig{})

	// Filter spans shorter than 10ms
	processor.AddFilter(SpanFilterFunc(func(s sdktrace.ReadOnlySpan) bool {
		return s.EndTime().Sub(s.StartTime()) < 10*time.Millisecond
	}))

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(processor),
	)

	// Create a quick span (should be filtered)
	_, span1 := tp.Tracer("test").Start(context.Background(), "quick")
	span1.End()

	// Create a slower span (should be kept)
	_, span2 := tp.Tracer("test").Start(context.Background(), "slow")
	time.Sleep(15 * time.Millisecond)
	span2.End()

	spans := mock.getSpans()
	if len(spans) != 1 {
		t.Errorf("expected 1 span (slow), got %d", len(spans))
	}
	if len(spans) > 0 && spans[0].Name() != "slow" {
		t.Errorf("expected slow span, got %s", spans[0].Name())
	}
}

func TestSpanProcessor_NilFilterIgnored(t *testing.T) {
	mock := &mockSpanProcessor{}
	processor := NewSpanProcessor(mock, SpanProcessorConfig{})

	// Adding nil filter should be ignored (no panic)
	processor.AddFilter(nil)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(processor),
	)

	createTestSpan(tp, "test-span")

	spans := mock.getSpans()
	if len(spans) != 1 {
		t.Errorf("expected 1 span, got %d", len(spans))
	}
}

func TestSpanProcessor_NilTransformerIgnored(t *testing.T) {
	mock := &mockSpanProcessor{}
	processor := NewSpanProcessor(mock, SpanProcessorConfig{})

	// Adding nil transformer should be ignored (no panic)
	processor.AddTransformer(nil)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(processor),
	)

	createTestSpan(tp, "test-span")

	spans := mock.getSpans()
	if len(spans) != 1 {
		t.Errorf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name() != "test-span" {
		t.Errorf("expected test-span, got %s", spans[0].Name())
	}
}
