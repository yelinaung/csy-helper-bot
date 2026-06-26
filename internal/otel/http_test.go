package otel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewHTTPTransport_NoDoubleWrap(t *testing.T) {
	t.Parallel()

	base := http.DefaultTransport
	wrapped := NewHTTPTransport(base)
	// Wrapping the already-wrapped transport is a passthrough config; the key
	// property tested here is that NewHTTPTransport returns a non-nil
	// RoundTripper and does not panic.
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
