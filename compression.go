package core

import (
	"net/http"

	"github.com/go-coldbrew/core/config"
	"github.com/klauspost/compress/gzhttp"
)

// newHTTPCompressionWrapper builds the gzhttp wrapper used by initHTTP. It
// negotiates gzip and (unless disabled) zstd from Accept-Encoding. Pulled out
// so it can be tested without standing up the full gateway.
func newHTTPCompressionWrapper(cfg config.Config) (func(http.Handler) http.HandlerFunc, error) {
	return gzhttp.NewWrapper(
		gzhttp.MinSize(cfg.HTTPCompressionMinSize),
		gzhttp.EnableZstd(!cfg.DisableZstdCompression),
		gzhttp.PreferZstd(!cfg.DisableZstdCompression && cfg.PreferZstd),
	)
}
