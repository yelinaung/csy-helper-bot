package otel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func TestNewHTTPTransport_WrapsBase(t *testing.T) {
	t.Parallel()

	// NewHTTPTransport returns a non-nil RoundTripper and does not panic when
	// handed a concrete base transport.
	wrapped := NewHTTPTransport(http.DefaultTransport)
	require.NotNil(t, wrapped)
}

func TestNewHTTPTransport_NilBaseUsesDefault(t *testing.T) {
	t.Parallel()

	wrapped := NewHTTPTransport(nil)
	require.NotNil(t, wrapped)
}

func TestNewHTTPTransport_PreservesContext(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewHTTPTransport(http.DefaultTransport)}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()
}

// TestWrapClientBehavior verifies the integration behavior relied on by the
// bot: a nil client is a no-op, a real client is wrapped exactly once with an
// otelhttp.Transport, and a second call does not double-wrap (which would
// produce duplicate nested spans/metrics).
func TestWrapClientBehavior(t *testing.T) {
	t.Parallel()

	t.Run("nil client is no-op", func(t *testing.T) {
		t.Parallel()
		require.NotPanics(t, func() { WrapClient(nil) })
	})

	t.Run("wraps client and avoids double wrap", func(t *testing.T) {
		t.Parallel()

		client := &http.Client{}
		require.Nil(t, client.Transport, "expected zero-value client.Transport to be nil before wrapping")

		WrapClient(client)
		require.NotNil(t, client.Transport, "expected client.Transport to be non-nil after wrapping")

		first, ok := client.Transport.(*otelhttp.Transport)
		require.True(t, ok, "expected client.Transport to be *otelhttp.Transport after first wrap")
		require.NotNil(t, first)

		// Second wrap must be a no-op: same instance, not re-wrapped.
		WrapClient(client)
		require.Same(t, first, client.Transport, "expected WrapClient to skip an already-wrapped client")
	})
}
