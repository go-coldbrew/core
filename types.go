package core

import (
	"context"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
)

type CBService interface {
	InitHTTP(ctx context.Context, mux *runtime.ServeMux, endpoint string, opts []grpc.DialOption) error
	InitGRPC(ctx context.Context, server *grpc.Server) error
}

type CB interface {
	SetService(CBService) error
	Run() error
	SetOpenAPIHandler(http.Handler)
}
