package download

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/client"
	"github.com/cloudwego/hertz/pkg/protocol"
)

func TestDebugContentLength(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "33")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	}))
	defer srv.Close()

	c, _ := client.NewClient(client.WithResponseBodyStream(true))
	req := &protocol.Request{}
	req.SetRequestURI(srv.URL)
	req.SetMethod(http.MethodGet)
	resp := &protocol.Response{}

	err := c.Do(context.Background(), req, resp)
	if err != nil {
		t.Fatalf("unexpected Do error: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode())
	}
	if got := resp.Header.ContentLength(); got != 33 {
		t.Fatalf("expected Content-Length 33, got %d", got)
	}
}
