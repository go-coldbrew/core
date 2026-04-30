package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-coldbrew/core/config"
	"github.com/go-coldbrew/workers"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
)

// resetWorkerRunOpts clears the package global between tests.
func resetWorkerRunOpts(t *testing.T) {
	t.Helper()
	prev := workerRunOpts
	workerRunOpts = nil
	t.Cleanup(func() { workerRunOpts = prev })
}

// recordingMetrics is a forward-compatible workers.Metrics that counts
// WorkerStarted invocations.
type recordingMetrics struct {
	workers.BaseMetrics
	started atomic.Int32
}

func (m *recordingMetrics) WorkerStarted(string) { m.started.Add(1) }

func TestBuildWorkerRunOpts_DefaultPrometheus_AppNameSet(t *testing.T) {
	resetWorkerRunOpts(t)
	c := &cb{config: config.Config{AppName: "test_buildopts_default"}}

	opts := c.buildWorkerRunOpts()
	if len(opts) != 1 {
		t.Fatalf("expected one default option, got %d", len(opts))
	}
}

func TestBuildWorkerRunOpts_NoDefault_WhenDisablePrometheus(t *testing.T) {
	resetWorkerRunOpts(t)
	c := &cb{config: config.Config{
		AppName:           "test_buildopts_disabled",
		DisablePrometheus: true,
	}}

	opts := c.buildWorkerRunOpts()
	if len(opts) != 0 {
		t.Fatalf("expected no options when DisablePrometheus=true, got %d", len(opts))
	}
}

func TestBuildWorkerRunOpts_NoDefault_WhenDeprecatedDisablePormetheus(t *testing.T) {
	resetWorkerRunOpts(t)
	c := &cb{config: config.Config{
		AppName:           "test_buildopts_deprecated",
		DisablePormetheus: true, //nolint:staticcheck // testing deprecated field
	}}

	opts := c.buildWorkerRunOpts()
	if len(opts) != 0 {
		t.Fatalf("expected no options when DisablePormetheus=true, got %d", len(opts))
	}
}

func TestBuildWorkerRunOpts_NoDefault_WhenEmptyAppName(t *testing.T) {
	resetWorkerRunOpts(t)
	c := &cb{config: config.Config{AppName: ""}}

	opts := c.buildWorkerRunOpts()
	if len(opts) != 0 {
		t.Fatalf("expected no default option when AppName is empty, got %d", len(opts))
	}
}

func TestAddWorkerRunOptions_AppendsAndCombinesWithDefault(t *testing.T) {
	resetWorkerRunOpts(t)
	AddWorkerRunOptions(workers.WithDefaultJitter(0))
	AddWorkerRunOptions(workers.AddInterceptors(noopMiddleware))

	c := &cb{config: config.Config{AppName: "test_buildopts_combined"}}
	opts := c.buildWorkerRunOpts()
	// 1 default Prometheus + 2 user options
	if len(opts) != 3 {
		t.Fatalf("expected 3 options (1 default + 2 user), got %d", len(opts))
	}
}

func TestAddWorkerRunOptions_NoDefault_WithoutAppName(t *testing.T) {
	resetWorkerRunOpts(t)
	AddWorkerRunOptions(workers.WithDefaultJitter(5))

	c := &cb{config: config.Config{}}
	opts := c.buildWorkerRunOpts()
	if len(opts) != 1 {
		t.Fatalf("expected 1 user option (no default), got %d", len(opts))
	}
}

func noopMiddleware(ctx context.Context, info *workers.WorkerInfo, next workers.CycleFunc) error {
	return next(ctx, info)
}

// runWithRecorder boots a core.cb with cfg, registers a CBWorkerProvider,
// injects rec via AddWorkerRunOptions, runs, waits for the recorder's
// WorkerStarted callback, then stops and drains. Used by the end-to-end
// tests below to verify that the option slice reaches workers.Run.
func runWithRecorder(t *testing.T, cfg config.Config, rec *recordingMetrics) {
	t.Helper()
	AddWorkerRunOptions(workers.WithMetrics(rec))

	cfg.GRPCPort = 0
	cfg.HTTPPort = 0
	cfg.ListenHost = "127.0.0.1"
	cfg.DisableSignalHandler = true
	cfg.DisableNewRelic = true
	cfg.DisableAutoMaxProcs = true

	instance := New(cfg)
	instance.SetService(&workerLifecycleService{})

	errCh := make(chan error, 1)
	go func() { errCh <- instance.Run() }()

	// Always stop the instance and drain Run() before the test exits, so a
	// failing assertion below never leaks the Run goroutine.
	var stopped atomic.Bool
	t.Cleanup(func() {
		if !stopped.CompareAndSwap(false, true) {
			return
		}
		_ = instance.Stop(2 * time.Second)
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
		}
	})

	startDeadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for rec.started.Load() == 0 {
		select {
		case err := <-errCh:
			t.Fatalf("Run exited before WorkerStarted fired: %v", err)
		case <-startDeadline:
			t.Fatal("recordingMetrics.WorkerStarted was never invoked")
		case <-ticker.C:
		}
	}

	if !stopped.CompareAndSwap(false, true) {
		return
	}
	if err := instance.Stop(2 * time.Second); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, grpc.ErrServerStopped) {
			t.Fatalf("unexpected Run error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after Stop")
	}
}

// TestRun_WorkerMetricsWired runs the full core.Run lifecycle with a worker
// and a recording Metrics implementation injected via AddWorkerRunOptions,
// and asserts that WorkerStarted fires — proving the option reaches workers.Run.
func TestRun_WorkerMetricsWired(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end Run lifecycle in short mode")
	}
	resetWorkerRunOpts(t)
	rec := &recordingMetrics{}
	runWithRecorder(t, config.Config{DisablePrometheus: true}, rec)
}

// TestRun_UserMetricsOverridesDefaultPrometheus exercises the override
// contract: when AppName is set (so the default Prometheus metrics is
// prepended) and the caller also adds workers.WithMetrics via
// AddWorkerRunOptions, the caller's recorder must be the effective metrics
// implementation. Uses a unique app name per run to avoid colliding with
// the process-global namespace cache in workers.NewPrometheusMetrics.
func TestRun_UserMetricsOverridesDefaultPrometheus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end Run lifecycle in short mode")
	}
	resetWorkerRunOpts(t)
	rec := &recordingMetrics{}
	runWithRecorder(t, config.Config{
		AppName: fmt.Sprintf("test_override_%d", time.Now().UnixNano()),
	}, rec)
}

// workerLifecycleService is a CBService + CBWorkerProvider used by the
// end-to-end test above. The worker blocks on ctx so it stays alive long
// enough for WorkerStarted to fire.
type workerLifecycleService struct{}

func (s *workerLifecycleService) InitHTTP(_ context.Context, _ *runtime.ServeMux, _ string, _ []grpc.DialOption) error {
	return nil
}

func (s *workerLifecycleService) InitGRPC(_ context.Context, _ *grpc.Server) error { return nil }

func (s *workerLifecycleService) Workers() []*workers.Worker {
	return []*workers.Worker{
		workers.NewWorker("test-lifecycle-worker").HandlerFunc(
			func(ctx context.Context, _ *workers.WorkerInfo) error {
				<-ctx.Done()
				return ctx.Err()
			},
		),
	}
}

var _ CBWorkerProvider = (*workerLifecycleService)(nil)
