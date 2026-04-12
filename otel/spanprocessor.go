// Package otel provides OpenTelemetry utilities for ColdBrew services.
package otel

import (
	"context"
	"strings"
	"sync"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SpanFilter allows clients to implement custom span filtering logic.
// Return true to drop the span (not export it), false to keep it.
type SpanFilter interface {
	// ShouldDrop returns true if the span should be filtered out.
	ShouldDrop(span sdktrace.ReadOnlySpan) bool
}

// SpanFilterFunc is a function adapter for SpanFilter interface.
type SpanFilterFunc func(span sdktrace.ReadOnlySpan) bool

// ShouldDrop implements SpanFilter.
func (f SpanFilterFunc) ShouldDrop(span sdktrace.ReadOnlySpan) bool {
	return f(span)
}

// SpanTransformer allows clients to transform span data before export.
// This is useful for renaming spans, adding attributes, etc.
type SpanTransformer interface {
	// Transform returns a modified span name. Return empty string to keep original.
	Transform(span sdktrace.ReadOnlySpan) string
}

// SpanTransformerFunc is a function adapter for SpanTransformer interface.
type SpanTransformerFunc func(span sdktrace.ReadOnlySpan) string

// Transform implements SpanTransformer.
func (f SpanTransformerFunc) Transform(span sdktrace.ReadOnlySpan) string {
	return f(span)
}

// SpanProcessorConfig configures the custom span processor.
type SpanProcessorConfig struct {
	// GRPCSpanNameFormat controls gRPC span naming.
	// "short" extracts just the method name (e.g., "V0GetStats")
	// "full" keeps the full path (e.g., "/pkg.Service/V0GetStats") - default
	GRPCSpanNameFormat string

	// FilterSpanNames is a list of span names to filter out (exact match).
	// Common use: []string{"ServeHTTP"} to filter HTTP transport spans.
	FilterSpanNames []string

	// Filters are custom filters provided by the client.
	// All filters are checked; if any returns true, the span is dropped.
	Filters []SpanFilter

	// Transformers are custom transformers provided by the client.
	// Applied in order; first non-empty result wins.
	Transformers []SpanTransformer
}

// SpanProcessor wraps a SpanProcessor with filtering and transformation.
// It implements sdktrace.SpanProcessor.
type SpanProcessor struct {
	next        sdktrace.SpanProcessor
	config      SpanProcessorConfig
	filterNames map[string]struct{}
	mu          sync.RWMutex
}

// NewSpanProcessor creates a new SpanProcessor wrapping the given processor.
func NewSpanProcessor(next sdktrace.SpanProcessor, config SpanProcessorConfig) *SpanProcessor {
	filterNames := make(map[string]struct{}, len(config.FilterSpanNames))
	for _, name := range config.FilterSpanNames {
		filterNames[name] = struct{}{}
	}
	return &SpanProcessor{
		next:        next,
		config:      config,
		filterNames: filterNames,
	}
}

// OnStart is called when a span starts.
func (p *SpanProcessor) OnStart(parent context.Context, s sdktrace.ReadWriteSpan) {
	p.next.OnStart(parent, s)
}

// OnEnd is called when a span ends. This is where filtering and transformation happen.
func (p *SpanProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	// Check exact name filter
	if _, ok := p.filterNames[s.Name()]; ok {
		return // filtered out
	}

	// Check custom filters (need lock since AddFilter can modify concurrently)
	p.mu.RLock()
	filters := p.config.Filters
	p.mu.RUnlock()
	for _, filter := range filters {
		if filter.ShouldDrop(s) {
			return // filtered out
		}
	}

	// Apply transformations if needed
	// Note: ReadOnlySpan doesn't allow modification, so we use a wrapper
	// that applies name transformation on read.
	transformed := p.maybeTransform(s)

	p.next.OnEnd(transformed)
}

// maybeTransform applies transformations and returns a potentially wrapped span.
func (p *SpanProcessor) maybeTransform(s sdktrace.ReadOnlySpan) sdktrace.ReadOnlySpan {
	newName := ""

	// Apply short gRPC span name format
	if p.config.GRPCSpanNameFormat == "short" {
		name := s.Name()
		// gRPC spans have format "/pkg.Service/Method"
		if strings.HasPrefix(name, "/") && strings.Contains(name, "/") {
			parts := strings.Split(name, "/")
			if len(parts) >= 2 {
				newName = parts[len(parts)-1]
			}
		}
	}

	// Apply custom transformers (first non-empty wins)
	// Need lock since AddTransformer can modify concurrently
	p.mu.RLock()
	transformers := p.config.Transformers
	p.mu.RUnlock()
	for _, t := range transformers {
		if result := t.Transform(s); result != "" {
			newName = result
			break
		}
	}

	if newName != "" && newName != s.Name() {
		return &renamedSpan{ReadOnlySpan: s, name: newName}
	}
	return s
}

// Shutdown shuts down the processor.
func (p *SpanProcessor) Shutdown(ctx context.Context) error {
	return p.next.Shutdown(ctx)
}

// ForceFlush forces a flush of the processor.
func (p *SpanProcessor) ForceFlush(ctx context.Context) error {
	return p.next.ForceFlush(ctx)
}

// AddFilter adds a custom filter at runtime.
// Thread-safe.
func (p *SpanProcessor) AddFilter(f SpanFilter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.config.Filters = append(p.config.Filters, f)
}

// AddTransformer adds a custom transformer at runtime.
// Thread-safe.
func (p *SpanProcessor) AddTransformer(t SpanTransformer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.config.Transformers = append(p.config.Transformers, t)
}

// renamedSpan wraps a ReadOnlySpan to return a different name.
type renamedSpan struct {
	sdktrace.ReadOnlySpan
	name string
}

// Name returns the transformed span name.
func (s *renamedSpan) Name() string {
	return s.name
}
