package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/client"
	"github.com/cloudwego/hertz/pkg/protocol"
)

func TestDebugContentLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "33")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	}))
	defer srv.Close()

	c, _ := client.NewClient(client.WithResponseBodyStream(true))
	req := &protocol.Request{}
	req.SetRequestURI(srv.URL)
	req.SetMethod(http.MethodGet)
	resp := &protocol.Response{}
	
	err := c.Do(context.Background(), req, resp)
	fmt.Printf("err=%v\n", err)
	fmt.Printf("status=%d\n", resp.StatusCode())
	fmt.Printf("header.ContentLength=%d\n", resp.Header.ContentLength())
	fmt.Printf("header.Peek=%q\n", resp.Header.Peek("Content-Length"))
}
