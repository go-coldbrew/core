package core

import (
	"context"
	"errors"
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

// TestRun_WorkerMetricsWired runs the full core.Run lifecycle with a worker
// and a recording Metrics implementation injected via AddWorkerRunOptions,
// and asserts that WorkerStarted fires — proving the option reaches workers.Run.
func TestRun_WorkerMetricsWired(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end Run lifecycle in short mode")
	}
	resetWorkerRunOpts(t)

	rec := &recordingMetrics{}
	AddWorkerRunOptions(workers.WithMetrics(rec))

	svc := &workerLifecycleService{}
	instance := New(config.Config{
		GRPCPort:             0,
		HTTPPort:             0,
		ListenHost:           "127.0.0.1",
		DisableSignalHandler: true,
		DisableNewRelic:      true,
		DisableAutoMaxProcs:  true,
		DisablePrometheus:    true, // suppress the default; recording metrics wins anyway
	})
	instance.SetService(svc)

	errCh := make(chan error, 1)
	go func() { errCh <- instance.Run() }()

	deadline := time.Now().Add(5 * time.Second)
	for rec.started.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if rec.started.Load() == 0 {
		t.Fatal("recordingMetrics.WorkerStarted was never invoked")
	}

	if err := instance.Stop(2 * time.Second); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	err := <-errCh
	if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, grpc.ErrServerStopped) {
		t.Fatalf("unexpected Run error: %v", err)
	}
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
