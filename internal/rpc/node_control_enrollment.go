// SPDX-License-Identifier: MPL-2.0
package rpc

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/noderuntime/enroll"
	"github.com/athNdev/carbon-panel/internal/rbac"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	conn "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
)

type nodeEnrollmentTLSKey struct{}

type nodeEnrollmentService struct {
	conn.UnimplementedNodeControlServiceHandler
	panel    *Server
	boundary *enroll.Boundary
}

// nodeEnrollmentHandler is the enrollment surface for a dedicated TLS
// listener. The general panel RPC mux retains its fail-closed mappings.
func (s *Server) nodeEnrollmentHandler(boundary *enroll.Boundary) http.Handler {
	service := &nodeEnrollmentService{panel: s, boundary: boundary}
	_, handler := conn.NewNodeControlServiceHandler(service, connect.WithReadMaxBytes(64*1024))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case conn.NodeControlServiceCreateEnrollmentProcedure,
			conn.NodeControlServiceBeginEnrollmentChallengeProcedure,
			conn.NodeControlServiceClaimEnrollmentProcedure,
			conn.NodeControlServiceRevokeCredentialProcedure:
		default:
			http.NotFound(w, r)
			return
		}
		if r.TLS == nil || !r.TLS.HandshakeComplete || r.TLS.Version < tls.VersionTLS13 {
			http.Error(w, "Node enrollment requires direct TLS 1.3", http.StatusForbidden)
			return
		}
		if boundary == nil || boundary.Authority == nil {
			http.Error(w, "Node enrollment is unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		ctx := context.WithValue(r.Context(), nodeEnrollmentTLSKey{}, r.TLS)
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *nodeEnrollmentService) CreateEnrollment(ctx context.Context, req *connect.Request[v1.CreateEnrollmentRequest]) (*connect.Response[v1.CreateEnrollmentResponse], error) {
	if err := s.authorizeOperator(ctx, req.Header(), rbac.ActionCreate, req.Msg.GetNodeId()); err != nil {
		return nil, err
	}
	reply, err := s.boundary.CreateEnrollment(ctx, enroll.Credential{Kind: enroll.CredentialOperator}, req.Msg)
	return nodeEnrollmentResponse(reply, err)
}

func (s *nodeEnrollmentService) BeginEnrollmentChallenge(ctx context.Context, req *connect.Request[v1.BeginEnrollmentChallengeRequest]) (*connect.Response[v1.BeginEnrollmentChallengeResponse], error) {
	state, err := bootstrapTLSState(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	reply, err := s.boundary.BeginTLSEnrollment(ctx, enroll.Credential{Kind: enroll.CredentialBootstrap}, req.Msg.GetEnrollmentId(), state)
	return nodeEnrollmentResponse(reply, err)
}

func (s *nodeEnrollmentService) ClaimEnrollment(ctx context.Context, req *connect.Request[v1.ClaimEnrollmentRequest]) (*connect.Response[v1.ClaimEnrollmentResponse], error) {
	state, err := bootstrapTLSState(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	reply, err := s.boundary.ClaimTLSEnrollment(ctx, enroll.Credential{Kind: enroll.CredentialBootstrap}, req.Msg, state)
	return nodeEnrollmentResponse(reply, err)
}

func (s *nodeEnrollmentService) RevokeCredential(ctx context.Context, req *connect.Request[v1.RevokeCredentialRequest]) (*connect.Response[v1.RevokeCredentialResponse], error) {
	if err := s.authorizeOperator(ctx, req.Header(), rbac.ActionUpdate, req.Msg.GetNodeId()); err != nil {
		return nil, err
	}
	reply, err := s.boundary.RevokeCredential(ctx, enroll.Credential{Kind: enroll.CredentialOperator}, req.Msg)
	return nodeEnrollmentResponse(reply, err)
}

func (s *nodeEnrollmentService) authorizeOperator(ctx context.Context, header http.Header, action, nodeID string) error {
	state, _ := ctx.Value(nodeEnrollmentTLSKey{}).(*tls.ConnectionState)
	if state == nil || len(state.PeerCertificates) != 0 {
		return connect.NewError(connect.CodePermissionDenied, errors.New("operator domain required"))
	}
	manager, enforcer := s.panel.authManager, s.panel.enforcer
	scheme, token, ok := strings.Cut(header.Get("Authorization"), " ")
	if manager == nil || !manager.IsAnyAuthEnabled() || !ok || !strings.EqualFold(scheme, "bearer") {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("operator session required"))
	}
	user, err := manager.ValidateSession(ctx, token)
	if err != nil {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("operator session required"))
	}
	if nodeID == "" || nodeID == "*" || strings.TrimSpace(nodeID) != nodeID {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("an exact node ID is required"))
	}
	if enforcer == nil {
		return connect.NewError(connect.CodePermissionDenied, errors.New("node permission unavailable"))
	}
	allowed, err := enforcer.Enforce(user.Roles, rbac.ResourceNodes, action, nodeID)
	if err != nil || !allowed {
		return connect.NewError(connect.CodePermissionDenied, errors.New("node permission required"))
	}
	return nil
}

func bootstrapTLSState(ctx context.Context, header http.Header) (*tls.ConnectionState, error) {
	state, _ := ctx.Value(nodeEnrollmentTLSKey{}).(*tls.ConnectionState)
	if state == nil || len(state.PeerCertificates) != 0 || header.Get("Authorization") != "" || header.Get("Cookie") != "" {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("bootstrap domain required"))
	}
	return state, nil
}

func nodeEnrollmentResponse[T any](reply *T, err error) (*connect.Response[T], error) {
	if err == nil {
		return connect.NewResponse(reply), nil
	}
	switch {
	case errors.Is(err, enroll.ErrRateLimited):
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("enrollment challenge limit reached"))
	case errors.Is(err, enroll.ErrMissingProof):
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("required enrollment proof missing"))
	case errors.Is(err, enroll.ErrTopology), errors.Is(err, enroll.ErrScope), errors.Is(err, enroll.ErrRefused),
		errors.Is(err, enroll.ErrMismatchedKey), errors.Is(err, enroll.ErrStaleNonce),
		errors.Is(err, enroll.ErrReplay), errors.Is(err, enroll.ErrRevoked):
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("enrollment refused"))
	case errors.Is(err, context.Canceled):
		return nil, connect.NewError(connect.CodeCanceled, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return nil, connect.NewError(connect.CodeDeadlineExceeded, context.DeadlineExceeded)
	default:
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("enrollment unavailable"))
	}
}
