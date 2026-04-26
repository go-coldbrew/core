package core

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/go-coldbrew/workers"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
)

// fullService implements all optional interfaces for testing.
type fullService struct {
	preStartCalled  atomic.Bool
	postStartCalled atomic.Bool
	preStopCalled   atomic.Bool
	postStopCalled  atomic.Bool
	workersCalled   atomic.Bool
	failCheckCalled atomic.Bool
	stopCalled      atomic.Bool
	preStartErr     error
}

func (s *fullService) InitHTTP(_ context.Context, _ *runtime.ServeMux, _ string, _ []grpc.DialOption) error {
	return nil
}

func (s *fullService) InitGRPC(_ context.Context, _ *grpc.Server) error {
	return nil
}

func (s *fullService) PreStart(_ context.Context) error {
	s.preStartCalled.Store(true)
	return s.preStartErr
}

func (s *fullService) PostStart(_ context.Context) {
	s.postStartCalled.Store(true)
}

func (s *fullService) PreStop(_ context.Context) {
	s.preStopCalled.Store(true)
}

func (s *fullService) PostStop(_ context.Context) {
	s.postStopCalled.Store(true)
}

func (s *fullService) Workers() []*workers.Worker {
	s.workersCalled.Store(true)
	return []*workers.Worker{
		workers.NewWorker("test-worker").
			HandlerFunc(func(ctx context.Context, info *workers.WorkerInfo) error {
				<-ctx.Done()
				return ctx.Err()
			}),
	}
}

func (s *fullService) FailCheck(fail bool) {
	s.failCheckCalled.Store(true)
}

func (s *fullService) Stop() {
	s.stopCalled.Store(true)
}

// Compile-time interface assertions.
var (
	_ CBService         = (*fullService)(nil)
	_ CBPreStarter      = (*fullService)(nil)
	_ CBPostStarter     = (*fullService)(nil)
	_ CBPreStopper      = (*fullService)(nil)
	_ CBPostStopper     = (*fullService)(nil)
	_ CBWorkerProvider  = (*fullService)(nil)
	_ CBGracefulStopper = (*fullService)(nil)
	_ CBStopper         = (*fullService)(nil)
)

// plainService implements only CBService — no optional interfaces.
type plainService struct{}

func (s *plainService) InitHTTP(_ context.Context, _ *runtime.ServeMux, _ string, _ []grpc.DialOption) error {
	return nil
}

func (s *plainService) InitGRPC(_ context.Context, _ *grpc.Server) error {
	return nil
}

func TestOptionalInterfaces_Discovery(t *testing.T) {
	// Verify type assertions work for each optional interface.
	full := &fullService{}
	var svc CBService = full

	if _, ok := svc.(CBPreStarter); !ok {
		t.Error("fullService should implement CBPreStarter")
	}
	if _, ok := svc.(CBPostStarter); !ok {
		t.Error("fullService should implement CBPostStarter")
	}
	if _, ok := svc.(CBPreStopper); !ok {
		t.Error("fullService should implement CBPreStopper")
	}
	if _, ok := svc.(CBPostStopper); !ok {
		t.Error("fullService should implement CBPostStopper")
	}
	if _, ok := svc.(CBWorkerProvider); !ok {
		t.Error("fullService should implement CBWorkerProvider")
	}

	// plainService should NOT implement any optional interfaces.
	plain := &plainService{}
	var plainSvc CBService = plain

	if _, ok := plainSvc.(CBPreStarter); ok {
		t.Error("plainService should not implement CBPreStarter")
	}
	if _, ok := plainSvc.(CBWorkerProvider); ok {
		t.Error("plainService should not implement CBWorkerProvider")
	}
}

func TestPreStart_Error_AbortsStartup(t *testing.T) {
	c := &cb{
		svc: make([]CBService, 0),
	}

	svc := &fullService{preStartErr: errors.New("db connection failed")}
	c.svc = append(c.svc, svc)

	// Run() should return the PreStart error without starting servers.
	err := c.Run()
	if err == nil {
		t.Fatal("expected error from PreStart, got nil")
	}
	if !svc.preStartCalled.Load() {
		t.Error("PreStart was not called")
	}
	if err.Error() != "pre-start: db connection failed" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPreStart_SkippedForPlainService(t *testing.T) {
	// Verify that a service without CBPreStarter doesn't panic
	// during the PreStart type assertion loop.
	plain := &plainService{}
	var svc CBService = plain

	// The type assertion should return false for plainService.
	if _, ok := svc.(CBPreStarter); ok {
		t.Error("plainService should not implement CBPreStarter")
	}
}

func TestWorkerProvider_CollectsWorkers(t *testing.T) {
	svc := &fullService{}
	c := &cb{
		svc: []CBService{svc},
	}

	// Simulate what Run() does: collect workers via type assertion.
	var allWorkers []*workers.Worker
	for _, s := range c.svc {
		if wp, ok := s.(CBWorkerProvider); ok {
			allWorkers = append(allWorkers, wp.Workers()...)
		}
	}

	if !svc.workersCalled.Load() {
		t.Error("Workers() was not called")
	}
	if len(allWorkers) != 1 {
		t.Errorf("expected 1 worker, got %d", len(allWorkers))
	}
}

func TestWorkerProvider_SkippedForPlainService(t *testing.T) {
	c := &cb{
		svc: []CBService{&plainService{}},
	}

	var allWorkers []*workers.Worker
	for _, s := range c.svc {
		if wp, ok := s.(CBWorkerProvider); ok {
			allWorkers = append(allWorkers, wp.Workers()...)
		}
	}

	if len(allWorkers) != 0 {
		t.Errorf("expected 0 workers for plain service, got %d", len(allWorkers))
	}
}

func TestStopHooks_Called(t *testing.T) {
	svc := &fullService{}
	c := &cb{
		svc: []CBService{svc},
	}

	// Call Stop directly to test hook ordering.
	_ = c.Stop(0)

	if !svc.preStopCalled.Load() {
		t.Error("PreStop was not called")
	}
	if !svc.failCheckCalled.Load() {
		t.Error("FailCheck was not called")
	}
	if !svc.stopCalled.Load() {
		t.Error("Stop was not called")
	}
	if !svc.postStopCalled.Load() {
		t.Error("PostStop was not called")
	}
}

func TestStopHooks_NotCalledForPlainService(t *testing.T) {
	c := &cb{
		svc: []CBService{&plainService{}},
	}

	// Should not panic when no optional interfaces are implemented.
	_ = c.Stop(0)
}
