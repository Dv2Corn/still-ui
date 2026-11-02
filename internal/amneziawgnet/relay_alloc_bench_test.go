package amneziawgnet

import (
	"io"
	"net"
	"testing"
)

// One short relayed connection: 4-byte request, 16 KiB reply, both sides close.
// Reports allocated bytes per relayed connection (-benchmem).
func BenchmarkPipeBothWaysShortConn(b *testing.B) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer ln.Close()
	pair := func() (net.Conn, net.Conn) {
		ch := make(chan net.Conn, 1)
		go func() { c, _ := ln.Accept(); ch <- c }()
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			b.Fatal(err)
		}
		return c, <-ch
	}
	payload := make([]byte, 16*1024)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tunnelClient, tunnelSide := pair()
		upstreamSide, upstreamServer := pair()
		done := make(chan struct{})
		go func() { pipeBothWays(tunnelSide, upstreamSide); close(done) }()
		go func() {
			req := make([]byte, 4)
			_, _ = io.ReadFull(upstreamServer, req)
			_, _ = upstreamServer.Write(payload)
			_ = upstreamServer.Close()
		}()
		_, _ = tunnelClient.Write([]byte("ping"))
		_ = tunnelClient.(*net.TCPConn).CloseWrite()
		_, _ = io.Copy(io.Discard, tunnelClient)
		_ = tunnelClient.Close()
		<-done
	}
}
