package core

import (
	"context"
	"net/http"
	"time"

	"github.com/go-coldbrew/workers"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
)

// CBService is the interface that wraps service methods used in ColdBrew.
// InitHTTP initializes the HTTP server.
// InitGRPC initializes the gRPC server.
// InitHTTP and InitGRPC are called by the core package.
type CBService interface {
	// InitHTTP initializes the HTTP server
	// mux is the HTTP server mux to register the service.
	// endpoint is the gRPC endpoint to connect.
	// opts is the gRPC dial options used to connect to the endpoint.
	InitHTTP(ctx context.Context, mux *runtime.ServeMux, endpoint string, opts []grpc.DialOption) error
	// InitGRPC initializes the gRPC server
	// server is the gRPC server to register the service.
	InitGRPC(ctx context.Context, server *grpc.Server) error
}

// CBGracefulStopper is the interface that wraps the graceful stop method.
type CBGracefulStopper interface {
	// FailCheck set if the service is ready to stop.
	// FailCheck is called by the core package.
	FailCheck(bool)
}

// CBStopper is the interface that wraps the stop method.
type CBStopper interface {
	// Stop stops the service.
	// Stop is called by the core package.
	Stop()
}

// CBWorkerProvider is implemented by services that run background workers.
// Workers are started after initGRPC/initHTTP and stopped during graceful
// shutdown. Called once during Run(). Workers are managed by the
// go-coldbrew/workers package with automatic panic recovery, configurable
// restart, and structured shutdown via suture supervisor trees.
type CBWorkerProvider interface {
	Workers() []*workers.Worker
}

// CBPreStarter is implemented by services that need setup before servers
// start. Called during Run(), before initGRPC/initHTTP. If PreStart returns
// an error, startup is aborted. Use this for connecting to databases,
// message brokers, configuring interceptors, or any setup that must
// complete before the service accepts traffic.
type CBPreStarter interface {
	PreStart(ctx context.Context) error
}

// CBPostStarter is implemented by services that need to act after servers
// are listening. Use this for registering with service discovery, logging
// startup banners, or notifying external systems.
type CBPostStarter interface {
	PostStart(ctx context.Context)
}

// CBPreStopper is implemented by services that need to act before graceful
// shutdown begins. Use this for deregistering from load balancers, flushing
// buffers, or notifying external systems of impending shutdown.
type CBPreStopper interface {
	PreStop(ctx context.Context)
}

// CBPostStopper is implemented by services that need final cleanup after
// all servers and workers have stopped. Use this for closing audit logs,
// pushing final metrics, or any cleanup that must happen after all
// in-flight work is complete.
type CBPostStopper interface {
	PostStop(ctx context.Context)
}

// CB is the interface that wraps coldbrew methods.
type CB interface {
	// SetService sets the service.
	SetService(CBService) error
	// Run runs the service.
	// Run is blocking. It returns an error if the service fails. Otherwise, it returns nil.
	Run() error
	// SetOpenAPIHandler sets the OpenAPI handler.
	SetOpenAPIHandler(http.Handler)
	// Stop stops the service.
	// Stop is blocking. It returns an error if the service fails. Otherwise, it returns nil.
	// duration is the duration to wait for the service to stop.
	Stop(time.Duration) error
}
