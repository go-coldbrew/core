package core

import (
	"context"
	"net/http"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
)

// CBService is the interface that wraps the basic service methods.
// InitHTTP initializes the HTTP server.
// InitGRPC initializes the gRPC server.
// InitHTTP and InitGRPC are called by the core package.
type CBService interface {
	InitHTTP(ctx context.Context, mux *runtime.ServeMux, endpoint string, opts []grpc.DialOption) error
	InitGRPC(ctx context.Context, server *grpc.Server) error
}

// CBGracefulStopper is the interface that wraps the graceful stop method.
// FailCheck checks if the service is ready to stop.
// FailCheck is called by the core package.
type CBGracefulStopper interface {
	FailCheck(bool)
}

// CBStopper is the interface that wraps the stop method.
// Stop stops the service.
// Stop is called by the core package.
type CBStopper interface {
	Stop()
}

// CB is the interface that wraps the basic coldbrew methods.
// SetService sets the service.
// Run runs the service.
// SetOpenAPIHandler sets the OpenAPI handler.
// Stop stops the service.
type CB interface {
	SetService(CBService) error
	Run() error
	SetOpenAPIHandler(http.Handler)
	Stop(time.Duration) error
}
