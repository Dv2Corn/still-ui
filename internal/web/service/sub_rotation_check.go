package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/link"
	"github.com/mhsanaei/3x-ui/v3/internal/util/random"
)

// SubRotationClientPrefix names the temporary client of the subscription rotation self-check.
const SubRotationClientPrefix = "still-ui-rotation-check-"

const (
	SubRotationStepInbound = "pick an inbound the subscription server serves"
	SubRotationStepCreate  = "create the test client"
	SubRotationStepFetch   = "fetch its subscription"
	SubRotationStepReissue = "re-issue the subscription id"
	SubRotationStepNewLink = "new link serves the client"
	SubRotationStepOldLink = "old link answers 404"
	SubRotationStepCleanup = "delete the test client"
)

// The check's client must never work as a real account: a one-byte quota and a short life.
const (
	subRotationClientQuota = 1
	subRotationClientLife  = 15 * time.Minute
	subRotationBodyLimit   = 4 << 20
	subRotationUserAgent   = "still-ui-rotation-check"
)

type SubRotationStep struct {
	Name   string `json:"name" example:"old link answers 404"`
	OK     bool   `json:"ok" example:"true"`
	Detail string `json:"detail" example:"HTTP 404"`
}

type SubRotationReport struct {
	Passed      bool              `json:"passed" example:"true"`
	Client      string            `json:"client" example:"still-ui-rotation-check-1767225600000"`
	Steps       []SubRotationStep `json:"steps"`
	NeedRestart bool              `json:"-"`
}

func (r *SubRotationReport) add(name string, err error, okDetail string) bool {
	step := SubRotationStep{Name: name, OK: err == nil, Detail: okDetail}
	if err != nil {
		step.Detail = err.Error()
	}
	r.Steps = append(r.Steps, step)
	return step.OK
}

// SubRotationCheck proves end to end that re-issuing a subscription id, the way the client
// form's Save does, makes the running subscription server serve the new link and drop the old.
type SubRotationCheck struct {
	Clients  *ClientService
	Inbounds *InboundService
	SubURL   func(subID string) string
	HTTP     *http.Client
}

// NewSubRotationCheck aims the check at the subscription server this panel runs, over loopback.
func NewSubRotationCheck(settings *SettingService, clients *ClientService, inbounds *InboundService) (*SubRotationCheck, error) {
	enabled, err := settings.GetSubEnable()
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, errors.New("the subscription server is disabled")
	}
	listen, err := settings.GetSubListen()
	if err != nil {
		return nil, err
	}
	port, err := settings.GetSubPort()
	if err != nil {
		return nil, err
	}
	path, err := settings.GetSubPath()
	if err != nil {
		return nil, err
	}
	domain, err := settings.GetSubDomain()
	if err != nil {
		return nil, err
	}
	certFile, err := settings.GetSubCertFile()
	if err != nil {
		return nil, err
	}
	keyFile, err := settings.GetSubKeyFile()
	if err != nil {
		return nil, err
	}
	dialHost := listen
	if ip := net.ParseIP(listen); listen == "" || (ip != nil && ip.IsUnspecified()) {
		dialHost = "127.0.0.1"
	}
	dialAddr := net.JoinHostPort(dialHost, strconv.Itoa(port))
	// The URL names the sub domain when one is set: the server refuses any other Host.
	urlHost := dialHost
	if domain != "" {
		urlHost = domain
	}
	scheme := "http"
	if certFile != "" || keyFile != "" {
		scheme = "https"
	}
	base := scheme + "://" + net.JoinHostPort(urlHost, strconv.Itoa(port)) + path
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, dialAddr)
		},
		// Without a sub domain no certificate name can match the loopback address of our own listener.
		TLSClientConfig: &tls.Config{ServerName: urlHost, InsecureSkipVerify: domain == ""},
	}
	return &SubRotationCheck{
		Clients:  clients,
		Inbounds: inbounds,
		SubURL:   func(subID string) string { return base + subID },
		HTTP:     &http.Client{Transport: transport, Timeout: 15 * time.Second},
	}, nil
}

// Run never stops before deleting the test client once it may exist, and reports that deletion.
func (c *SubRotationCheck) Run(ctx context.Context) (report SubRotationReport) {
	now := time.Now()
	email := SubRotationClientPrefix + strconv.FormatInt(now.UnixMilli(), 10)
	report.Client = email
	defer func() {
		report.Passed = true
		for _, step := range report.Steps {
			report.Passed = report.Passed && step.OK
		}
	}()

	ib, err := subRotationInbound()
	if !report.add(SubRotationStepInbound, err, inboundLabel(ib)) {
		return report
	}
	client := model.Client{
		Email:      email,
		ID:         uuid.NewString(),
		Password:   random.Seq(16),
		SubID:      random.NumLower(16),
		TotalGB:    subRotationClientQuota,
		ExpiryTime: now.Add(subRotationClientLife).UnixMilli(),
		Enable:     true,
		Comment:    "temporary client of the subscription rotation self-check",
	}
	needRestart, err := c.Clients.Create(c.Inbounds, &ClientCreatePayload{Client: client, InboundIds: []int{ib.Id}})
	report.NeedRestart = needRestart
	createErr := err
	defer func() {
		if createErr != nil && !c.clientStored(email) {
			return
		}
		nr, err := c.Clients.DeleteByEmail(c.Inbounds, email, false)
		report.NeedRestart = report.NeedRestart || nr
		report.add(SubRotationStepCleanup, err, email)
	}()
	if !report.add(SubRotationStepCreate, createErr, email) {
		return report
	}

	credential := client.ID
	if ib.Protocol == model.Trojan {
		credential = client.Password
	}
	oldURL := c.SubURL(client.SubID)
	if !report.add(SubRotationStepFetch, c.expectClient(ctx, oldURL, credential), oldURL) {
		return report
	}

	newSubID := random.NumLower(16)
	err = c.reissue(email, newSubID)
	if !report.add(SubRotationStepReissue, err, client.SubID+" -> "+newSubID) {
		return report
	}
	newURL := c.SubURL(newSubID)
	if !report.add(SubRotationStepNewLink, c.expectClient(ctx, newURL, credential), newURL) {
		return report
	}
	report.add(SubRotationStepOldLink, c.expectGone(ctx, oldURL), "HTTP 404")
	return report
}

// subRotationInbound picks a local inbound whose share links carry the client's own credential.
func subRotationInbound() (*model.Inbound, error) {
	var ib model.Inbound
	err := database.GetDB().
		Where("enable = ? AND exclude_from_sub = ? AND node_id IS NULL AND protocol IN ?", true, false,
			[]model.Protocol{model.VLESS, model.VMESS, model.Trojan}).
		Order("id").First(&ib).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.New("no enabled local VLESS, VMess or Trojan inbound is in the subscription")
	}
	if err != nil {
		return nil, err
	}
	return &ib, nil
}

func inboundLabel(ib *model.Inbound) string {
	if ib == nil {
		return ""
	}
	return fmt.Sprintf("inbound %d (%s, %s)", ib.Id, ib.Protocol, ib.Tag)
}

func (c *SubRotationCheck) clientStored(email string) bool {
	_, err := c.Clients.GetRecordByEmail(nil, email)
	return err == nil
}

// reissue sends the client form's full Save payload with only the subscription id changed.
func (c *SubRotationCheck) reissue(email, newSubID string) error {
	rec, err := c.Clients.GetRecordByEmail(nil, email)
	if err != nil {
		return err
	}
	edited := rec.ToClient()
	edited.SubID = newSubID
	body, err := json.Marshal(clientPayloadWithHwid{Client: *edited, LimitHwid: rec.LimitHwid})
	if err != nil {
		return err
	}
	_, err = c.Clients.UpdateByEmailJSON(c.Inbounds, email, body)
	return err
}

// fetch sends what a device-limited app sends: a device id, so the gate counts it as a device.
func (c *SubRotationCheck) fetch(ctx context.Context, url string) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("User-Agent", subRotationUserAgent)
	req.Header.Set("X-HWID", subRotationUserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, subRotationBodyLimit))
	return resp.StatusCode, resp.Header, body, err
}

func (c *SubRotationCheck) expectClient(ctx context.Context, url, credential string) error {
	status, header, body, err := c.fetch(ctx, url)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("HTTP %d", status)
	}
	if header.Get("X-Hwid-Limit") != "" || header.Get("X-Hwid-Not-Supported") != "" {
		return errors.New("the device limit refused the check's device and served its notice instead of servers")
	}
	outbounds, _, err := link.ParseSubscriptionBody(body)
	if err != nil {
		return err
	}
	if len(outbounds) == 0 {
		return errors.New("the body holds no share link that parses")
	}
	for _, ob := range outbounds {
		if raw, err := json.Marshal(ob); err == nil && bytes.Contains(raw, []byte(credential)) {
			return nil
		}
	}
	return fmt.Errorf("none of the %d share links carries the test client's credentials", len(outbounds))
}

func (c *SubRotationCheck) expectGone(ctx context.Context, url string) error {
	status, _, body, err := c.fetch(ctx, url)
	if err != nil {
		return err
	}
	if status != http.StatusNotFound {
		return fmt.Errorf("HTTP %d, %d bytes: the old link still answers", status, len(strings.TrimSpace(string(body))))
	}
	return nil
}
