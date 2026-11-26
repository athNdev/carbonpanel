// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, you can obtain one at https://mozilla.org/MPL/2.0/.

// Package agent implements the outbound managed-node control transport.
package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"runtime"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/nodeinstall"
	"github.com/athNdev/carbon-panel/internal/noderuntime/enroll"
	"github.com/athNdev/carbon-panel/internal/noderuntime/native"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Config selects an explicitly qualified local daemon and an outbound TLS
// controller endpoint. StateDir belongs to the portable installer.
type Config struct {
	StateDir      string
	ControllerURL string
	ControllerCAs *x509.CertPool
	EnrollmentCA  *x509.Certificate
	EnrollmentID  string
	Facts         native.HostFacts
}

// Run enrolls an explicitly provisioned Linux node and reports preflight and
// health over mTLS until the caller cancels or the control session is refused.
// It addresses only the daemon approved in its preflight plan.
func Run(ctx context.Context, config Config) error {
	if runtime.GOOS != "linux" {
		return errors.New("the managed node agent requires Linux")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	controller, err := newController(config.ControllerURL, config.ControllerCAs, config.EnrollmentCA)
	if err != nil {
		return err
	}
	identity, key, err := nodeinstall.LoadIdentity(config.StateDir)
	if err != nil {
		return err
	}
	plan := native.BuildPlan(identity.NodeID, config.Facts, time.Now())
	if plan.Refusal != "" {
		return fmt.Errorf("managed daemon preflight refused: %s", plan.Refusal)
	}
	credential, err := nodeinstall.LoadManagedCredential(config.StateDir)
	enrolling := errors.Is(err, nodeinstall.ErrNotFound)
	if enrolling {
		if config.EnrollmentID == "" {
			return errors.New("an approved enrollment ID is required on first start")
		}
		observation, err := observeDaemon(ctx, config.Facts, plan.Daemon)
		if err != nil {
			return fmt.Errorf("initial probe of the private daemon failed: %w", err)
		}
		claim, err := controller.enroll(ctx, config.StateDir, identity.NodeID, config.EnrollmentID, activeCapabilities(plan.Capabilities, observation))
		if err != nil {
			return err
		}
		credential = &nodeinstall.ManagedCredential{ControllerURL: controller.endpoint, DaemonID: observation.id, Claim: claim}
	} else if err != nil {
		return err
	}
	certificate, err := validateCredential(credential, controller, identity, key, time.Now())
	if err != nil {
		return err
	}
	if enrolling {
		if err := nodeinstall.SaveManagedCredential(config.StateDir, credential); err != nil {
			return fmt.Errorf("persist managed credential after enrollment: %w", err)
		}
	}

	tlsConfig := controller.tls.Clone()
	tlsConfig.Certificates = []tls.Certificate{certificate}
	transport := newTransport(tlsConfig)
	defer transport.CloseIdleConnections()
	client := controlClient(controller.endpoint, transport)
	node := proto.CloneOf(credential.Claim.GetIdentity())
	node.DaemonId = credential.DaemonID
	return reportDaemon(ctx, client, node, plan, config.Facts)
}

func reportDaemon(ctx context.Context, client carbonpanelv1connect.NodeControlServiceClient, node *v1.NodeIdentity, plan native.Plan, facts native.HostFacts) error {
	caps := plan.Capabilities
	negotiation, err := client.NegotiateVersion(ctx, connect.NewRequest(&v1.NegotiateVersionRequest{
		NodeId: node.GetNodeId(),
		Endpoint: &v1.VersionNegotiation{
			AgentVersion: caps.GetAgentVersion(), ProtocolVersion: caps.GetProtocolVersion(),
			MinSupportedVersion: caps.GetMinSupportedVersion(), MaxSupportedVersion: caps.GetMaxSupportedVersion(),
		},
	}))
	if err != nil {
		return fmt.Errorf("negotiate managed control version: %w", err)
	}
	if version := negotiation.Msg.GetSelectedVersion(); !negotiation.Msg.GetSupported() ||
		version < caps.GetMinSupportedVersion() || version > caps.GetMaxSupportedVersion() {
		return errors.New("UPGRADE_REQUIRED: no supported node control protocol version")
	}
	for ctx.Err() == nil {
		start := time.Now()
		observation := observePinnedDaemon(ctx, node, plan, facts)
		latency := time.Since(start).Milliseconds()
		active := activeCapabilities(caps, observation)
		active.ProtocolVersion = negotiation.Msg.GetSelectedVersion()
		open, err := client.OpenControlSession(ctx, connect.NewRequest(&v1.OpenControlSessionRequest{Identity: node, Capabilities: active}))
		if err != nil {
			return fmt.Errorf("open managed control session: %w", err)
		}
		session := open.Msg.GetSession()
		if err := validateSession(session, node.GetNodeId(), time.Now()); err != nil {
			return err
		}
		deadline := session.GetDeadline().AsTime()
		interval := session.GetHeartbeatInterval().AsDuration()
		preflight := proto.CloneOf(plan.Report)
		preflight.ObservedAt = timestamppb.Now()
		if err := submitPreflight(ctx, client, node, active, session, preflight); err != nil {
			return err
		}
		for time.Now().Before(deadline) {
			report := &v1.NodeHealthReport{
				NodeId: node.GetNodeId(), DaemonId: node.GetDaemonId(), ObservedAt: timestamppb.Now(),
				Reachable: observation.id != "", LatencyMs: latency, Capabilities: active, Preflight: preflight,
			}
			if err := submitHealth(ctx, client, node, active, session, report); err != nil {
				return err
			}
			timer := time.NewTimer(min(interval, time.Until(deadline)))
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			if !time.Now().Before(deadline) {
				break
			}
			start = time.Now()
			observation = observePinnedDaemon(ctx, node, plan, facts)
			latency = time.Since(start).Milliseconds()
			active = activeCapabilities(caps, observation)
			active.ProtocolVersion = negotiation.Msg.GetSelectedVersion()
		}
	}
	return ctx.Err()
}

func validateSession(session *v1.ControlSession, nodeID string, now time.Time) error {
	if session.GetSessionId() == "" || session.GetNodeId() != nodeID ||
		session.GetMaxInFlight() <= 0 || session.GetMaxMessageBytes() <= 0 ||
		session.GetDeadline().CheckValid() != nil || session.GetHeartbeatInterval().CheckValid() != nil {
		return errors.New("controller returned an invalid managed control session")
	}
	lifetime := session.GetDeadline().AsTime().Sub(now)
	interval := session.GetHeartbeatInterval().AsDuration()
	if lifetime <= 0 || lifetime > 24*time.Hour || interval <= 0 || interval >= lifetime {
		return errors.New("controller returned invalid managed session timing")
	}
	return nil
}

func observePinnedDaemon(ctx context.Context, node *v1.NodeIdentity, plan native.Plan, facts native.HostFacts) daemonObservation {
	observation, err := observeDaemon(ctx, facts, plan.Daemon)
	if err != nil || observation.id != node.GetDaemonId() {
		return daemonObservation{}
	}
	return observation
}

func reportEnvelope(node *v1.NodeIdentity, caps *v1.NodeCapabilities, deadline time.Time, report proto.Message) (*v1.OperationEnvelope, error) {
	hash, err := enroll.ReportHash(report)
	if err != nil {
		return nil, err
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return nil, err
	}
	return &v1.OperationEnvelope{
		OperationId: id.String(), NodeId: node.GetNodeId(), EndpointGeneration: node.GetEndpointGeneration(),
		CredentialGeneration: node.GetCredentialGeneration(), RequestHash: hash,
		Deadline: timestamppb.New(minTime(deadline, time.Now().Add(requestTimeout))), Attempt: 1,
		CapabilitySnapshot: caps, Phase: v1.NodeOperationPhase_NODE_OPERATION_PHASE_ACCEPTED,
	}, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func submitPreflight(ctx context.Context, client carbonpanelv1connect.NodeControlServiceClient, node *v1.NodeIdentity, caps *v1.NodeCapabilities, session *v1.ControlSession, report *v1.NodePreflightReport) error {
	envelope, err := reportEnvelope(node, caps, session.GetDeadline().AsTime(), report)
	if err != nil {
		return err
	}
	request := &v1.ReportPreflightRequest{Report: report, Envelope: envelope}
	if proto.Size(request) > int(session.GetMaxMessageBytes()) {
		return errors.New("preflight report exceeds the managed control session limits")
	}
	ctx, cancel := context.WithDeadline(ctx, envelope.GetDeadline().AsTime())
	defer cancel()
	response, err := client.ReportPreflight(ctx, connect.NewRequest(request))
	if err != nil {
		return fmt.Errorf("report node preflight: %w", err)
	}
	return checkAcknowledgement(envelope, response.Msg.GetResult())
}

func submitHealth(ctx context.Context, client carbonpanelv1connect.NodeControlServiceClient, node *v1.NodeIdentity, caps *v1.NodeCapabilities, session *v1.ControlSession, report *v1.NodeHealthReport) error {
	envelope, err := reportEnvelope(node, caps, session.GetDeadline().AsTime(), report)
	if err != nil {
		return err
	}
	request := &v1.ReportHealthRequest{Report: report, Envelope: envelope}
	if proto.Size(request) > int(session.GetMaxMessageBytes()) {
		return errors.New("health report exceeds the managed control session limits")
	}
	ctx, cancel := context.WithDeadline(ctx, envelope.GetDeadline().AsTime())
	defer cancel()
	response, err := client.ReportHealth(ctx, connect.NewRequest(request))
	if err != nil {
		return fmt.Errorf("report node health: %w", err)
	}
	return checkAcknowledgement(envelope, response.Msg.GetResult())
}

func checkAcknowledgement(envelope *v1.OperationEnvelope, result *v1.OperationResult) error {
	if result.GetOperationId() != envelope.GetOperationId() || result.GetPhase() != v1.NodeOperationPhase_NODE_OPERATION_PHASE_SUCCEEDED {
		return errors.New("controller did not acknowledge the managed node report")
	}
	return nil
}
