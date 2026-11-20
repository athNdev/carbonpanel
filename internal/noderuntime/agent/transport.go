// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, you can obtain one at https://mozilla.org/MPL/2.0/.

package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/nodeinstall"
	"github.com/athNdev/carbon-panel/internal/noderuntime/enroll"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
)

const (
	requestTimeout = 10 * time.Second
	maxMessageSize = 128 << 10
)

type controller struct {
	endpoint  string
	tls       *tls.Config
	issuingCA *x509.Certificate
}

func newController(endpoint string, roots *x509.CertPool, issuingCA *x509.Certificate) (controller, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" || parsed.User != nil {
		return controller{}, errors.New("controller must be an HTTPS origin")
	}
	if roots == nil || issuingCA == nil || !issuingCA.IsCA {
		return controller{}, errors.New("controller trust roots and enrollment issuing CA are required")
	}
	parsed.Path = ""
	return controller{
		endpoint: parsed.String(),
		tls: &tls.Config{
			ServerName: parsed.Hostname(), RootCAs: roots, MinVersion: tls.VersionTLS13,
		},
		issuingCA: issuingCA,
	}, nil
}

func newTransport(config *tls.Config) *http.Transport {
	return &http.Transport{
		DialContext:            (&net.Dialer{Timeout: requestTimeout}).DialContext,
		TLSClientConfig:        config,
		ForceAttemptHTTP2:      true,
		MaxConnsPerHost:        1,
		MaxIdleConnsPerHost:    1,
		IdleConnTimeout:        90 * time.Second,
		TLSHandshakeTimeout:    requestTimeout,
		ResponseHeaderTimeout:  requestTimeout,
		MaxResponseHeaderBytes: maxMessageSize,
	}
}

func controlClient(endpoint string, transport *http.Transport) carbonpanelv1connect.NodeControlServiceClient {
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return carbonpanelv1connect.NewNodeControlServiceClient(httpClient, endpoint,
		connect.WithReadMaxBytes(maxMessageSize), connect.WithSendMaxBytes(maxMessageSize))
}

// Bootstrap admits exactly one physical TLS connection. A closed or replaced
// connection makes any later claim fail before the secret can be transmitted.
type bootstrapConnection struct {
	mu      sync.Mutex
	dialed  bool
	binding []byte
}

func (b *bootstrapConnection) dial(config *tls.Config) func(context.Context, string, string) (net.Conn, error) {
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: requestTimeout}, Config: config}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		b.mu.Lock()
		if b.dialed {
			b.mu.Unlock()
			return nil, errors.New("bootstrap TLS connection cannot be replaced")
		}
		b.dialed = true
		b.mu.Unlock()
		conn, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		state := conn.(*tls.Conn).ConnectionState()
		binding, err := enroll.TLSChannelBinding(&state)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		b.mu.Lock()
		b.binding = binding
		b.mu.Unlock()
		return conn, nil
	}
}

func (b *bootstrapConnection) matches(binding []byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.binding) > 0 && bytes.Equal(b.binding, binding)
}

func (c controller) enroll(ctx context.Context, stateDir, nodeID, enrollmentID string, caps *v1.NodeCapabilities) (*v1.ClaimEnrollmentResponse, error) {
	transport := newTransport(c.tls)
	defer transport.CloseIdleConnections()
	var connection bootstrapConnection
	transport.DialTLSContext = connection.dial(c.tls)
	client := controlClient(c.endpoint, transport)
	challenge, err := client.BeginEnrollmentChallenge(ctx, connect.NewRequest(&v1.BeginEnrollmentChallengeRequest{EnrollmentId: enrollmentID}))
	if err != nil {
		return nil, fmt.Errorf("enrollment challenge: %w", err)
	}
	if challenge.Msg.GetEnrollmentId() != enrollmentID || challenge.Msg.GetNodeId() != nodeID ||
		!connection.matches(challenge.Msg.GetChannelContext()) {
		return nil, errors.New("enrollment challenge does not match the expected node or TLS connection")
	}
	claim, err := nodeinstall.BuildLiveClaim(stateDir, challenge.Msg, caps, time.Now())
	if err != nil {
		return nil, err
	}
	response, err := client.ClaimEnrollment(ctx, connect.NewRequest(claim))
	if err != nil {
		return nil, fmt.Errorf("enrollment claim failed after local reservation (%s)", connect.CodeOf(err))
	}
	return response.Msg, nil
}
