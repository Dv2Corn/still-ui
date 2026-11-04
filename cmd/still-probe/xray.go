package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// xrayRun is the local Xray the checks dial through: one loopback SOCKS
// inbound per job target, each routed to that target's outbound.
type xrayRun struct {
	cmd   *exec.Cmd
	ports []int
	ready bool
	cfg   string
}

func freePorts(n int) ([]int, error) {
	listeners := make([]net.Listener, 0, n)
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	ports := make([]int, 0, n)
	for range n {
		l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	return ports, nil
}

func buildXrayConfig(targets []jobTarget, ports []int) ([]byte, error) {
	inbounds := make([]map[string]any, 0, len(targets))
	outbounds := make([]any, 0, len(targets)+1)
	rules := make([]map[string]any, 0, len(targets))
	for i, t := range targets {
		var ob map[string]any
		if err := json.Unmarshal(t.Outbound, &ob); err != nil {
			return nil, fmt.Errorf("target %s: %w", t.Key, err)
		}
		in, out := "in-"+strconv.Itoa(i), "out-"+strconv.Itoa(i)
		ob["tag"] = out
		inbounds = append(inbounds, map[string]any{
			"tag": in, "listen": "127.0.0.1", "port": ports[i], "protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": false},
		})
		outbounds = append(outbounds, ob)
		rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{in}, "outboundTag": out})
	}
	// Anything not matched above must not leak out of this box unproxied.
	outbounds = append(outbounds, map[string]any{"tag": "block", "protocol": "blackhole"})
	return json.MarshalIndent(map[string]any{
		"log":       map[string]any{"loglevel": "warning", "access": "none"},
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"routing":   map[string]any{"domainStrategy": "AsIs", "rules": rules},
	}, "", "  ")
}

func startXray(bin, stateDir string, targets []jobTarget) (*xrayRun, error) {
	ports, err := freePorts(len(targets))
	if err != nil {
		return nil, err
	}
	raw, err := buildXrayConfig(targets, ports)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(stateDir, "xray.json")
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(context.Background(), bin, "run", "-c", cfgPath)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		_ = os.Remove(cfgPath)
		return nil, fmt.Errorf("start xray %s: %w", bin, err)
	}
	xr := &xrayRun{cmd: cmd, ports: ports, cfg: cfgPath}
	xr.ready = waitPorts(ports, 10*time.Second)
	return xr, nil
}

func waitPorts(ports []int, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for _, p := range ports {
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(p))
		for {
			c, err := (&net.Dialer{Timeout: time.Second}).DialContext(context.Background(), "tcp", addr)
			if err == nil {
				_ = c.Close()
				break
			}
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	return true
}

func (x *xrayRun) stop() {
	if x.cmd.Process != nil {
		_ = x.cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = x.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = x.cmd.Process.Kill()
			<-done
		}
	}
	_ = os.Remove(x.cfg)
}
