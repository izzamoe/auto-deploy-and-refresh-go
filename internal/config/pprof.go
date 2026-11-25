package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ValidatePprofAddr accepts only a loopback listen address for PPROF_ADDR (B32, FR-056,
// NFR-004): pprof serves goroutine dumps, heap profiles and CPU profiling without any
// authentication. The host must be "localhost" or a loopback IP literal; an empty host
// (":6060"), a wildcard, a LAN/public address or any other name (which could resolve to
// anything) is refused, and so is a missing or invalid port.
func ValidatePprofAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("PPROF_ADDR %q must be host:port with a loopback host (e.g. localhost:6060): %w", addr, err)
	}
	if n, perr := strconv.Atoi(port); perr != nil || n < 1 || n > 65535 {
		return fmt.Errorf("PPROF_ADDR %q has an invalid port %q", addr, port)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("PPROF_ADDR %q is not a loopback address: pprof is unauthenticated and may only listen on localhost or 127.0.0.1/::1", addr)
}
