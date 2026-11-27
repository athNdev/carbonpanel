// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/athNdev/carbon-panel/internal/activity"
	"github.com/athNdev/carbon-panel/internal/auth"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/worldterrain"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

const (
	auditEventOverlaySet      = "world_view.overlay.set"
	auditEventObservePlayers  = "world_view.observe_players"
	auditSubjectTypeServer    = "server"
	observationSourceResolver = "worldview.resolver"
	observationSourcePanel    = "panel.visibility-flag"
)

// Store is the persisted-data seam used by the service. *storage.Store
// satisfies it; no new global state is introduced.
type Store interface {
	ListServers(ctx context.Context) ([]*storage.Server, error)
	GetServer(ctx context.Context, id string) (*storage.Server, error)
	GetServerConfig(ctx context.Context, id string) (*storage.ServerConfig, error)
	SubuserPermissions(ctx context.Context, serverID, userID string) []string
}

// ServiceDeps are the dependencies of the WorldViewService.
type ServiceDeps struct {
	Store      Store
	Authorizer Authorizer
	Resolver   *Resolver
	Sessions   *SessionRegistry
	Native     *NativeMap
	Overlays   *OverlayStore
	Auditor    AuditLogger
	Log        Logger
}

// Service implements the generated WorldViewServiceHandler. It is observation
// only: map reads are filtered per server, player observation and overlay
// edits use distinct actions, and all mutations are audited.
type Service struct {
	deps ServiceDeps
}

func NewService(deps ServiceDeps) *Service {
	if deps.Sessions == nil {
		deps.Sessions = NewSessionRegistry()
	}
	if deps.Overlays == nil {
		deps.Overlays = NewOverlayStore()
	}
	return &Service{deps: deps}
}

// SessionRegistry exposes the registry to the composition root so the
// same-origin proxy validates the exact keys this service issued.
func (s *Service) SessionRegistry() *SessionRegistry { return s.deps.Sessions }

func (s *Service) ListWorldViewTargets(ctx context.Context, _ *connect.Request[v1.ListWorldViewTargetsRequest]) (*connect.Response[v1.ListWorldViewTargetsResponse], error) {
	user := auth.GetUserFromContext(ctx)
	if user == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
	}
	servers, err := s.deps.Store.ListServers(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list servers: %w", err))
	}
	targets := make([]*v1.WorldViewTarget, 0, len(servers))
	budget := nativeInventoryBudget{
		remaining: nativeInventoryProbeLimit,
		timeLeft:  nativeInventoryProbeWindow,
	}
	for _, server := range servers {
		if server == nil || !s.authorized(ctx, user, server.ID, rbac.ActionRead) {
			continue
		}
		target, _, _ := s.targetFor(ctx, server, nil, &budget)
		targets = append(targets, target)
	}
	return connect.NewResponse(&v1.ListWorldViewTargetsResponse{Targets: targets}), nil
}

func (s *Service) GetWorldViewStatus(ctx context.Context, req *connect.Request[v1.GetWorldViewStatusRequest]) (*connect.Response[v1.GetWorldViewStatusResponse], error) {
	_, serverID, err := s.requestContext(ctx, req.Msg.GetServerId(), rbac.ActionRead)
	if err != nil {
		return nil, err
	}
	server, err := s.deps.Store.GetServer(ctx, serverID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found: %w", err))
	}
	ref := req.Msg.GetWorldRef()
	if ref != nil && ref.GetServerId() != "" && ref.GetServerId() != serverID {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("world_ref.server_id does not match server_id"))
	}
	target, _, probeErr := s.targetFor(ctx, server, ref, nil)
	if probeErr != nil {
		return nil, probeErr
	}
	return connect.NewResponse(&v1.GetWorldViewStatusResponse{Target: target}), nil
}

func (s *Service) GetWorldViewSession(ctx context.Context, req *connect.Request[v1.GetWorldViewSessionRequest]) (*connect.Response[v1.GetWorldViewSessionResponse], error) {
	user, serverID, err := s.requestContext(ctx, req.Msg.GetServerId(), rbac.ActionRead)
	if err != nil {
		return nil, err
	}
	server, err := s.deps.Store.GetServer(ctx, serverID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found: %w", err))
	}
	ref := req.Msg.GetWorldRef()
	if ref != nil && ref.GetServerId() != "" && ref.GetServerId() != serverID {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("world_ref.server_id does not match server_id"))
	}
	target, native, probeErr := s.targetFor(ctx, server, ref, nil)
	if probeErr != nil {
		return nil, probeErr
	}
	state := target.GetRendererState()
	// A STALE renderer is still reachable (its health probe succeeded): its
	// last-known tiles can be served, so the workspace must receive a session
	// for the honest last-good view promised by ADR 0015. STOPPED/ERROR/ABSENT
	// never get a key because there is no reachable renderer to serve assets.
	// A static snapshot has no reachable tile proxy: keep its honest STALE
	// state but do not offer an iframe that could never load.
	staticWithoutProxy := state == v1.RendererState_RENDERER_STATE_STALE &&
		target.GetRendererType() == v1.RendererType_RENDERER_TYPE_STATIC
	canOpen := state == v1.RendererState_RENDERER_STATE_READY ||
		state == v1.RendererState_RENDERER_STATE_STALE ||
		state == v1.RendererState_RENDERER_STATE_NATIVE_AVAILABLE
	if !canOpen || staticWithoutProxy {
		reason := target.GetError()
		if reason == "" {
			reason = "renderer is not ready; no map session was issued"
		}
		if staticWithoutProxy {
			reason = "offline tile snapshot has no map proxy; last-known freshness only"
		}
		return connect.NewResponse(&v1.GetWorldViewSessionResponse{State: state, Reason: reason}), nil
	}

	discovered := target.GetFreshness().GetSource() == discoveredSource
	key, expires := s.deps.Sessions.issue(serverID, worldKey(target.GetWorldRef()), user.ID, DefaultSessionTTL, native, discovered)
	if key == "" {
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not issue a map session key"))
	}
	reason := ""
	if state == v1.RendererState_RENDERER_STATE_STALE {
		reason = "last-known tiles; the renderer reports a stale render"
	}
	return connect.NewResponse(&v1.GetWorldViewSessionResponse{
		ProxyPath: ProxyPrefix + key + "/",
		ExpiresAt: timestamppb.New(expires),
		State:     state,
		Reason:    reason,
	}), nil
}

func (s *Service) ListCapabilities(ctx context.Context, req *connect.Request[v1.ListCapabilitiesRequest]) (*connect.Response[v1.ListCapabilitiesResponse], error) {
	_, serverID, err := s.requestContext(ctx, req.Msg.GetServerId(), rbac.ActionRead)
	if err != nil {
		return nil, err
	}
	server, err := s.deps.Store.GetServer(ctx, serverID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found: %w", err))
	}
	renderer, resolveErr := s.resolver().Resolve(ctx, canonicalNodeID(server.NodeID), serverID)
	fallback := resolveErr
	if discovered, ok := renderer.(*discoveredRenderer); ok {
		h, err := discovered.Health(ctx)
		if !discoveredReady(h, err) {
			fallback = ErrRendererUnavailable
		}
	}
	if fallback != nil || renderer == nil {
		_, nativeErr := s.nativeScopeFor(ctx, server, nil, fallback, nil)
		if nativeErr == nil {
			return connect.NewResponse(&v1.ListCapabilitiesResponse{
				RendererType: v1.RendererType_RENDERER_TYPE_NATIVE,
				Capabilities: nativeCapabilities(),
			}), nil
		}
		if transientErr := nativeTransientError(nativeErr); transientErr != nil {
			return nil, transientErr
		}
		if renderer != nil {
			return connect.NewResponse(&v1.ListCapabilitiesResponse{
				RendererType: renderer.Type(), Capabilities: renderer.Capabilities(),
			}), nil
		}
		detail := "renderer is not configured; map capabilities are unavailable"
		if errors.Is(nativeErr, worldterrain.ErrTerrainUnsupported) {
			detail = nativeFormatUnsupported
		} else if errors.Is(resolveErr, ErrUnsupportedRenderer) {
			detail = "the configured renderer has no verified adapter"
		} else if errors.Is(resolveErr, ErrInvalidEndpoint) {
			detail = "renderer endpoint configuration is invalid for this node"
		} else if resolveErr != nil && !errors.Is(resolveErr, ErrRendererUnavailable) {
			detail = "renderer is temporarily unavailable; see panel logs"
		}
		caps := make([]*v1.Capability, 0, len(AllCapabilityNames))
		for _, name := range AllCapabilityNames {
			caps = append(caps, capability(name, false, detail))
		}
		return connect.NewResponse(&v1.ListCapabilitiesResponse{Capabilities: caps}), nil
	}
	return connect.NewResponse(&v1.ListCapabilitiesResponse{
		RendererType: renderer.Type(),
		Capabilities: renderer.Capabilities(),
	}), nil
}

func (s *Service) ListMapOverlays(ctx context.Context, req *connect.Request[v1.ListMapOverlaysRequest]) (*connect.Response[v1.ListMapOverlaysResponse], error) {
	user, serverID, err := s.requestContext(ctx, req.Msg.GetServerId(), rbac.ActionRead)
	if err != nil {
		return nil, err
	}
	ref := req.Msg.GetWorldRef()
	if ref == nil || worldKey(ref) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("world_ref is required"))
	}
	if ref.GetServerId() != "" && ref.GetServerId() != serverID {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("world_ref.server_id does not match server_id"))
	}
	all := s.deps.Overlays.List(serverID, worldKey(ref), req.Msg.GetDimension())
	mayObserve := s.authorized(ctx, user, serverID, rbac.ActionObservePlayers)
	out := make([]*v1.MapOverlay, 0, len(all))
	for _, overlay := range all {
		if overlay.GetSensitive() && !mayObserve {
			continue
		}
		out = append(out, overlay)
	}
	return connect.NewResponse(&v1.ListMapOverlaysResponse{Overlays: out}), nil
}

func (s *Service) SetMapOverlay(ctx context.Context, req *connect.Request[v1.SetMapOverlayRequest]) (*connect.Response[v1.SetMapOverlayResponse], error) {
	user, serverID, err := s.requestContext(ctx, req.Msg.GetServerId(), rbac.ActionUpdate)
	if err != nil {
		return nil, err
	}
	overlay := req.Msg.GetOverlay()
	if overlay == nil || overlay.GetWorldRef() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("overlay with a world_ref is required"))
	}
	if refServer := overlay.GetWorldRef().GetServerId(); refServer != "" && refServer != serverID {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("overlay world_ref.server_id does not match server_id"))
	}
	stored := cloneOverlay(overlay)
	stored.ServerId = serverID
	stored.WorldRef.ServerId = serverID
	if stored.Id == "" {
		stored.Id = uuid.NewString()
	}
	stored.UpdatedAt = timestamppb.New(time.Now().UTC())
	properties, _ := json.Marshal(map[string]any{
		"overlay_id": stored.Id, "world_key": worldKey(stored.WorldRef),
		"dimension": stored.Dimension, "kind": stored.Kind, "sensitive": stored.Sensitive,
	})
	// Fail closed: an unaudited mutation must never happen. The audit write
	// precedes the in-memory mutation, and Set cannot fail for a validated
	// overlay (the only error is a nil entry, already rejected above).
	if err := s.audit(activity.Entry{
		ActorID: user.ID, ActorName: user.Username,
		Event: auditEventOverlaySet, SubjectTyp: auditSubjectTypeServer, SubjectID: serverID,
		Properties: string(properties),
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("overlay change was not audited: %w", err))
	}
	saved, err := s.deps.Overlays.Set(stored)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&v1.SetMapOverlayResponse{Overlay: saved}), nil
}

func (s *Service) SetPlayerVisibility(ctx context.Context, req *connect.Request[v1.SetPlayerVisibilityRequest]) (*connect.Response[v1.SetPlayerVisibilityResponse], error) {
	user, serverID, err := s.requestContext(ctx, req.Msg.GetServerId(), rbac.ActionObservePlayers)
	if err != nil {
		return nil, err
	}
	ref := req.Msg.GetWorldRef()
	if ref == nil || worldKey(ref) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("world_ref is required"))
	}
	if ref.GetServerId() != "" && ref.GetServerId() != serverID {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("world_ref.server_id does not match server_id"))
	}
	properties, _ := json.Marshal(map[string]any{
		"world_key": worldKey(ref), "visible": req.Msg.GetVisible(),
	})
	if err := s.audit(activity.Entry{
		ActorID: user.ID, ActorName: user.Username,
		Event: auditEventObservePlayers, SubjectTyp: auditSubjectTypeServer, SubjectID: serverID,
		Properties: string(properties),
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("player visibility change was not audited: %w", err))
	}
	return connect.NewResponse(&v1.SetPlayerVisibilityResponse{
		Visible: req.Msg.GetVisible(),
		Observation: &v1.Observation{
			Source: observationSourcePanel, ObservedAt: timestamppb.New(time.Now().UTC()),
			Applicability: "panel-only player visibility flag",
			Confidence:    "unknown",
			Note:          "this flag is not a verified renderer observation; live positions require an explicitly configured player bridge feed",
		},
	}), nil
}

// requestContext re-checks the server scope inside the handler. The interceptor
// already checked it; this is defense in depth for handlers that can be called
// directly and keeps subuser grants consistent with ListWorldViewTargets.
func (s *Service) requestContext(ctx context.Context, serverID, action string) (*auth.AuthenticatedUser, string, error) {
	user := auth.GetUserFromContext(ctx)
	if user == nil {
		return nil, "", connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
	}
	if strings.TrimSpace(serverID) == "" {
		return nil, "", connect.NewError(connect.CodeInvalidArgument, errors.New("server_id is required"))
	}
	if !s.authorized(ctx, user, serverID, action) {
		return nil, "", connect.NewError(connect.CodePermissionDenied, fmt.Errorf("insufficient permissions for %s/%s", rbac.ResourceWorldView, action))
	}
	return user, serverID, nil
}

func (s *Service) authorized(ctx context.Context, user *auth.AuthenticatedUser, serverID, action string) bool {
	if user == nil || serverID == "" || s.deps.Authorizer == nil {
		return false
	}
	allowed, err := s.deps.Authorizer.Enforce(user.Roles, rbac.ResourceWorldView, action, serverID)
	if err != nil {
		s.logError("worldview: RBAC error: %v", err)
		return false
	}
	if !allowed && s.deps.Store != nil {
		want := rbac.ResourceWorldView + "." + action
		for _, p := range s.deps.Store.SubuserPermissions(ctx, serverID, user.ID) {
			if p == want {
				allowed = true
				break
			}
		}
	}
	return allowed
}

// targetFor projects a map only after verifying its active source. A fleet
// inventory can defer format checks while keeping the active world link.
func (s *Service) targetFor(ctx context.Context, server *storage.Server, requested *v1.WorldRef, budget *nativeInventoryBudget) (*v1.WorldViewTarget, nativeScope, error) {
	nodeID := canonicalNodeID(server.NodeID)
	var ref *v1.WorldRef
	if requested == nil {
		levelName := ""
		if s.deps.Store != nil {
			if cfg, cfgErr := s.deps.Store.GetServerConfig(ctx, server.ID); cfgErr == nil {
				levelName = cfg.ActualLevelName()
			}
		}
		ref = &v1.WorldRef{Kind: v1.WorldRef_KIND_SERVER_LOCAL, ServerId: server.ID, LevelName: levelName}
	} else {
		ref = proto.Clone(requested).(*v1.WorldRef)
		ref.ServerId = server.ID
	}

	target := &v1.WorldViewTarget{
		ServerId:      server.ID,
		WorldRef:      ref,
		Name:          server.Name,
		NodeId:        nodeID,
		RendererType:  v1.RendererType_RENDERER_TYPE_UNSPECIFIED,
		RendererState: v1.RendererState_RENDERER_STATE_ABSENT,
		Renderer: &v1.RendererDescriptor{
			Type:        v1.RendererType_RENDERER_TYPE_UNSPECIFIED,
			WebBasePath: ProxyPrefix,
		},
		Freshness: &v1.Observation{
			Source: observationSourceResolver, Applicability: "renderer endpoint configuration",
			Confidence: "unknown",
			Note:       "no renderer endpoint is configured for this server/node; install and configure a renderer before viewing maps",
		},
	}

	renderer, err := s.resolver().Resolve(ctx, nodeID, server.ID)
	var h *Health
	var healthErr error
	fallback := err
	if discovered, ok := renderer.(*discoveredRenderer); ok {
		h, healthErr = discovered.Health(ctx)
		if discoveredReady(h, healthErr) {
			if discovered.matchesWorldRef(ref) {
				ref.Kind = v1.WorldRef_KIND_SERVER_LOCAL
				ref.LevelName = discovered.facts.WorldName
			} else {
				renderer = nil
				err = ErrRendererUnavailable
				fallback = err
				h = nil
			}
		} else {
			fallback = ErrRendererUnavailable
		}
	}
	if fallback != nil || renderer == nil {
		native, nativeErr := s.nativeScopeFor(ctx, server, ref, fallback, budget)
		if nativeErr == nil {
			ref.LevelName = native.level
			if ref.GetKind() == v1.WorldRef_KIND_UNSPECIFIED {
				ref.Kind = v1.WorldRef_KIND_SERVER_LOCAL
			}
			target.Dimension = "overworld"
			target.RendererType = v1.RendererType_RENDERER_TYPE_NATIVE
			target.RendererState = v1.RendererState_RENDERER_STATE_NATIVE_AVAILABLE
			target.Renderer = &v1.RendererDescriptor{Type: target.RendererType, WebBasePath: ProxyPrefix}
			for _, cap := range nativeCapabilities() {
				if cap.GetSupported() {
					target.Renderer.Capabilities = append(target.Renderer.Capabilities, cap.GetName())
				}
			}
			target.Freshness = &v1.Observation{
				Source: nativeSource, Applicability: "saved Java terrain", Confidence: "detected",
				Note: "Terrain updates after Minecraft saves its chunks.",
			}
			if h != nil {
				target.Error = h.Detail
			}
			return target, native, nil
		}

		if errors.Is(nativeErr, worldterrain.ErrTerrainUnsupported) && renderer == nil {
			target.RendererType = v1.RendererType_RENDERER_TYPE_NATIVE
			target.Renderer.Type = target.RendererType
			target.RendererState = v1.RendererState_RENDERER_STATE_UNSUPPORTED
			target.Freshness.Source = nativeSource
			target.Freshness.Applicability = "saved Java terrain"
			target.Freshness.Confidence = "detected"
			target.Freshness.Note = nativeFormatUnsupported
			target.Error = nativeFormatUnsupported
			return target, nativeScope{}, nil
		}
		if transientErr := nativeTransientError(nativeErr); transientErr != nil {
			target.RendererState = v1.RendererState_RENDERER_STATE_UNSPECIFIED
			target.Error = errNativeProbeDeferred.Error()
			if errors.Is(nativeErr, errNativeWorldBusy) {
				target.Error = errNativeWorldBusy.Error()
			}
			target.Freshness.Note = target.Error
			// A transient probe or lifecycle change must leave a direct
			// caller's last good map available while the request is retried.
			if budget == nil {
				return target, nativeScope{}, transientErr
			}
			return target, nativeScope{}, nil
		}
	}
	if renderer == nil {
		if errors.Is(err, ErrUnsupportedRenderer) {
			target.RendererState = v1.RendererState_RENDERER_STATE_UNSUPPORTED
		} else if err != nil && !errors.Is(err, ErrRendererUnavailable) {
			target.RendererState = v1.RendererState_RENDERER_STATE_ERROR
		}
		if err != nil {
			// Do not echo endpoint hosts, ports or internal errors to clients.
			reason := "renderer is temporarily unavailable; see panel logs"
			switch {
			case errors.Is(err, ErrUnsupportedRenderer):
				reason = "the configured renderer has no verified adapter"
			case errors.Is(err, ErrInvalidEndpoint):
				reason = "renderer endpoint configuration is invalid for this node"
			case errors.Is(err, ErrRendererUnavailable):
				reason = "no renderer endpoint is configured for this server/node; install and configure a renderer"
			}
			target.Error = reason
			target.Freshness.Note = reason
		}
		return target, nativeScope{}, nil
	}

	if _, ok := renderer.(*discoveredRenderer); !ok {
		h, healthErr = renderer.Health(ctx)
	}
	target.RendererType = renderer.Type()
	if desc := renderer.Descriptor(); desc != nil {
		if h != nil {
			desc.Version = h.Version
		}
		target.Renderer = desc
	}
	if h == nil {
		target.RendererState = v1.RendererState_RENDERER_STATE_ERROR
		target.Error = "renderer health probe returned no observation"
		target.Freshness.Confidence = "unknown"
		target.Freshness.Note = target.Error
		return target, nativeScope{}, nil
	}
	target.RendererState = h.State
	target.LastRender = h.LastRender
	target.Freshness = &v1.Observation{
		Source: h.Source, ObservedAt: timestamppb.New(time.Now().UTC()),
		Applicability: "renderer health probe", Confidence: h.Confidence, Note: h.Detail,
	}
	if h.State == v1.RendererState_RENDERER_STATE_ERROR || h.State == v1.RendererState_RENDERER_STATE_UNSUPPORTED {
		target.Error = h.Detail
	}
	if healthErr != nil && target.Error == "" {
		target.Error = "renderer health probe failed; see panel logs"
	}
	return target, nativeScope{}, nil
}

const nativeFormatUnsupported = "The active world is not in a supported Java Overworld save format."

func nativeTransientError(err error) error {
	switch {
	case errors.Is(err, errNativeWorldBusy):
		return connect.NewError(connect.CodeUnavailable, err)
	case errors.Is(err, errNativeProbeBusy), errors.Is(err, errNativeProbeDeferred):
		return connect.NewError(connect.CodeResourceExhausted, err)
	default:
		return nil
	}
}

func (s *Service) nativeScopeFor(ctx context.Context, server *storage.Server, ref *v1.WorldRef, resolveErr error, budget *nativeInventoryBudget) (nativeScope, error) {
	if s.deps.Native == nil || !s.resolver().allowsNativeFallback(canonicalNodeID(server.NodeID), server.ID, resolveErr) {
		return nativeScope{}, ErrRendererUnavailable
	}
	return s.deps.Native.inspect(ctx, server, ref, budget)
}

func nativeCapabilities() []*v1.Capability {
	caps := make([]*v1.Capability, 0, len(AllCapabilityNames))
	for _, name := range AllCapabilityNames {
		switch name {
		case CapabilityMap2D:
			caps = append(caps, capability(name, true, "Saved Java Overworld terrain"))
		case CapabilityMarkers:
			caps = append(caps, capability(name, true, "Saved panel markers, waypoints and points of interest on the Overworld map"))
		default:
			caps = append(caps, capability(name, false, "Native terrain does not provide this feature."))
		}
	}
	return caps
}

func cloneOverlay(overlay *v1.MapOverlay) *v1.MapOverlay {
	return proto.Clone(overlay).(*v1.MapOverlay)
}

func canonicalNodeID(nodeID string) string {
	if strings.TrimSpace(nodeID) == "" {
		return "default"
	}
	return nodeID
}

// ActualLevelName reports the same save-folder identity used by module mounts.
func ActualLevelName(cfg *storage.ServerConfig) string { return cfg.ActualLevelName() }

func (s *Service) audit(e activity.Entry) error {
	if s.deps.Auditor == nil {
		return errors.New("worldview: audit writer unavailable")
	}
	return s.deps.Auditor.Log(e)
}

func (s *Service) resolver() *Resolver {
	if s.deps.Resolver == nil {
		return &Resolver{}
	}
	return s.deps.Resolver
}

func (s *Service) logError(format string, args ...any) {
	if s.deps.Log != nil {
		s.deps.Log.Error(format, args...)
	}
}
