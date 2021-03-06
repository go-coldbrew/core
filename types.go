package core

import (
	"context"
	"net/http"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
)

type CBService interface {
	InitHTTP(ctx context.Context, mux *runtime.ServeMux, endpoint string, opts []grpc.DialOption) error
	InitGRPC(ctx context.Context, server *grpc.Server) error
}

type CBGracefulStopper interface {
	CBStopper
	FailCheck(bool)
}

type CBStopper interface {
	Stop()
}

type CB interface {
	SetService(CBService) error
	Run() error
	SetOpenAPIHandler(http.Handler)
	Stop(time.Duration) error
}
