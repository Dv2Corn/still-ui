package main

// "x-ui node-enroll" adds this panel as a node of the master that made the
// invitation; install.sh node-enroll runs it after installing still-ui.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/nodeenroll"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
	"github.com/mhsanaei/3x-ui/v3/internal/util/random"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/panel"
)

const nodeEnrollInviteEnv = "STILL_NODE_INVITE"

type nodeEnrollOptions struct {
	name      string
	address   string
	noRestart bool
}

// nodeEnrollDeps are the parts of the run that touch the machine; tests swap them.
type nodeEnrollDeps struct {
	initDB       func() error
	restartPanel func(ctx context.Context) error
	localTimeout time.Duration
	hostname     func() (string, error)
}

func defaultNodeEnrollDeps() nodeEnrollDeps {
	return nodeEnrollDeps{
		initDB:       func() error { return database.InitDB(config.GetDBPath()) },
		restartPanel: restartPanelService,
		localTimeout: 90 * time.Second,
		hostname:     os.Hostname,
	}
}

func runNodeEnroll(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("node-enroll", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var opts nodeEnrollOptions
	fs.StringVar(&opts.name, "name", "", "node name on the master (default: the invitation's, else this server's host name)")
	fs.StringVar(&opts.address, "address", "", "address the master reaches this panel at (default: the invitation's, else the address this server connects from)")
	fs.BoolVar(&opts.noRestart, "no-restart", false, "do not restart the panel after giving it a certificate")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: x-ui node-enroll [-name NAME] [-address HOST] < invitation")
		fmt.Fprintln(stderr, "The invitation is read from "+nodeEnrollInviteEnv+" or standard input, never from the command line.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "node-enroll: the invitation goes in "+nodeEnrollInviteEnv+" or on standard input, not on the command line (other users can read it there)")
		return 2
	}
	raw := os.Getenv(nodeEnrollInviteEnv)
	_ = os.Unsetenv(nodeEnrollInviteEnv)
	if strings.TrimSpace(raw) == "" {
		line, err := bufio.NewReader(io.LimitReader(stdin, 4096)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintln(stderr, "node-enroll: reading the invitation:", err)
			return 1
		}
		raw = line
	}
	inv, err := nodeenroll.Parse(raw)
	if err != nil {
		fmt.Fprintln(stderr, "node-enroll:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := enrollNode(ctx, inv, opts, defaultNodeEnrollDeps(), stdout); err != nil {
		fmt.Fprintln(stderr, "node-enroll:", err)
		return 1
	}
	return 0
}

func explainMasterError(err error) error {
	var se *nodeenroll.StatusError
	switch {
	case errors.Is(err, nodeenroll.ErrKeyPinMismatch):
		return errors.New("the server at the master URL is not the master that made this invitation (its TLS key does not match); nothing was changed")
	case errors.As(err, &se) && se.Status == http.StatusUnauthorized:
		return errors.New("the master does not accept this invitation: it is unknown, already used, revoked or expired; make a new one on the master's Nodes page")
	case errors.As(err, &se):
		return err
	}
	return fmt.Errorf("cannot reach the master: %w", err)
}

func enrollNode(ctx context.Context, inv nodeenroll.Invite, opts nodeEnrollOptions, deps nodeEnrollDeps, out io.Writer) error {
	client := nodeenroll.NewClient(inv)
	check, err := client.Check(ctx)
	if err != nil {
		return explainMasterError(err)
	}
	if check.Scope != model.ApiScopeNodeSync && check.Scope != model.ApiScopeNodeAdmin {
		return fmt.Errorf("the master asked for an API token of scope %q, which a node does not issue", check.Scope)
	}
	fmt.Fprintf(out, "Master %s accepted the invitation (valid until %s).\n", inv.MasterURL, time.Unix(check.ExpiresAt, 0).Format(time.RFC3339))

	if err := deps.initDB(); err != nil {
		return fmt.Errorf("open the panel database: %w", err)
	}
	settings := service.SettingService{}
	port, err := settings.GetListenPort()
	if err != nil {
		return err
	}
	panelDomain, err := settings.GetWebDomain()
	if err != nil {
		return err
	}
	basePath, err := settings.GetBasePath()
	if err != nil {
		return err
	}
	listen, err := settings.GetListen()
	if err != nil {
		return err
	}
	certSha, changed, err := ensureNodePanelCert(&settings, certHosts(opts.address))
	if err != nil {
		return err
	}

	keep, restarted := false, false
	if changed {
		defer func() {
			if !keep {
				takeBackNodePanelCert(ctx, &settings, restarted, deps, out)
			}
		}()
	}

	tokens := panel.ApiTokenService{}
	tokenName := "master-enroll-" + time.Now().UTC().Format("20060102-150405") + "-" + random.Seq(4)
	created, err := tokens.Create(tokenName, check.Scope, 0)
	if err != nil {
		return fmt.Errorf("issue the API token for the master: %w", err)
	}
	defer func() {
		if !keep {
			if err := tokens.Delete(created.Id); err != nil {
				fmt.Fprintln(out, "warning: could not remove the unused API token", tokenName+":", err)
			}
		}
	}()

	if changed {
		if opts.noRestart {
			return errors.New("the panel got a new certificate and must restart before the master can reach it; run again without -no-restart")
		}
		fmt.Fprintln(out, "Restarting the panel to load its new certificate...")
		restarted = true
		if err := deps.restartPanel(ctx); err != nil {
			return fmt.Errorf("restart the panel: %w", err)
		}
	}
	if err := waitLocalPanel(ctx, listen, panelDomain, port, basePath, certSha, created.Token, deps.localTimeout); err != nil {
		return err
	}

	name := opts.name
	if name == "" {
		name, _ = deps.hostname()
	}
	resp, err := client.Register(ctx, nodeenroll.RegisterRequest{
		Name: name, Address: opts.address, Port: port, BasePath: basePath,
		CertSha256: certSha, ApiToken: created.Token, PanelVersion: config.GetPanelVersion(),
	})
	if err != nil && !masterRefused(err) {
		keep = true // the master may have added the node with this token and certificate
		return fmt.Errorf("the master did not answer the registration (%v), so it may have added this node anyway: "+
			"look for it on the master's Nodes page; if it is not there, delete the API token %q here and run again with a new invitation", err, tokenName)
	}
	if err != nil {
		return explainMasterError(err)
	}
	keep = true
	fmt.Fprintf(out, "Done: this server is node %q (#%d) on the master, reached at %s:%d (TLS %s).\n",
		resp.Name, resp.NodeId, resp.Address, port, resp.TlsVerifyMode)
	fmt.Fprintf(out, "The master uses the API token %q of this panel; delete it in Settings → API tokens to cut the master off.\n", tokenName)
	return nil
}

// masterRefused reports an answer from the master that it did not add the node; anything else
// (a lost or garbled answer) may follow a commit on the master.
func masterRefused(err error) bool {
	var se *nodeenroll.StatusError
	return errors.As(err, &se) || errors.Is(err, nodeenroll.ErrKeyPinMismatch)
}

// takeBackNodePanelCert undoes ensureNodePanelCert after a failed enrollment, so a panel that
// served plain HTTP (behind a proxy, through a tunnel) does so again.
func takeBackNodePanelCert(ctx context.Context, settings *service.SettingService, restarted bool, deps nodeEnrollDeps, out io.Writer) {
	if err := settings.SetCertFile(""); err != nil {
		fmt.Fprintln(out, "warning: could not clear the certificate this run gave the panel:", err)
		return
	}
	if err := settings.SetKeyFile(""); err != nil {
		fmt.Fprintln(out, "warning: could not clear the certificate this run gave the panel:", err)
		return
	}
	dir := filepath.Join(config.GetDBFolderPath(), "node-tls")
	_ = os.Remove(filepath.Join(dir, "cert.pem"))
	_ = os.Remove(filepath.Join(dir, "key.pem"))
	if !restarted {
		fmt.Fprintln(out, "The panel's settings are as before: the certificate it was given is removed again.")
		return
	}
	if err := deps.restartPanel(ctx); err != nil {
		fmt.Fprintln(out, "warning: the certificate it was given is removed again, but the panel did not restart:", err, "- restart it yourself to serve plain HTTP again")
		return
	}
	fmt.Fprintln(out, "The panel is back on plain HTTP: the certificate it was given is removed again.")
}

// certHosts lists the names a generated certificate carries: the address the
// master will use when known, and this machine's own addresses.
func certHosts(address string) []string {
	hosts := []string{}
	if address != "" {
		hosts = append(hosts, address)
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() {
				hosts = append(hosts, ipn.IP.String())
			}
		}
	}
	return hosts
}

// ensureNodePanelCert gives a panel without a certificate a self-signed one (the
// master pins it); a configured one that does not load is an error, never replaced.
func ensureNodePanelCert(settings *service.SettingService, hosts []string) (string, bool, error) {
	certFile, err := settings.GetCertFile()
	if err != nil {
		return "", false, err
	}
	keyFile, err := settings.GetKeyFile()
	if err != nil {
		return "", false, err
	}
	if certFile != "" || keyFile != "" {
		pair, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return "", false, fmt.Errorf("the panel's certificate (%s, %s) does not load: %w; fix or clear it first", certFile, keyFile, err)
		}
		sum := sha256.Sum256(pair.Certificate[0])
		return hex.EncodeToString(sum[:]), false, nil
	}

	var ips []net.IP
	var dns []string
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			ips = append(ips, ip)
		} else if h != "" {
			dns = append(dns, h)
		}
	}
	pair, err := crypto.GenerateSelfSignedServerCert("still-ui node", ips, dns)
	if err != nil {
		return "", false, err
	}
	dir := filepath.Join(config.GetDBFolderPath(), "node-tls")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, err
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(keyPath, pair.KeyPEM, 0o600); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(certPath, pair.CertPEM, 0o644); err != nil {
		return "", false, err
	}
	if err := settings.SetCertFile(certPath); err != nil {
		return "", false, err
	}
	if err := settings.SetKeyFile(keyPath); err != nil {
		return "", false, err
	}
	loaded, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return "", false, err
	}
	sum := sha256.Sum256(loaded.Certificate[0])
	return hex.EncodeToString(sum[:]), true, nil
}

func restartPanelService(ctx context.Context) error {
	if path, err := exec.LookPath("systemctl"); err == nil {
		return exec.CommandContext(ctx, path, "restart", config.ServiceName).Run()
	}
	if path, err := exec.LookPath("rc-service"); err == nil {
		return exec.CommandContext(ctx, path, config.ServiceName, "restart").Run()
	}
	return errors.New("neither systemctl nor rc-service found; restart the panel yourself and run again")
}

// waitLocalPanel waits until the panel serves the expected certificate and takes
// the new token, so the master's check does not race the restart.
func waitLocalPanel(ctx context.Context, listen, panelDomain string, port int, basePath, certSha, token string, timeout time.Duration) error {
	host := listen
	if ip := net.ParseIP(listen); listen == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	want, err := hex.DecodeString(certSha)
	if err != nil {
		return err
	}
	tlsCfg := &tls.Config{
		InsecureSkipVerify: true, // lgtm[go/disabled-certificate-check] the certificate hash is checked below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no certificate")
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(sum[:], want) != 1 {
				return errors.New("the panel still serves its previous certificate")
			}
			return nil
		},
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	defer client.CloseIdleConnections()
	url := "https://" + net.JoinHostPort(host, strconv.Itoa(port)) + basePath + "panel/api/server/status"
	deadline := time.Now().Add(timeout)
	var last error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if panelDomain != "" {
			req.Host = panelDomain // the Panel Domain check answers 403 to any other Host
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		last = err
		if time.Now().After(deadline) {
			return fmt.Errorf("the panel does not answer at %s with its certificate and the new token: %w", url, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
