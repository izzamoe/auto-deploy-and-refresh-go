package config

import "testing"

// B32 / FR-056 / NFR-004: PPROF_ADDR exposes goroutine dumps, heap profiles and CPU
// profiling without authentication, so it may only bind a loopback address.
func TestPprofAddr_OnlyLoopbackIsAccepted(t *testing.T) {
	for _, ok := range []string{"localhost:6060", "127.0.0.1:6060", "[::1]:6060", "127.0.0.2:6060", "LOCALHOST:6060"} {
		if err := ValidatePprofAddr(ok); err != nil {
			t.Errorf("%q must be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{
		":6060",         // every interface
		"0.0.0.0:6060",  // every IPv4 interface
		"[::]:6060",     // every IPv6 interface
		"10.0.0.5:6060", // a LAN address
		"192.168.1.10:6060",
		"203.0.113.9:6060", // a public address
		"example.com:6060", // a name that can resolve to anything
		"6060",             // no host
		"localhost",        // no port
		"localhost:abc",
		"localhost:0",
		"localhost:70000",
		"127.0.0.1:6060/x",
		"",
	} {
		if err := ValidatePprofAddr(bad); err == nil {
			t.Errorf("B32: PPROF_ADDR=%q was accepted; only loopback hosts may be", bad)
		}
	}
}
