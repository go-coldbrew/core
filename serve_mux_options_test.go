package core

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
)

type fakeMarshaler struct {
	contentType string
}

func (f *fakeMarshaler) Marshal(v any) ([]byte, error)   { return []byte("fake"), nil }
func (f *fakeMarshaler) Unmarshal(_ []byte, _ any) error { return errors.New("not used") }
func (f *fakeMarshaler) NewDecoder(_ io.Reader) runtime.Decoder {
	return runtime.DecoderFunc(func(_ any) error { return errors.New("not used") })
}

func (f *fakeMarshaler) NewEncoder(_ io.Writer) runtime.Encoder {
	return runtime.EncoderFunc(func(_ any) error { return errors.New("not used") })
}
func (f *fakeMarshaler) ContentType(_ any) string { return f.contentType }

func resetServeMuxOptionsForTest() {
	httpServeMuxOptions = nil
}

func TestRegisterHTTPMarshaler_RoundTrip(t *testing.T) {
	resetServeMuxOptionsForTest()
	t.Cleanup(resetServeMuxOptionsForTest)

	want := &fakeMarshaler{contentType: "application/x-test"}
	RegisterHTTPMarshaler("application/x-test", want)

	mux := runtime.NewServeMux(registeredServeMuxOptions()...)

	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	req.Header.Set("Accept", "application/x-test")
	req.Header.Set("Content-Type", "application/x-test")

	_, outbound := runtime.MarshalerForRequest(mux, req)
	if outbound != want {
		t.Fatalf("MarshalerForRequest returned %T, want the registered fake marshaler", outbound)
	}
}

func TestRegisterServeMuxOption_MiddlewareStacks(t *testing.T) {
	resetServeMuxOptionsForTest()
	t.Cleanup(resetServeMuxOptionsForTest)

	var customCalled, builtinCalled bool

	customMiddleware := func(next runtime.HandlerFunc) runtime.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request, p map[string]string) {
			customCalled = true
			next(w, r, p)
		}
	}
	builtinMiddleware := func(next runtime.HandlerFunc) runtime.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request, p map[string]string) {
			builtinCalled = true
			next(w, r, p)
		}
	}

	RegisterServeMuxOption(runtime.WithMiddlewares(customMiddleware))

	muxOpts := []runtime.ServeMuxOption{runtime.WithMiddlewares(builtinMiddleware)}
	muxOpts = append(muxOpts, registeredServeMuxOptions()...)
	mux := runtime.NewServeMux(muxOpts...)

	if err := mux.HandlePath(http.MethodGet, "/probe", func(w http.ResponseWriter, _ *http.Request, _ map[string]string) {
		w.WriteHeader(http.StatusOK)
	}); err != nil {
		t.Fatalf("HandlePath: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !customCalled {
		t.Error("custom middleware was not invoked")
	}
	if !builtinCalled {
		t.Error("built-in middleware was not invoked — registered options must stack, not replace")
	}
}

func TestRegisterHTTPMarshaler_Override(t *testing.T) {
	resetServeMuxOptionsForTest()
	t.Cleanup(resetServeMuxOptionsForTest)

	custom := &fakeMarshaler{contentType: "application/json"}
	RegisterHTTPMarshaler("application/json", custom)

	muxOpts := []runtime.ServeMuxOption{
		runtime.WithMarshalerOption("application/json", &runtime.JSONBuiltin{}),
	}
	muxOpts = append(muxOpts, registeredServeMuxOptions()...)
	mux := runtime.NewServeMux(muxOpts...)

	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	_, outbound := runtime.MarshalerForRequest(mux, req)
	if outbound != custom {
		t.Fatalf("MarshalerForRequest returned %T, want the user-registered marshaler (last-write-wins)", outbound)
	}
}

func TestResetServeMuxOptionsForTest(t *testing.T) {
	resetServeMuxOptionsForTest()
	RegisterServeMuxOption(runtime.WithMarshalerOption("application/x-foo", &fakeMarshaler{}))
	if got := len(registeredServeMuxOptions()); got != 1 {
		t.Fatalf("after register: got %d options, want 1", got)
	}
	resetServeMuxOptionsForTest()
	if got := len(registeredServeMuxOptions()); got != 0 {
		t.Fatalf("after reset: got %d options, want 0", got)
	}
}
