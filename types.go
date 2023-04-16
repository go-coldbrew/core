package core

import (
	"context"
	"net/http"
	"time"

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
