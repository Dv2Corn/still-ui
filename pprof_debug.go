//go:build pprof

package main

import (
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
)

// A test-only build (go build -tags pprof) serves runtime profiles on loopback;
// release builds never compile this file. STILL_PPROF_ADDR overrides the address.
func init() {
	addr := os.Getenv("STILL_PPROF_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6060"
	}
	go func() {
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Println("pprof listener stopped:", err)
		}
	}()
}
