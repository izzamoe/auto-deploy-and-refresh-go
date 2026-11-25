package auth

import (
	"log/slog"
	"net"
	"strings"
	"sync/atomic"

	"github.com/cloudwego/hertz/pkg/app"
)

// ClientIPResolver derives the client IP (Q-5). Forwarding headers
// (CF-Connecting-IP, X-Forwarded-For) are honoured only when the socket peer is
// inside one of the trusted proxy CIDRs (TRUSTED_PROXY_CIDRS, default empty);
// otherwise any client could pick its own rate-limit bucket.
type ClientIPResolver struct {
	trusted []*net.IPNet
	warned  atomic.Bool
	warn    func(msg string, args ...any)
}

// NewClientIPResolver trusts forwarding headers only from the given CIDRs.
func NewClientIPResolver(trusted []*net.IPNet) *ClientIPResolver {
	return &ClientIPResolver{trusted: trusted, warn: slog.Warn}
}

// Resolve returns the client IP given the socket peer and a header accessor.
func (r *ClientIPResolver) Resolve(remote net.Addr, header func(string) string) string {
	peer := addrIP(remote)
	cf, xff := strings.TrimSpace(header("CF-Connecting-IP")), strings.TrimSpace(header("X-Forwarded-For"))

	if len(r.trusted) == 0 {
		if (cf != "" || xff != "") && r.warned.CompareAndSwap(false, true) {
			r.warn("forwarding headers (CF-Connecting-IP/X-Forwarded-For) are present but TRUSTED_PROXY_CIDRS is empty: ignoring them, so every client behind the proxy shares one rate-limit bucket; set TRUSTED_PROXY_CIDRS to the proxy address range")
		}
		return ipString(peer)
	}
	if peer == nil || !r.trustsIP(peer) {
		return ipString(peer)
	}
	if ip := net.ParseIP(cf); ip != nil {
		return ip.String()
	}
	// X-Forwarded-For: the right-most entry that is not one of our own proxies is the
	// address our edge actually saw; anything further left is client-controlled.
	if xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip := net.ParseIP(strings.TrimSpace(parts[i]))
			if ip == nil {
				break // garbage: stop trusting the chain
			}
			if !r.trustsIP(ip) {
				return ip.String()
			}
		}
	}
	return ipString(peer)
}

// FromContext resolves the client IP of a request.
func (r *ClientIPResolver) FromContext(c *app.RequestContext) string {
	return r.Resolve(c.RemoteAddr(), func(k string) string { return string(c.GetHeader(k)) })
}

// TrustsRemote reports whether remote is inside a trusted proxy CIDR.
func (r *ClientIPResolver) TrustsRemote(remote net.Addr) bool {
	ip := addrIP(remote)
	return ip != nil && r.trustsIP(ip)
}

func (r *ClientIPResolver) trustsIP(ip net.IP) bool {
	for _, n := range r.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func addrIP(a net.Addr) net.IP {
	switch v := a.(type) {
	case nil:
		return nil
	case *net.TCPAddr:
		return v.IP
	case *net.UDPAddr:
		return v.IP
	case *net.IPAddr:
		return v.IP
	}
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		host = a.String()
	}
	return net.ParseIP(host)
}

func ipString(ip net.IP) string {
	if ip == nil {
		return "unknown"
	}
	return ip.String()
}
