package auth

import (
	"net"

	"github.com/cloudwego/hertz/pkg/app"
)

// ParseCIDRs parses a comma-separated CIDR list (TRUSTED_PROXY_CIDRS).
func ParseCIDRs(s string) ([]*net.IPNet, error) { return nil, errNotImplemented }

// ClientIPResolver derives the client IP (Q-5).
type ClientIPResolver struct{}

// NewClientIPResolver trusts forwarding headers only from the given CIDRs.
func NewClientIPResolver(trusted []*net.IPNet) *ClientIPResolver { return &ClientIPResolver{} }

// Resolve returns the client IP given the socket peer and a header accessor.
func (r *ClientIPResolver) Resolve(remote net.Addr, header func(string) string) string { return "" }

// FromContext resolves the client IP of a request.
func (r *ClientIPResolver) FromContext(c *app.RequestContext) string { return "" }

// TrustsRemote reports whether remote is inside a trusted proxy CIDR.
func (r *ClientIPResolver) TrustsRemote(remote net.Addr) bool { return false }
