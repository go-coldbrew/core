package core

import (
	"mime"
	"net/http"

	"github.com/go-coldbrew/core/config"
	"github.com/klauspost/compress/gzhttp"
)

// sseMediaType is the Content-Type advertised by SSEMarshaler and excluded
// from HTTP compression.
const sseMediaType = "text/event-stream"

// newHTTPCompressionWrapper builds the gzhttp wrapper used by initHTTP. It
// negotiates gzip and (unless disabled) zstd from Accept-Encoding. Pulled out
// so it can be tested without standing up the full gateway.
//
// text/event-stream is excluded via excludeSSEContentTypeFilter — proxies
// and CDNs buffer compressed SSE responses, defeating real-time delivery.
func newHTTPCompressionWrapper(cfg config.Config) (func(http.Handler) http.HandlerFunc, error) {
	return gzhttp.NewWrapper(
		gzhttp.MinSize(cfg.HTTPCompressionMinSize),
		gzhttp.EnableZstd(!cfg.DisableZstdCompression),
		gzhttp.PreferZstd(!cfg.DisableZstdCompression && cfg.PreferZstd),
		gzhttp.ContentTypeFilter(excludeSSEContentTypeFilter),
	)
}

// excludeSSEContentTypeFilter wraps gzhttp.DefaultContentTypeFilter to also
// exclude text/event-stream, so SSE frames are delivered uncompressed and
// reach the client without intermediary buffering.
func excludeSSEContentTypeFilter(ct string) bool {
	if mediaType, _, err := mime.ParseMediaType(ct); err == nil && mediaType == sseMediaType {
		return false
	}
	return gzhttp.DefaultContentTypeFilter(ct)
}
