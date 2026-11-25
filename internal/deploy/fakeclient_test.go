package deploy

import (
	"io"
	"net/http"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fakeBodyClient returns an *http.Client that answers every request with a 200
// whose body (and Content-Length) come from body. No network is involved.
func fakeBodyClient(body func(*http.Request) (io.ReadCloser, int64)) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rc, n := body(r)
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Header:        http.Header{},
			Body:          rc,
			ContentLength: n,
			Request:       r,
		}, nil
	})}
}
