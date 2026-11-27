// SPDX-License-Identifier: MIT AND MPL-2.0
// This file includes DiscoPanel material under the MIT License,
// Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
// and still applies to that inherited material.
// Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
// If a copy of the MPL was not distributed with this file, You can obtain
// one at https://mozilla.org/MPL/2.0/.

package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/activity"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/command"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/events"
	"github.com/athNdev/carbon-panel/internal/metrics"
	"github.com/athNdev/carbon-panel/internal/minecraft"
	"github.com/athNdev/carbon-panel/internal/module"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/operation"
	"github.com/athNdev/carbon-panel/internal/proxy"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/scheduler"
	"github.com/athNdev/carbon-panel/internal/serverruntime"
	"github.com/athNdev/carbon-panel/internal/staging"
	"github.com/athNdev/carbon-panel/pkg/download"
	"github.com/athNdev/carbon-panel/pkg/files"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	"github.com/athNdev/carbon-panel/pkg/upload"
	"github.com/athNdev/carbon-panel/pkg/utils"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Compile-time check that ServerService implements the interface
var _ carbonpanelv1connect.ServerServiceHandler = (*ServerService)(nil)

// statusRefreshTimeout bounds the live Docker status refresh done by list/get
// endpoints. It is deliberately far below the panel's HTTP write timeout so a
// single unresponsive node cannot hold the response open until the server
// drops the connection; the background metrics collector and reconciler keep
// the stored status fresh, so falling back to it is safe.
const statusRefreshTimeout = 5 * time.Second

// SetStorageResolver supplies the instance-owned node storage resolver.
func (s *ServerService) SetStorageResolver(resolver *nodestorage.Resolver) {
	s.storageResolver = resolver
}

// ServerService implements the Server service
type ServerService struct {
	store            *storage.Store
	docker           *docker.Client
	storageResolver  *nodestorage.Resolver
	archiveDownloads *download.Manager
	archiveUploads   *upload.Manager
	hostMemoryMB     func(context.Context, string) (int64, error)
	hostPortBound    func(int) bool
	removeData       func(string) error
	hostPortBoundUDP func(int) bool
	capacityMu       sync.Mutex
	pool             *docker.ClientPool
	placementEngine  *docker.PlacementEngine
	sender           *command.Sender
	config           *config.Config
	proxy            *proxy.Manager
	log              *logger.Logger
	logStreamer      *logger.LogStreamer
	metricsCollector *metrics.Collector
	moduleManager    *module.Manager
	bus              *events.Bus
	enforcer         *rbac.Enforcer

	// admission is the shared in-process start gate. It is supplied by the
	// composition root and serializes same-node starts across RPC and scheduler
	// entrypoints. Nil fails closed at the acquire helper.
	admission *serverruntime.AdmissionCoordinator

	daemonInfoMu sync.Mutex
	daemonInfo   map[string]daemonEnforcementCache

	// inspectArchiveContainer is the stopped-export observation seam. It
	// returns the live container status and effective bind mounts from the
	// owning node; tests replace it so archive verification never needs a real
	// Docker daemon.
	inspectArchiveContainer func(ctx context.Context, nodeID, containerID string) (archiveContainerObservation, error)

	// archiveSubmissionMu serializes same-request archive submissions in this
	// controller so two retries cannot both reclaim and re-place one durable
	// destination.
	archiveSubmissionMu keyedMutex
}

// beginServerLifecycleTransition claims the lifecycle transition for a server
// id coming straight from a request. A missing id is an invalid argument, not a
// competing transition: without this, TryBeginServerTransition("") fails with a
// misleading "server transition already in progress". The returned handle owns
// the claim and must be released.
func beginServerLifecycleTransition(id string) (*docker.Transition, error) {
	if strings.TrimSpace(id) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("server id is required"))
	}
	transition, err := docker.TryBeginServerTransition(id)
	if err != nil {
		return nil, connect.NewError(connect.CodeAborted, err)
	}
	return transition, nil
}

// stagedPublicationError keeps reviewed-mod admission refusals as
// FailedPrecondition with their re-review guidance instead of flattening them
// into an internal lifecycle failure.
func stagedPublicationError(prefix string, err error) *connect.Error {
	if errors.Is(err, storage.ErrStagedModVersionChange) ||
		errors.Is(err, storage.ErrStagedModRuntimeTargetChanged) {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewError(connect.CodeInternal, fmt.Errorf("%s: %w", prefix, err))
}

func (s *ServerService) getDockerClient(nodeID string) *docker.Client {
	cli, err := s.getDockerClientStrict(nodeID)
	if err != nil {
		return nil
	}
	return cli
}

func (s *ServerService) recreateStagedModule(ctx context.Context, moduleID string) error {
	if s.moduleManager == nil {
		return errors.New("module recreate is not configured")
	}
	return s.moduleManager.RecreateModule(ctx, moduleID)
}

func (s *ServerService) getDockerClientStrict(nodeID string) (*docker.Client, error) {
	return docker.NewLocator(s.pool, s.docker, s.store).For(context.Background(), nodeID)
}

// checkNodeHostPort checks the game port, which is TCP, on the selected daemon.
// The controller socket table is used only when that daemon is the local node.
func (s *ServerService) checkNodeHostPort(ctx context.Context, nodeID string, port int) error {
	return s.checkNodeHostPortProtocol(ctx, nodeID, port, "tcp")
}

// checkNodeHostPortProtocol checks one protocol. A publish of the other
// protocol does not occupy this port.
func (s *ServerService) checkNodeHostPortProtocol(ctx context.Context, nodeID string, port int, protocol string) error {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "" {
		protocol = "tcp"
	}
	if protocol != "tcp" && protocol != "udp" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid protocol %s", protocol))
	}
	if port < 1 || port > 65535 {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid host port %d", port))
	}
	if s.store == nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("cannot identify host for port %d", port))
	}
	node, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("cannot identify Docker node %q for port %d: %w", nodeID, port, err))
	}
	cli, err := s.getDockerClientStrict(nodeID)
	if err != nil || cli == nil {
		if err == nil {
			err = fmt.Errorf("no Docker client available")
		}
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("cannot check port %d on Docker node %s: %w", port, node.Name, err))
	}
	allocated, err := cli.AllocatedHostPorts(ctx, protocol)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("cannot check allocated ports on Docker node %s: %w", node.Name, err))
	}
	if allocated[port] {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("port %d/%s is already in use on Docker node %s", port, protocol, node.Name))
	}
	if node.IsLocal && s.hostPortBoundOn(port, protocol) {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("port %d/%s is already in use on the local host", port, protocol))
	}
	return nil
}

func hostPorts(ports []*v1.AdditionalPort, proxied bool) []storage.HostPort {
	out := make([]storage.HostPort, 0, len(ports))
	for _, port := range ports {
		if port == nil {
			continue
		}
		out = append(out, storage.HostPort{
			Port:     int(port.HostPort),
			Protocol: port.Protocol,
			Address:  docker.AdditionalPortHostIP(proxied, port.GetExposePublic()),
		})
	}
	return out
}

func (s *ServerService) checkAdditionalHostPorts(ctx context.Context, nodeID string, ports []*v1.AdditionalPort) error {
	for _, extra := range ports {
		protocol := extra.GetProtocol()
		if protocol == "" {
			protocol = "tcp"
		}
		if err := s.checkNodeHostPortProtocol(ctx, nodeID, int(extra.GetHostPort()), protocol); err != nil {
			return err
		}
	}
	return nil
}

func (s *ServerService) isHostPortBound(port int) bool {
	if s.hostPortBound != nil {
		return s.hostPortBound(port)
	}
	return isHostPortBound(port)
}

func (s *ServerService) hostPortBoundOn(port int, protocol string) bool {
	if protocol == "udp" {
		if s.hostPortBoundUDP != nil {
			return s.hostPortBoundUDP(port)
		}
		return isHostPortBoundOn(port, "udp")
	}
	return s.isHostPortBound(port)
}

func (s *ServerService) getHostMemoryMB(ctx context.Context, nodeID string) (int64, error) {
	if s.hostMemoryMB != nil {
		return s.hostMemoryMB(ctx, nodeID)
	}
	if s.pool != nil {
		return s.pool.GetHostMemoryMB(ctx, nodeID)
	}
	cli, err := s.getDockerClientStrict(nodeID)
	if err != nil || cli == nil {
		if err == nil {
			err = fmt.Errorf("no docker client available for node %s", nodeID)
		}
		return 0, err
	}
	return cli.HostMemoryMB(ctx)
}

// checkNodeMemoryCapacity prevents operations that would exceed either the
// operator-configured node cap or the capacity reported by its Docker daemon.
// additionalMemoryMB is the new reservation for creation; starts pass zero
// because their server memory is already included in the node allocation.
func (s *ServerService) checkNodeMemoryCapacity(ctx context.Context, nodeID string, additionalMemoryMB int64) error {
	node, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("failed to load target node capacity: %w", err))
	}
	hostMemoryMB, err := s.getHostMemoryMB(ctx, nodeID)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("cannot verify memory capacity on node %s: %w", node.Name, err))
	}
	allocatedMemoryMB, _, _, err := s.store.GetNodeStats(ctx, nodeID)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("cannot verify allocated memory on node %s: %w", node.Name, err))
	}

	capacityMB := hostMemoryMB
	if node.MaxMemoryMB > 0 && node.MaxMemoryMB < capacityMB {
		capacityMB = node.MaxMemoryMB
	}
	if allocatedMemoryMB+additionalMemoryMB > capacityMB {
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("node %s lacks memory capacity (allocated %d MB + requested %d MB > limit %d MB)", node.Name, allocatedMemoryMB, additionalMemoryMB, capacityMB))
	}
	return nil
}

// SetAdmissionCoordinator supplies the shared in-process admission gate.
func (s *ServerService) SetAdmissionCoordinator(c *serverruntime.AdmissionCoordinator) {
	s.admission = c
}

// acquireNodeAdmission claims the per-node gate before a capacity check. The
// caller must defer the returned release at function scope so the gate is held
// through runtime mutation and status persistence. A refusal or cancellation
// uses the same typed-to-Connect mapping as the shared capacity check.
func (s *ServerService) acquireNodeAdmission(ctx context.Context, server *storage.Server) (func(), error) {
	release, err := s.acquireNodeAdmissionRaw(ctx, server.NodeID)
	if err == nil {
		return release, nil
	}
	var refusal *serverruntime.AdmissionError
	if errors.As(err, &refusal) {
		code := connect.CodeUnavailable
		if refusal.Kind == serverruntime.InvalidNode {
			code = connect.CodeFailedPrecondition
		}
		return nil, connect.NewError(code, err)
	}
	if errors.Is(err, context.Canceled) {
		return nil, connect.NewError(connect.CodeCanceled, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	return nil, connect.NewError(connect.CodeUnavailable, err)
}

// acquireNodeAdmissionRaw claims the shared per-node gate for an explicit node
// id. Migration starts a container on the target node, so it must contend on
// the target's key rather than the source server's current NodeID.
func (s *ServerService) acquireNodeAdmissionRaw(ctx context.Context, nodeID string) (func(), error) {
	return s.admission.Acquire(ctx, nodeID)
}

// checkServerStartCapacity ensures starting a server does not exceed the node's
// memory limit when combined with other currently active (running/starting) servers.
func (s *ServerService) checkServerStartCapacity(ctx context.Context, server *storage.Server) error {
	err := serverruntime.CheckStartCapacity(ctx, s.store, server, s.getHostMemoryMB)
	var refusal *serverruntime.AdmissionError
	if !errors.As(err, &refusal) {
		return err
	}
	code := connect.CodeUnavailable
	switch refusal.Kind {
	case serverruntime.InvalidNode:
		code = connect.CodeFailedPrecondition
	case serverruntime.CapacityExceeded:
		code = connect.CodeResourceExhausted
	}
	return connect.NewError(code, err)
}

// statusAfterContainerRecreation preserves intentional deep sleep when a
// replacement container is created stopped. Other non-running replacements
// represent a normal stopped server; a replacement that was running remains
// running (or starting, where the caller owns that transition).
func statusAfterContainerRecreation(previous storage.ServerStatus, wasRunning bool) storage.ServerStatus {
	if wasRunning {
		return storage.StatusRunning
	}
	if previous == storage.StatusDeepSleep {
		return storage.StatusDeepSleep
	}
	return storage.StatusStopped
}

func (s *ServerService) selectNodeWithHostCapacity(ctx context.Context, strategy docker.PlacementStrategy, requestedMemoryMB int64) (*storage.Node, error) {
	nodes, err := s.store.ListNodesWithStats(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query Docker nodes: %w", err)
	}

	// Probe hosts concurrently with a short per-node budget. A stale "online"
	// record with an unreachable Docker daemon must not be treated as unlimited.
	var wg sync.WaitGroup
	for _, node := range nodes {
		if !node.Enabled || node.Status != storage.NodeStatusOnline {
			continue
		}
		wg.Add(1)
		go func(node *storage.Node) {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			hostMemoryMB, err := s.getHostMemoryMB(probeCtx, node.ID)
			if err != nil {
				node.Status = storage.NodeStatusOffline
				return
			}
			if node.MaxMemoryMB == 0 || hostMemoryMB < node.MaxMemoryMB {
				node.MaxMemoryMB = hostMemoryMB
			}
		}(node)
	}
	wg.Wait()

	eligible := docker.FilterEligibleNodes(nodes, requestedMemoryMB)
	if len(eligible) == 0 {
		return nil, fmt.Errorf("no reachable Docker node has capacity for %d MB", requestedMemoryMB)
	}
	switch strategy {
	case docker.StrategyLeastServers:
		return docker.SelectLeastServers(eligible), nil
	case docker.StrategyRoundRobin:
		return s.placementEngine.SelectRoundRobin(eligible), nil
	default:
		return docker.SelectLeastMemory(eligible), nil
	}
}

// SetClientPool sets the client pool on the service
func (s *ServerService) SetClientPool(pool *docker.ClientPool) {
	s.pool = pool
}

// SetPlacementEngine sets the placement engine on the service
func (s *ServerService) SetPlacementEngine(engine *docker.PlacementEngine) {
	s.placementEngine = engine
}

// auditPower records a server power event in the audit trail (MINE-141).
// Audit failures are logged, never returned.
func (s *ServerService) auditPower(ctx context.Context, ip, event, serverID, requestID string) {
	s.auditServerLifecycle(ctx, ip, event, serverID, requestID)
}

// auditServerLifecycle records a server lifecycle event in the audit trail (MINE-141, SEC-3).
// Audit failures are logged, never returned.
func (s *ServerService) auditServerDelete(ctx context.Context, ip string, server *storage.Server, keepData, detach bool, requestID string) {
	if server == nil {
		return
	}
	actorID, actorName := "", ""
	if u := auth.GetUserFromContext(ctx); u != nil {
		actorID, actorName = u.ID, u.Username
	}
	props, err := json.Marshal(map[string]any{
		"keep_data":    keepData,
		"detach":       detach,
		"data_path":    server.DataPath,
		"container_id": server.ContainerID,
		"port":         server.Port,
	})
	if err != nil {
		s.log.Error("Audit log failed for %s: %v", activity.EventServerDelete, err)
		return
	}
	if err := activity.Log(s.store.DB(), activity.Entry{
		ActorID:    actorID,
		ActorName:  actorName,
		IP:         ip,
		Event:      activity.EventServerDelete,
		SubjectTyp: "server",
		SubjectID:  server.ID,
		Properties: string(props),
		RequestID:  strings.TrimSpace(requestID),
	}); err != nil {
		s.log.Error("Audit log failed for %s: %v", activity.EventServerDelete, err)
	}
}

func (s *ServerService) auditServerLifecycle(ctx context.Context, ip, event, serverID, requestID string) {
	actorID, actorName := "", ""
	if u := auth.GetUserFromContext(ctx); u != nil {
		actorID, actorName = u.ID, u.Username
	}
	if err := activity.Log(s.store.DB(), activity.Entry{
		ActorID:    actorID,
		ActorName:  actorName,
		IP:         ip,
		Event:      event,
		SubjectTyp: "server",
		SubjectID:  serverID,
		RequestID:  strings.TrimSpace(requestID),
	}); err != nil {
		s.log.Error("Audit log failed for %s: %v", event, err)
	}
}

// auditPrivilegedOverrides records an immutable audit log entry whenever privileged
// Docker overrides are granted to a server (SEC-3).
func (s *ServerService) auditPrivilegedOverrides(ctx context.Context, ip, serverID string, overrides *v1.DockerOverrides) {
	if !docker.IsPrivilegedDockerOverride(overrides) {
		return
	}
	actorID, actorName := "", ""
	if u := auth.GetUserFromContext(ctx); u != nil {
		actorID, actorName = u.ID, u.Username
	}
	fields := docker.PrivilegedOverrideFields(overrides)
	propsJSON, _ := json.Marshal(map[string]any{
		"action":          "grant_docker_privileged_overrides",
		"override_fields": fields,
	})
	if err := activity.Log(s.store.DB(), activity.Entry{
		ActorID:    actorID,
		ActorName:  actorName,
		IP:         ip,
		Event:      activity.EventServerPrivilegedOverrides,
		SubjectTyp: "server",
		SubjectID:  serverID,
		Properties: string(propsJSON),
	}); err != nil {
		s.log.Error("Audit log failed for %s: %v", activity.EventServerPrivilegedOverrides, err)
	}
}

// auditServerImage records a requested image change and whether the daemon
// identity of the running container moved with it.
func (s *ServerService) auditServerImage(ctx context.Context, ip, serverID, previous, requested, appliedID, repoDigest string, applied bool) {
	actorID, actorName := "", ""
	if u := auth.GetUserFromContext(ctx); u != nil {
		actorID, actorName = u.ID, u.Username
	}
	propsJSON, err := json.Marshal(map[string]any{
		"previous":            previous,
		"requested":           requested,
		"applied_image_id":    appliedID,
		"applied_repo_digest": repoDigest,
		"applied":             applied,
	})
	if err != nil {
		s.log.Error("Audit log failed for %s: %v", activity.EventServerImage, err)
		return
	}
	if err := activity.Log(s.store.DB(), activity.Entry{
		ActorID:    actorID,
		ActorName:  actorName,
		IP:         ip,
		Event:      activity.EventServerImage,
		SubjectTyp: "server",
		SubjectID:  serverID,
		Properties: string(propsJSON),
	}); err != nil {
		s.log.Error("Audit log failed for %s: %v", activity.EventServerImage, err)
	}
}

// recordAppliedImageID stores the daemon image identity after a container
// exists. A miss is logged and does not undo the container.
func (s *ServerService) recordAppliedImageID(ctx context.Context, dockerCli *docker.Client, server *storage.Server) {
	if dockerCli == nil || server == nil || s.store == nil {
		return
	}
	imageID, repoDigest, err := dockerCli.ServerImageIdentity(ctx, server)
	if err != nil {
		s.log.Error("Failed to read applied image for server %s: %v", server.ID, err)
		return
	}
	server.AppliedImageID = imageID
	server.AppliedRepoDigest = repoDigest
	if err := s.store.UpdateServerFields(ctx, server.ID, map[string]any{
		"applied_image_id":    imageID,
		"applied_repo_digest": repoDigest,
	}); err != nil {
		s.log.Error("Failed to store applied image for server %s: %v", server.ID, err)
	}
}

// NewServerService creates a new server service
func NewServerService(
	store *storage.Store,
	dockerCli *docker.Client,
	sender *command.Sender,
	config *config.Config,
	proxy *proxy.Manager,
	logStreamer *logger.LogStreamer,
	metricsCollector *metrics.Collector,
	moduleManager *module.Manager,
	bus *events.Bus,
	log *logger.Logger,
	pool *docker.ClientPool,
	placementEngine *docker.PlacementEngine,
	enforcer *rbac.Enforcer,
) *ServerService {
	if pool == nil && dockerCli != nil {
		pool = docker.NewClientPool(store, dockerCli, log)
	}
	if placementEngine == nil && store != nil {
		placementEngine = docker.NewPlacementEngine(store)
	}
	return &ServerService{
		store:            store,
		docker:           dockerCli,
		pool:             pool,
		placementEngine:  placementEngine,
		sender:           sender,
		config:           config,
		proxy:            proxy,
		log:              log,
		logStreamer:      logStreamer,
		metricsCollector: metricsCollector,
		moduleManager:    moduleManager,
		bus:              bus,
		enforcer:         enforcer,
		// Every service owns a gate by default; the composition root replaces
		// it with the one shared by RPC and scheduler starts.
		admission:  serverruntime.NewAdmissionCoordinator(),
		daemonInfo: make(map[string]daemonEnforcementCache),
	}
}

// checkDockerPrivilegedOverrides enforces that only callers holding the
// elevated ResourceServers/ActionManageDockerPrivileged permission may set
// host-breakout-capable Docker overrides (Privileged mode, CapAdd, or a
// confinement-weakening SecurityOpt). Benign overrides are always allowed.
// This is a payload-conditional check in addition to the ordinary
// create/update permission already enforced by the RPC auth interceptor.
func (s *ServerService) checkDockerPrivilegedOverrides(ctx context.Context, overrides *v1.DockerOverrides) error {
	if !docker.IsPrivilegedDockerOverride(overrides) {
		return nil
	}

	if s.enforcer == nil {
		return connect.NewError(connect.CodePermissionDenied, errors.New("privileged docker overrides require the servers/manage_docker_privileged permission, but the RBAC enforcer is unavailable"))
	}

	user := auth.GetUserFromContext(ctx)
	if user == nil {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
	}

	allowed, err := s.enforcer.Enforce(user.Roles, rbac.ResourceServers, rbac.ActionManageDockerPrivileged, "*")
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("rbac enforcement error: %w", err))
	}
	if !allowed {
		return connect.NewError(connect.CodePermissionDenied, docker.ErrElevatedDockerPermissionRequired)
	}
	return nil
}

// checkDockerOverridesConfinement rejects host-breakout-capable Docker
// overrides (host-path bind mounts outside the server's own data path, host
// device mappings, host/none networking) for ALL callers, including holders
// of the elevated docker permission. Unlike the permission-gated
// checkDockerPrivilegedOverrides above, this is a pure payload check that
// maps to CodeInvalidArgument. It must run before the overrides are persisted
// to the server record; container creation (ApplyOverrides) trusts the stored
// value and does not re-check.
func (s *ServerService) checkDockerOverridesConfinement(overrides *v1.DockerOverrides, serverDataPath string) error {
	if err := docker.ValidateDockerOverridesConfinement(overrides, docker.ServerOverrideSafeRoots(serverDataPath)...); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return nil
}

// dbServerToProto converts a database server model to proto server
func dbServerToProto(server *storage.Server) *v1.Server {
	if server == nil {
		return nil
	}

	// Convert JavaVersion string to int32
	javaVersion, _ := strconv.ParseInt(server.JavaVersion, 10, 32)

	protoServer := &v1.Server{
		Id:                      server.ID,
		Name:                    server.Name,
		Description:             server.Description,
		McVersion:               server.MCVersion,
		LoaderVersion:           server.LoaderVersion,
		Port:                    int32(server.Port),
		ProxyHostname:           server.ProxyHostname,
		ProxyListenerId:         server.ProxyListenerID,
		ProxyPort:               int32(server.ProxyPort),
		MaxPlayers:              int32(server.MaxPlayers),
		Memory:                  int32(server.Memory),
		DataPath:                server.DataPath,
		ContainerId:             server.ContainerID,
		JavaVersion:             int32(javaVersion),
		DockerImage:             server.DockerImage,
		AutoStart:               server.AutoStart,
		Detached:                server.Detached,
		TpsCommand:              server.TPSCommand,
		AutoHibernate:           server.AutoHibernate,
		IdleTimeoutMinutes:      int32(server.IdleTimeoutMinutes),
		AutoDeepSleep:           server.AutoDeepSleep,
		DeepSleepTimeoutMinutes: int32(server.DeepSleepTimeoutMinutes),
		MemoryUsage:             int64(server.MemoryUsage),
		CpuPercent:              server.CPUPercent,
		DiskUsage:               server.DiskUsage,
		DiskTotal:               server.DiskTotal,
		WorldSize:               server.WorldSize,
		PlayersOnline:           int32(server.PlayersOnline),
		Tps:                     server.TPS,
		AdditionalPorts:         server.AdditionalPorts,
		CreatedAt:               timestamppb.New(server.CreatedAt),
		UpdatedAt:               timestamppb.New(server.UpdatedAt),

		// SLP fields
		SlpAvailable:        server.SLPAvailable,
		SlpLatencyMs:        server.SLPLatencyMs,
		Motd:                server.MOTD,
		ServerVersion:       server.ServerVersion,
		ProtocolVersion:     int32(server.ProtocolVersion),
		PlayerSample:        server.PlayerSample,
		MaxPlayersSlp:       int32(server.MaxPlayersSLP),
		Favicon:             server.Favicon,
		NodeId:              server.NodeID,
		JoinObservationKind: server.JoinObservationKind,
	}

	// Apply overrides
	protoServer.DockerOverrides = server.DockerOverrides

	// Map mod loader
	protoServer.ModLoader = dbModLoaderToProto(server.ModLoader)

	// Display loader underneath a modpack platform. Empty is UNSPECIFIED, not
	// vanilla: an unresolved platform must keep showing the platform.
	if server.ModpackLoader == "" {
		protoServer.ModpackLoader = v1.ModLoader_MOD_LOADER_UNSPECIFIED
	} else {
		protoServer.ModpackLoader = dbModLoaderToProto(server.ModpackLoader)
	}

	// Map status
	protoServer.Status = dbStatusToProto(server.Status)
	protoServer.StatusReason = server.StatusReason

	// Map optional last started
	if server.LastStarted != nil {
		protoServer.LastStarted = timestamppb.New(*server.LastStarted)
	}
	if !server.JoinObservedAt.IsZero() {
		protoServer.JoinObservedAt = timestamppb.New(server.JoinObservedAt)
	}
	if !server.LastGoodJoinAt.IsZero() {
		protoServer.LastGoodJoinAt = timestamppb.New(server.LastGoodJoinAt)
	}

	return protoServer
}

// dbModLoaderToProto converts database mod loader to proto
func dbModLoaderToProto(loader storage.ModLoader) v1.ModLoader {
	switch loader {
	case storage.ModLoaderVanilla:
		return v1.ModLoader_MOD_LOADER_VANILLA
	case storage.ModLoaderForge:
		return v1.ModLoader_MOD_LOADER_FORGE
	case storage.ModLoaderFabric:
		return v1.ModLoader_MOD_LOADER_FABRIC
	case storage.ModLoaderQuilt:
		return v1.ModLoader_MOD_LOADER_QUILT
	case storage.ModLoaderPaper:
		return v1.ModLoader_MOD_LOADER_PAPER
	case storage.ModLoaderFolia:
		return v1.ModLoader_MOD_LOADER_FOLIA
	case storage.ModLoaderSpigot:
		return v1.ModLoader_MOD_LOADER_SPIGOT
	case storage.ModLoaderBukkit:
		return v1.ModLoader_MOD_LOADER_BUKKIT
	case storage.ModLoaderPurpur:
		return v1.ModLoader_MOD_LOADER_PURPUR
	case storage.ModLoaderSpongeVanilla:
		return v1.ModLoader_MOD_LOADER_SPONGE_VANILLA
	case storage.ModLoaderMohist:
		return v1.ModLoader_MOD_LOADER_MOHIST
	case storage.ModLoaderCatserver:
		return v1.ModLoader_MOD_LOADER_CATSERVER
	case storage.ModLoaderArclight:
		return v1.ModLoader_MOD_LOADER_ARCLIGHT
	case storage.ModLoaderAutoCurseForge:
		return v1.ModLoader_MOD_LOADER_AUTO_CURSEFORGE
	case storage.ModLoaderModrinth:
		return v1.ModLoader_MOD_LOADER_MODRINTH
	case storage.ModLoaderNeoForge:
		return v1.ModLoader_MOD_LOADER_NEOFORGE
	default:
		return v1.ModLoader_MOD_LOADER_VANILLA
	}
}

// protoModLoaderToDB converts proto mod loader to database
func protoModLoaderToDB(loader v1.ModLoader) storage.ModLoader {
	switch loader {
	case v1.ModLoader_MOD_LOADER_VANILLA:
		return storage.ModLoaderVanilla
	case v1.ModLoader_MOD_LOADER_FORGE:
		return storage.ModLoaderForge
	case v1.ModLoader_MOD_LOADER_FABRIC:
		return storage.ModLoaderFabric
	case v1.ModLoader_MOD_LOADER_QUILT:
		return storage.ModLoaderQuilt
	case v1.ModLoader_MOD_LOADER_PAPER:
		return storage.ModLoaderPaper
	case v1.ModLoader_MOD_LOADER_FOLIA:
		return storage.ModLoaderFolia
	case v1.ModLoader_MOD_LOADER_SPIGOT:
		return storage.ModLoaderSpigot
	case v1.ModLoader_MOD_LOADER_BUKKIT:
		return storage.ModLoaderBukkit
	case v1.ModLoader_MOD_LOADER_PURPUR:
		return storage.ModLoaderPurpur
	case v1.ModLoader_MOD_LOADER_SPONGE_VANILLA:
		return storage.ModLoaderSpongeVanilla
	case v1.ModLoader_MOD_LOADER_MOHIST:
		return storage.ModLoaderMohist
	case v1.ModLoader_MOD_LOADER_CATSERVER:
		return storage.ModLoaderCatserver
	case v1.ModLoader_MOD_LOADER_ARCLIGHT:
		return storage.ModLoaderArclight
	case v1.ModLoader_MOD_LOADER_AUTO_CURSEFORGE:
		return storage.ModLoaderAutoCurseForge
	case v1.ModLoader_MOD_LOADER_MODRINTH:
		return storage.ModLoaderModrinth
	case v1.ModLoader_MOD_LOADER_NEOFORGE:
		return storage.ModLoaderNeoForge
	default:
		return storage.ModLoaderVanilla
	}
}

// parseModLoaderString normalizes any modloader string or proto enum name to storage.ModLoader
func parseModLoaderString(s string) storage.ModLoader {
	clean := strings.TrimSpace(s)
	if clean == "" {
		return storage.ModLoaderVanilla
	}
	if val, ok := v1.ModLoader_value[clean]; ok {
		return protoModLoaderToDB(v1.ModLoader(val))
	}
	clean = strings.TrimPrefix(clean, "MOD_LOADER_")
	switch strings.ToLower(clean) {
	case "vanilla":
		return storage.ModLoaderVanilla
	case "forge":
		return storage.ModLoaderForge
	case "fabric":
		return storage.ModLoaderFabric
	case "quilt":
		return storage.ModLoaderQuilt
	case "paper":
		return storage.ModLoaderPaper
	case "folia":
		return storage.ModLoaderFolia
	case "spigot":
		return storage.ModLoaderSpigot
	case "bukkit":
		return storage.ModLoaderBukkit
	case "purpur":
		return storage.ModLoaderPurpur
	case "spongevanilla", "sponge_vanilla":
		return storage.ModLoaderSpongeVanilla
	case "mohist":
		return storage.ModLoaderMohist
	case "catserver":
		return storage.ModLoaderCatserver
	case "arclight":
		return storage.ModLoaderArclight
	case "auto_curseforge", "curseforge":
		return storage.ModLoaderAutoCurseForge
	case "modrinth":
		return storage.ModLoaderModrinth
	case "neoforge":
		return storage.ModLoaderNeoForge
	case "custom":
		return storage.ModLoaderCustom
	default:
		return storage.ModLoader(strings.ToLower(clean))
	}
}

// preserveStoredStatus reports whether the stored DB status owns the display
// value and must not be clobbered by a live Docker inspect in List/Get.
//
//   - DeepSleep: the container is EXPECTED to be stopped (exited). Docker
//     reports StatusStopped for it, which would flip the UI to OFFLINE/STOPPED
//     and hide the DEEP SLEEP state + Wake action (MINE-172). The
//     DeepSleepManager owns this state and flips it to Running on wake.
//   - Paused: the container is cgroup-frozen and still exists. A Stopped
//     reading means the daemon lost the pause state; keep PAUSED until the
//     reconciler converges rather than flapping the UI (MINE-172).
func preserveStoredStatus(stored, live storage.ServerStatus) bool {
	if stored == storage.StatusDeepSleep {
		return true
	}
	if stored == storage.StatusPaused && live == storage.StatusStopped {
		return true
	}
	if live == storage.StatusUnhealthy &&
		(stored == storage.StatusStarting || stored == storage.StatusCreating) {
		// Healthchecks fail during normal boot (image pulls and modpack
		// downloads take minutes). A starting server that fails probes is
		// still starting; let the reconciler own the transition instead of
		// flapping the UI on every list refresh.
		return true
	}
	if stored == storage.StatusError &&
		(live == storage.StatusStarting || live == storage.StatusUnhealthy || live == storage.StatusStopped) {
		// A recorded game failure owns the display until the server actually
		// recovers or a stop is recorded. Docker's stopped state alone does
		// not distinguish a clean shutdown from a failed process.
		return true
	}
	return false
}

// dbStatusToProto converts database status to proto
func dbStatusToProto(status storage.ServerStatus) v1.ServerStatus {
	switch status {
	case storage.StatusCreating:
		return v1.ServerStatus_SERVER_STATUS_CREATING
	case storage.StatusStarting:
		return v1.ServerStatus_SERVER_STATUS_STARTING
	case storage.StatusRunning:
		return v1.ServerStatus_SERVER_STATUS_RUNNING
	case storage.StatusStopping:
		return v1.ServerStatus_SERVER_STATUS_STOPPING
	case storage.StatusStopped:
		return v1.ServerStatus_SERVER_STATUS_STOPPED
	case storage.StatusError:
		return v1.ServerStatus_SERVER_STATUS_ERROR
	case storage.StatusUnhealthy:
		return v1.ServerStatus_SERVER_STATUS_UNHEALTHY
	case storage.StatusPaused:
		return v1.ServerStatus_SERVER_STATUS_PAUSED
	case storage.StatusDeepSleep:
		return v1.ServerStatus_SERVER_STATUS_DEEPSLEEP
	default:
		return v1.ServerStatus_SERVER_STATUS_UNSPECIFIED
	}
}

// ListServers lists all servers
func (s *ServerService) ListServers(ctx context.Context, req *connect.Request[v1.ListServersRequest]) (*connect.Response[v1.ListServersResponse], error) {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		s.log.Error("Failed to list servers: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list servers"))
	}

	// Get all proxy listeners once for efficiency
	var listeners map[string]*storage.ProxyListener
	if s.config.Proxy.Enabled {
		allListeners, err := s.store.GetProxyListeners(ctx)
		if err == nil {
			listeners = make(map[string]*storage.ProxyListener)
			for _, l := range allListeners {
				listeners[l.ID] = l
			}
		}
	}

	// Update status from Docker and apply cached metrics. The live refresh runs
	// under one shared budget so N servers on an unresponsive node cannot sum
	// up to N times the per-call timeout.
	statusCtx, cancelStatus := context.WithTimeout(ctx, statusRefreshTimeout)
	defer cancelStatus()

	for _, server := range servers {
		// If server uses proxy, ensure ProxyPort is populated from the listener
		if server.ProxyHostname != "" && server.ProxyListenerID != "" && listeners != nil {
			if listener, ok := listeners[server.ProxyListenerID]; ok {
				server.ProxyPort = listener.Port
			}
		}

		if server.ContainerID != "" {
			dockerCli := s.getDockerClient(server.NodeID)
			if dockerCli != nil {
				status, err := dockerCli.GetContainerStatus(statusCtx, server.ContainerID)
				if err == nil && !preserveStoredStatus(server.Status, status) {
					server.Status = status
					// Display-only: a live non-error reading retires any
					// recorded crash reason. The row itself converges on the
					// next reconciler sweep.
					if server.Status != storage.StatusError {
						server.StatusReason = ""
					}
				}
			}

			// Converge the display loader for modpack servers created before
			// it existed. Best-effort and self-terminating: resolved rows
			// skip the lookup on later reads.
			s.backfillModpackLoader(ctx, server)

			var live *metrics.ServerMetrics
			if s.metricsCollector != nil {
				live = s.metricsCollector.GetMetrics(server.ID)
			}
			applyCachedMetrics(server, live, time.Now())
		}
	}

	// Convert to proto
	protoServers := make([]*v1.Server, len(servers))
	for i, server := range servers {
		protoServers[i] = s.toProtoServer(ctx, server)
	}

	return connect.NewResponse(&v1.ListServersResponse{
		Servers: protoServers,
	}), nil
}

// GetServer gets a specific server
func (s *ServerService) GetServer(ctx context.Context, req *connect.Request[v1.GetServerRequest]) (*connect.Response[v1.GetServerResponse], error) {
	server, err := s.store.GetServer(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}

	// If server uses proxy, ensure ProxyPort is populated from the listener
	if server.ProxyHostname != "" && server.ProxyListenerID != "" {
		listener, err := s.store.GetProxyListener(ctx, server.ProxyListenerID)
		if err == nil && listener != nil {
			server.ProxyPort = listener.Port
		}
	}

	// Update status from Docker, bounded so an unresponsive node cannot outlive
	// the HTTP write timeout (the stored status is the fallback).
	if server.ContainerID != "" {
		dockerCli := s.getDockerClient(server.NodeID)
		if dockerCli != nil {
			statusCtx, cancelStatus := context.WithTimeout(ctx, statusRefreshTimeout)
			status, err := dockerCli.GetContainerStatus(statusCtx, server.ContainerID)
			cancelStatus()
			if err == nil && !preserveStoredStatus(server.Status, status) {
				server.Status = status
				if server.Status != storage.StatusError {
					server.StatusReason = ""
				}
			}
		}
	}

	// See ListServers: converge the display loader for older modpack servers.
	s.backfillModpackLoader(ctx, server)

	var live *metrics.ServerMetrics
	if s.metricsCollector != nil {
		live = s.metricsCollector.GetMetrics(server.ID)
	}
	applyCachedMetrics(server, live, time.Now())

	return connect.NewResponse(&v1.GetServerResponse{
		Server: s.toProtoServer(ctx, server),
	}), nil
}

// validateModpackServerSupport checks the provider declaration before a deployment changes server resources.
func validateModpackServerSupport(pack *storage.IndexedModpack) error {
	if pack != nil && pack.Indexer == "modrinth" && pack.ServerSide == "unsupported" {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot deploy %q: Modrinth lists this project as unsupported on dedicated servers. Choose a pack that supports server installation", pack.Name))
	}
	return nil
}

// CreateServer creates a new server
func (s *ServerService) CreateServer(ctx context.Context, req *connect.Request[v1.CreateServerRequest]) (*connect.Response[v1.CreateServerResponse], error) {
	msg := req.Msg

	serverUUID := uuid.New().String()
	var imported *archiveImport
	var archiveIntent *storage.ArchiveImport
	var archiveCleanup func() error
	var archiveRelease func()
	var archiveCleanupNeeded bool
	var archiveCommitted bool
	var archivePath string
	if msg.GetArchiveUploadId() != "" {
		archiveCtx, cancel := s.beginArchiveRequest(ctx)
		defer cancel()
		ctx = archiveCtx
		// Defaults derived from an archive belong to this invocation.
		msg = proto.CloneOf(msg)
		releaseSubmission := s.archiveSubmissionMu.Lock(uploadOwnerID(ctx) + "\x00" + strings.TrimSpace(msg.GetRequestId()))
		defer releaseSubmission()

		var prior *storage.Server
		var err error
		imported, prior, err = s.prepareArchiveImport(ctx, msg)
		if err != nil {
			return nil, err
		}
		if prior != nil {
			return connect.NewResponse(&v1.CreateServerResponse{Server: s.toProtoServer(ctx, prior)}), nil
		}
		defer func() {
			if imported.file != nil {
				_ = imported.file.Close()
			}
		}()

		if imported.claim == nil {
			// New submission: record the destination and the verified content
			// identity in the same transaction as the idempotency claim.
			archivePath = s.archiveDataPath(msg.Name, serverUUID)
			intent, claimed, err := s.store.ClaimArchiveImport(ctx, imported.replayKey, imported.replayKind, serverUUID, archivePath, imported.digest, imported.uploadID)
			if err != nil {
				if errors.Is(err, storage.ErrArchiveImportUnproven) {
					return nil, connect.NewError(connect.CodeFailedPrecondition,
						errors.New("an earlier submission was interrupted before its destination was recorded; use a new request id and inspect the destination node's server storage"))
				}
				return nil, archiveReplayError(err)
			}
			if claimed {
				archiveIntent = intent
				imported.claim = intent
			} else {
				// Another attempt won the claim between the read-only prepare
				// and this transaction. Adopt its durable intent and validate
				// the current upload against the recorded content identity.
				serverUUID = intent.ServerID
				archiveIntent = intent
				imported.claim = intent
			}
		} else {
			serverUUID = imported.claim.ServerID
			archiveIntent = imported.claim
		}

		// A committed server always wins: answer idempotently before requiring
		// the upload that may already have expired.
		committed, exists, getErr := s.committedArchiveServer(ctx, archiveIntent.ServerID)
		if getErr != nil {
			return nil, connect.NewError(connect.CodeInternal, errors.New("could not verify the archive destination state"))
		}
		if exists {
			if err := s.verifyCommittedArchiveReplay(ctx, msg.GetArchiveUploadId(), committed.ID, imported.replayKey, imported.replayKind, ""); err != nil {
				return nil, err
			}
			s.settleCommittedArchiveImport(ctx, imported.replayKey)
			return connect.NewResponse(&v1.CreateServerResponse{Server: s.toProtoServer(ctx, committed)}), nil
		}
		// Validate the supplied upload against the durable content identity
		// before any destination is reclaimed. A restart may use a fresh upload
		// id only for identical verified content.
		if imported.file == nil {
			resumed, err := s.openServerArchive(ctx, msg.GetArchiveUploadId())
			if err != nil {
				return nil, err
			}
			imported.file = resumed.file
			imported.verified = resumed.verified
			imported.digest = resumed.digest
			imported.uploadID = resumed.uploadID
		}
		if err := verifyArchiveClaimIdentity(imported, archiveIntent); err != nil {
			return nil, err
		}
		if err := applyArchiveDefaults(msg, imported.verified.Manifest.Runtime); err != nil {
			return nil, err
		}
		// Only now reclaim the destination this durable intent proves this
		// submission created.
		archiveCleanupNeeded = true
		if err := s.recoverArchiveImport(ctx, archiveIntent); err != nil {
			return nil, err
		}
		archiveCleanupNeeded = false
		committed, exists, getErr = s.committedArchiveServer(ctx, archiveIntent.ServerID)
		if getErr != nil {
			return nil, connect.NewError(connect.CodeInternal, errors.New("could not verify the archive destination state"))
		}
		if exists {
			if err := s.verifyCommittedArchiveReplay(ctx, msg.GetArchiveUploadId(), committed.ID, imported.replayKey, imported.replayKind, ""); err != nil {
				return nil, err
			}
			s.settleCommittedArchiveImport(ctx, imported.replayKey)
			return connect.NewResponse(&v1.CreateServerResponse{Server: s.toProtoServer(ctx, committed)}), nil
		}
		// A failed attempt releases the request claim only after its own
		// destination is gone. If cleanup cannot prove and remove it, the
		// durable claim and intent stay so the next retry can recover them.
		defer func() {
			// The exact allocation opened for this request is always closed,
			// whether the request committed or is rolling back.
			if archiveRelease != nil {
				defer archiveRelease()
			}
			if archiveCommitted {
				return
			}
			if archiveCleanupNeeded {
				if archiveCleanup == nil {
					return
				}
				if err := archiveCleanup(); err != nil {
					s.log.Error("Cleanup interrupted archive import %s: %v", serverUUID, err)
					return
				}
			}
			// Release the durable claim and intent atomically, and only while
			// this attempt still owns the recorded intent.
			if archiveIntent == nil {
				return
			}
			if _, err := s.store.ReleaseArchiveImportClaim(context.WithoutCancel(ctx),
				imported.replayKey, imported.replayKind, archiveIntent.ClaimToken); err != nil {
				s.log.Error("Release interrupted archive import %s: %v", serverUUID, err)
			}
		}()
		if err := s.store.CheckArchiveServerName(ctx, msg.Name); err != nil {
			return nil, archiveNameError(err)
		}
	}

	if err := s.checkDockerPrivilegedOverrides(ctx, msg.DockerOverrides); err != nil {
		return nil, err
	}

	// Convert mod loader from proto
	modLoader := protoModLoaderToDB(msg.ModLoader)

	// Display loader underneath a modpack platform; empty unless a modpack
	// resolved a real game loader below.
	var modpackLoader storage.ModLoader

	// If modpack is selected, load it and derive settings
	var modpackURL string
	modpackVersion := msg.ModpackVersionId
	if msg.ModpackId != "" {
		modpack, err := s.store.GetIndexedModpack(ctx, msg.ModpackId)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid modpack"))
		}

		if err := validateModpackServerSupport(modpack); err != nil {
			return nil, err
		}

		// Set the modpack URL
		modpackURL = modpack.WebsiteURL

		// Override mod loader based on indexer
		switch modpack.Indexer {
		case "fuego", "manual":
			modLoader = storage.ModLoaderAutoCurseForge
		case "modrinth":
			modLoader = storage.ModLoaderModrinth
		}

		// Record the real game loader for display. The container TYPE must
		// stay the platform so the image fetches the pack; the console names
		// the loader players actually run.
		modpackLoader, modpackVersion = s.resolveAttachedModpack(ctx, modpack, modpackVersion)

		// Get MC version from modpack if not explicitly set
		if msg.McVersion == "" {
			var gameVersions []string
			if err := json.Unmarshal([]byte(modpack.GameVersions), &gameVersions); err == nil && len(gameVersions) > 0 {
				msg.McVersion = minecraft.FindMostRecentMinecraftVersion(gameVersions)
			}
		}

		// A caller's memory choice is authoritative. Only fill an omitted
		// value, preferring the pack's recommendation.
		if msg.Memory <= 0 {
			msg.Memory = int32(modpack.RecommendedRAM)
			if msg.Memory <= 0 {
				msg.Memory = 4096
			}
		}
	}

	// Validate request
	if msg.Name == "" || msg.McVersion == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name and MC version are required"))
	}

	// Handle proxy configuration
	proxyHostname := msg.ProxyHostname
	proxyListenerID := msg.ProxyListenerId
	port := int(msg.Port)

	// If auto deep sleep is enabled but no proxy hostname is provided,
	// auto-generate one if proxy is enabled globally so wake-on-login works (MINE-172).
	if msg.AutoDeepSleep && proxyHostname == "" && s.config != nil && s.config.Proxy.Enabled {
		proxyConfig, _, err := s.store.GetProxyConfig(ctx)
		if err == nil && proxyConfig != nil && proxyConfig.Enabled {
			baseName := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(msg.Name), " ", "-"))
			if baseName == "" {
				baseName = "mc-server"
			}
			if proxyConfig.BaseURL != "" {
				proxyHostname = baseName + "." + proxyConfig.BaseURL
			} else {
				proxyHostname = baseName
			}
		}
	}

	if proxyHostname != "" {
		// If using base URL, append it to the hostname
		if msg.UseBaseUrl {
			proxyConfig, _, err := s.store.GetProxyConfig(ctx)
			if err == nil && proxyConfig.BaseURL != "" {
				// Only append base URL if hostname doesn't already contain a domain
				if !strings.Contains(proxyHostname, ".") {
					proxyHostname = proxyHostname + "." + proxyConfig.BaseURL
				}
			}
		}

		// Validate listener selection
		if proxyListenerID != "" {
			listener, err := s.store.GetProxyListener(ctx, proxyListenerID)
			if err != nil || !listener.Enabled {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid or disabled proxy listener"))
			}
			port = listener.Port
		} else {
			// No listener specified, get the default one
			listeners, err := s.store.GetProxyListeners(ctx)
			if err != nil || len(listeners) == 0 {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("no proxy listeners configured"))
			}

			// Find default or first enabled listener
			var defaultListener *storage.ProxyListener
			for _, l := range listeners {
				if l.IsDefault && l.Enabled {
					defaultListener = l
					break
				}
			}
			if defaultListener == nil {
				for _, l := range listeners {
					if l.Enabled {
						defaultListener = l
						break
					}
				}
			}
			if defaultListener == nil {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("no enabled proxy listeners available"))
			}
			proxyListenerID = defaultListener.ID
			port = defaultListener.Port
		}
	} else {
		// For non-proxy servers, must have a unique port
		if port == 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("port is required for non-proxy servers"))
		}

		// Check if port is already in use
		existing, err := s.store.GetServerByPort(ctx, port)
		if err != nil {
			s.log.Error("Failed to check port: %v", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check port availability"))
		}
		if existing != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("port already in use"))
		}

		// Also check if this port is used by the proxy
		if s.config.Proxy.Enabled {
			if slices.Contains(s.config.Proxy.ListenPorts, port) {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("port is already in use by the proxy server"))
			}
		}
	}

	// Determine Docker image if not specified. A provided reference is
	// normalized here so a full itzg name is not prefixed again later.
	dockerImage := msg.DockerImage
	if dockerImage == "" {
		dockerImage = docker.GetOptimalDockerTag(msg.McVersion, modLoader, false)
	} else {
		canonical, err := docker.CanonicalServerImage(dockerImage)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		dockerImage = canonical
	}

	// Validate additional ports
	var additionalPorts []*v1.AdditionalPort
	usedPorts := make(map[string]bool)

	for _, protoPort := range msg.AdditionalPorts {
		// Validate port range
		if protoPort.ContainerPort < 1 || protoPort.ContainerPort > 65535 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid container port %d", protoPort.ContainerPort))
		}
		if protoPort.HostPort < 1 || protoPort.HostPort > 65535 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid host port %d", protoPort.HostPort))
		}

		// Default protocol to TCP
		protocol := protoPort.Protocol
		if protocol == "" {
			protocol = "tcp"
		} else if protocol != "tcp" && protocol != "udp" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid protocol %s (must be tcp or udp)", protocol))
		}

		// Check for duplicate ports
		portKey := fmt.Sprintf("%d/%s", protoPort.HostPort, protocol)
		if usedPorts[portKey] {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("duplicate host port %d/%s", protoPort.HostPort, protocol))
		}
		usedPorts[portKey] = true

		// The game port is TCP. A UDP publish of the same number is a different bind.
		if protocol == "tcp" && int(protoPort.HostPort) == port {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("additional port %d conflicts with main server port", protoPort.HostPort))
		}
		if s.config.Proxy.Enabled && slices.Contains(s.config.Proxy.ListenPorts, int(protoPort.HostPort)) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("port %d is already in use by the proxy server", protoPort.HostPort))
		}

		additionalPorts = append(additionalPorts, &v1.AdditionalPort{
			ContainerPort: protoPort.ContainerPort,
			HostPort:      protoPort.HostPort,
			Protocol:      protocol,
			Name:          protoPort.Name,
			ExposePublic:  protoPort.ExposePublic,
		})
	}

	// Create server object
	loaderVersion, err := validateLoaderVersion(modLoader, msg.LoaderVersion)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	serverDataPath := archivePath
	if archiveIntent != nil {
		serverDataPath = archiveIntent.DataPath
	}
	if serverDataPath == "" {
		serverDataDir := fmt.Sprintf("%s_%s", files.SanitizePathName(msg.Name), serverUUID)
		serverDataPath = filepath.Join(s.config.Storage.DataDir, "servers", serverDataDir)
	}

	// Persist the requested provisioning intent with publication. A server
	// that was explicitly created stopped must be recorded stopped, so a crash
	// before the asynchronous container provision cannot make the reconciler
	// recreate and start it behind the operator's back.
	initialStatus := storage.StatusCreating
	if !msg.StartImmediately {
		initialStatus = storage.StatusStopped
	}
	server := &storage.Server{
		ID:              serverUUID,
		Name:            msg.Name,
		Description:     msg.Description,
		ModLoader:       modLoader,
		ModpackLoader:   modpackLoader,
		MCVersion:       msg.McVersion,
		LoaderVersion:   loaderVersion,
		Status:          initialStatus,
		Port:            port,
		ProxyHostname:   proxyHostname,
		ProxyListenerID: proxyListenerID,
		MaxPlayers:      int(msg.MaxPlayers),
		Memory:          int(msg.Memory),
		DataPath:        serverDataPath,
		JavaVersion:     docker.GetRequiredJavaVersion(msg.McVersion, modLoader),
		DockerImage:     dockerImage,
		AutoStart:       msg.AutoStart,
		Detached:        msg.Detached,
		TPSCommand:      minecraft.GetTPSCommand(modLoader),
		AutoHibernate:   msg.AutoHibernate,
		AutoDeepSleep:   msg.AutoDeepSleep,
		AdditionalPorts: additionalPorts,
		DockerOverrides: msg.DockerOverrides,
	}

	// Confinement check for the volume/device/network gap. This runs here —
	// not next to the privileged-overrides check at the top of this function
	// — because the safe roots are derived from the server's own data path,
	// which is only assigned above.
	if err := s.checkDockerOverridesConfinement(msg.DockerOverrides, serverDataPath); err != nil {
		return nil, err
	}

	// Set defaults
	if server.MaxPlayers == 0 {
		server.MaxPlayers = 20
	}
	if server.Memory == 0 {
		server.Memory = 4096
	}
	if server.ModLoader == "" {
		server.ModLoader = storage.ModLoaderVanilla
	}

	// When using proxy, remember the player-facing listener. The backend port is
	// allocated after the node is known so two servers do not publish the same bind.
	if server.ProxyHostname != "" && proxyListenerID != "" {
		listener, err := s.store.GetProxyListener(ctx, proxyListenerID)
		if err == nil && listener != nil {
			server.ProxyPort = listener.Port
		}
	}

	// Determine node placement
	nodeID := msg.NodeId
	if nodeID == "" && s.placementEngine != nil {
		strategy := docker.StrategyRoundRobin
		if msg.PlacementStrategy != "" {
			strategy = docker.PlacementStrategy(strings.ToLower(msg.PlacementStrategy))
		}
		selectedNode, err := s.selectNodeWithHostCapacity(ctx, strategy, int64(server.Memory))
		if err != nil {
			s.log.Error("Failed to select node using strategy %s: %v", strategy, err)
			return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("placement failed: %w", err))
		}
		nodeID = selectedNode.ID
	} else if nodeID != "" {
		if s.store != nil {
			node, err := s.store.GetNode(ctx, nodeID)
			if err != nil {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("specified node %s not found: %w", nodeID, err))
			}
			if !node.Enabled {
				return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("specified node %s is disabled", node.Name))
			}
		}
	}
	if nodeID == "" {
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("no Docker nodes available: register and enable a node in Settings before creating servers"))
	}
	server.NodeID = nodeID

	requestID := strings.TrimSpace(msg.GetRequestId())
	if err := operation.ValidateRequestID(requestID); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	releaseClaim := false
	if requestID != "" && imported == nil {
		owner, claimed, claimErr := s.store.ClaimIdempotencyKey(ctx, requestID, server.ID, "create")
		if claimErr != nil {
			code := connect.CodeInternal
			if errors.Is(claimErr, storage.ErrRequestIDTooLong) || errors.Is(claimErr, storage.ErrRequestIDReused) {
				code = connect.CodeInvalidArgument
			}
			return nil, connect.NewError(code, claimErr)
		}
		if !claimed {
			existing, getErr := s.store.GetServer(ctx, owner)
			if getErr != nil {
				return nil, connect.NewError(connect.CodeAborted, fmt.Errorf("create request is already in progress"))
			}
			return connect.NewResponse(&v1.CreateServerResponse{Server: s.toProtoServer(ctx, existing)}), nil
		}
		releaseClaim = true
		defer func() {
			if releaseClaim {
				_ = s.store.ReleaseIdempotencyKey(context.WithoutCancel(ctx), requestID)
			}
		}()
	}

	portsReserved := false
	if server.ProxyHostname != "" {
		used := map[int]bool{}
		dockerCli, err := s.getDockerClientStrict(server.NodeID)
		if err != nil || dockerCli == nil {
			if err == nil {
				err = fmt.Errorf("no Docker client available")
			}
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("cannot check allocated backend ports on node %s: %w", server.NodeID, err))
		}
		allocated, err := dockerCli.AllocatedHostPorts(ctx, "tcp")
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("cannot check allocated backend ports on node %s: %w", server.NodeID, err))
		}
		used = allocated
		gamePort, _, err := s.store.ReserveBackendPortPair(ctx, server.NodeID, server.ID, used)
		if err != nil {
			return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("no free backend port on node: %w", err))
		}
		server.Port = gamePort
		portsReserved = true
	}
	keepLease := false
	defer func() {
		if portsReserved && !keepLease {
			_ = s.store.ReleaseBackendPorts(context.WithoutCancel(ctx), server.ID)
		}
	}()

	// Check port availability on host and target node for non-proxy servers.
	// Additional ports use their own protocol, so a UDP publish does not
	// occupy the TCP game port.
	if server.ProxyHostname == "" && server.Port > 0 {
		if err := s.checkNodeHostPort(ctx, server.NodeID, server.Port); err != nil {
			return nil, err
		}
	}
	if err := s.checkAdditionalHostPorts(ctx, server.NodeID, server.AdditionalPorts); err != nil {
		return nil, err
	}
	if len(server.AdditionalPorts) > 0 {
		if err := s.store.ReplaceAdditionalPortLeases(ctx, server.NodeID, server.ID, hostPorts(server.AdditionalPorts, server.ProxyHostname != "")); err != nil {
			if errors.Is(err, storage.ErrPortLeaseHeld) {
				return nil, connect.NewError(connect.CodeInvalidArgument, err)
			}
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to reserve additional ports: %w", err))
		}
		portsReserved = true
	}

	// The creation task continues after this RPC returns. Claim the same
	// lifecycle transition used by start/stop/update/delete so those operations
	// cannot race container creation or its final database writes.
	transition, err := docker.TryBeginServerTransition(server.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeAborted, err)
	}
	transitionHeld := true
	defer func() {
		if transitionHeld {
			transition.Release()
		}
	}()

	if imported != nil {
		cleanup, release, err := s.placeArchive(ctx, imported, server)
		archiveCleanup = cleanup
		archiveRelease = release
		archiveCleanupNeeded = cleanup != nil
		if err != nil {
			return nil, err
		}
	}

	// Serialize capacity check and DB reservation so concurrent create
	// requests cannot both observe the same free memory and overcommit a node.
	s.capacityMu.Lock()
	if err := s.checkNodeMemoryCapacity(ctx, server.NodeID, int64(server.Memory)); err != nil {
		s.capacityMu.Unlock()
		return nil, err
	}

	// Create the world on the node's storage. A remote node with no reachable
	// mount is refused before a controller-local directory can be mistaken for
	// the world the remote daemon will use.
	if imported == nil {
		if _, err := dataRootFor(ctx, s.store, server, true, s.storageResolver); err != nil {
			s.capacityMu.Unlock()
			if nodestorage.IsUnavailable(err) {
				return nil, connect.NewError(connect.CodeUnavailable, err)
			}
			s.log.Error("Failed to create data directory: %v", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create server directory"))
		}
	}

	// A manual server pack is written through ForNode before the row is kept,
	// so a missing remote adapter cannot leave a controller-local zip.
	placedManualPack := false
	if msg.ModpackId != "" {
		placed, err := s.writeManualServerPack(ctx, server, msg.ModpackId)
		if err != nil {
			s.capacityMu.Unlock()
			if nodestorage.IsUnavailable(err) {
				return nil, connect.NewError(connect.CodeUnavailable, err)
			}
			s.log.Error("Failed to place manual server pack: %v", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to place server pack"))
		}
		placedManualPack = placed
	}

	// Save to database
	createRecord := s.store.CreateServer
	if imported != nil {
		createRecord = func(ctx context.Context, server *storage.Server) error {
			return s.store.CreateArchiveServer(ctx, server, archiveGameplaySeed(imported.verified.Gameplay),
				archiveIntent.RequestKey, imported.digest, imported.uploadID)
		}
	}
	if err := createRecord(ctx, server); err != nil {
		s.capacityMu.Unlock()
		if imported != nil {
			return nil, archiveNameError(err)
		}
		s.log.Error("Failed to create server: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create server"))
	}
	if imported != nil {
		archiveCommitted = true
	}
	keepLease = true
	releaseClaim = false
	s.capacityMu.Unlock()
	if imported != nil {
		s.finishArchiveImport(ctx, archiveIntent)
	}

	// Get the server config
	serverConfig, err := s.store.GetServerConfig(ctx, server.ID)
	if err != nil {
		s.log.Error("Failed to get server config: %v", err)
		serverConfig = s.store.CreateDefaultServerConfig(server.ID)
	}

	// Set memory configuration with dynamic headroom and OOM guard (MINE-4)
	alloc := utils.CalculateMemoryAllocation(server.Memory)
	if serverConfig.MaxMemory == nil && serverConfig.Memory == nil && serverConfig.InitMemory == nil {
		serverConfig.MaxMemory = &alloc.MaxMemoryStr
		serverConfig.InitMemory = &alloc.InitMemoryStr
	}

	if serverConfig.Memory != nil {
		serverConfig.MaxMemory = serverConfig.Memory
		serverConfig.InitMemory = serverConfig.Memory
	}

	if err := s.store.UpdateServerConfig(ctx, serverConfig); err != nil {
		s.log.Error("Failed to update server config with memory settings: %v", err)
	}

	// Configure modpack if selected
	if msg.ModpackId != "" {
		modpack, _ := s.store.GetIndexedModpack(ctx, msg.ModpackId)
		if modpack != nil && modpack.Indexer == "manual" && placedManualPack {
			cfModpackZip := "/data/modpack.zip"
			serverConfig.CFModpackZip = &cfModpackZip
			cfSlug := "manual-" + modpack.ID
			serverConfig.CFSlug = &cfSlug
		} else if modpackURL != "" && server.ModLoader == storage.ModLoaderAutoCurseForge {
			// If version is pinned, append /files/<id>
			if msg.ModpackVersionId != "" {
				versionedURL := fmt.Sprintf("%s/files/%s", modpackURL, msg.ModpackVersionId)
				serverConfig.CFPageURL = &versionedURL
			} else {
				serverConfig.CFPageURL = &modpackURL
			}
		} else if modpack != nil && modpack.Indexer == "modrinth" {
			configureModrinthModpack(serverConfig, modpack.IndexerID, modpackVersion)
		}

		// Update config with modpack settings
		if err := s.store.UpdateServerConfig(ctx, serverConfig); err != nil {
			s.log.Error("Failed to update server config with modpack settings: %v", err)
		}
	}

	// The create task can outlive this RPC. Give it a private snapshot and use
	// targeted writes for its lifecycle fields so a later UpdateServer cannot
	// be overwritten by a stale full-row Save from this goroutine.
	createServer := *server
	startImmediately := msg.StartImmediately
	go func(server *storage.Server, startImmediately bool, transition *docker.Transition) {
		defer transition.Release()
		bgCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		s.log.Info("Starting async Docker container creation for server %s", server.ID)

		s.prepareServerConfig(bgCtx, server, serverConfig)
		dockerCli, err := s.getDockerClientStrict(server.NodeID)
		if err != nil {
			s.log.Error("Failed to get docker client for node %q: %v", server.NodeID, err)
			if updateErr := s.store.UpdateServerStatus(bgCtx, server.ID, storage.StatusError); updateErr != nil {
				s.log.Error("Failed to update server status to error: %v", updateErr)
			}
			return
		}
		var containerID string
		if err := s.runJournaledMutation(bgCtx, server, "create", requestID, func() error {
			var createErr error
			containerID, createErr = dockerCli.CreateContainer(bgCtx, server, serverConfig)
			return createErr
		}); err != nil {
			s.log.Error("Failed to create container: %v", err)
			if updateErr := s.store.UpdateServerStatus(bgCtx, server.ID, storage.StatusError); updateErr != nil {
				s.log.Error("Failed to update server status to error: %v", updateErr)
			}
			return
		}

		server.ContainerID = containerID
		s.log.Info("Container created successfully for server %s: %s", server.ID, containerID)
		s.recordAppliedImageID(bgCtx, dockerCli, server)

		// Update server with container ID
		if err := s.store.UpdateServerContainerID(bgCtx, server.ID, containerID); err != nil {
			s.log.Error("Failed to update server with container ID: %v", err)
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(bgCtx), 10*time.Second)
			defer cancel()
			if cleanupErr := dockerCli.RemoveContainer(cleanupCtx, containerID); cleanupErr != nil {
				s.log.Error("Failed to remove untracked container %s after identity persistence failed: %v", containerID, cleanupErr)
			}
			if statusErr := s.store.UpdateServerStatus(cleanupCtx, server.ID, storage.StatusError); statusErr != nil {
				s.log.Error("Failed to mark server %s as errored after container identity persistence failed: %v", server.ID, statusErr)
			}
			return
		}

		// Start the container immediately if requested
		if startImmediately {
			if err := s.runJournaledMutation(bgCtx, server, "start", requestID, func() error {
				return serverruntime.Start(bgCtx, s.store, dockerCli, server)
			}); err != nil {
				s.log.Error("Failed to start container: %v", err)
				server.Status = storage.StatusError
				if updateErr := s.store.UpdateServerStatus(bgCtx, server.ID, storage.StatusError); updateErr != nil {
					s.log.Error("Failed to update server status to error: %v", updateErr)
				}
			} else {
				server.Status = storage.StatusStarting
				// Update last started time
				now := time.Now()
				server.LastStarted = &now
				// Clear ephemeral configuration fields
				if err := s.store.ClearEphemeralConfigFields(bgCtx, server.ID); err != nil {
					s.log.Error("Failed to clear ephemeral config fields: %v", err)
				}
			}
			// Update only lifecycle fields; settings may have changed since this
			// asynchronous operation began.
			fields := map[string]interface{}{"status": server.Status}
			if server.LastStarted != nil {
				fields["last_started"] = server.LastStarted
			}
			if err := s.store.UpdateServerFields(bgCtx, server.ID, fields); err != nil {
				s.log.Error("Failed to update server status: %v", err)
			}
			// Update proxy route if enabled
			if s.proxy != nil && server.ProxyHostname != "" {
				if err := s.proxy.UpdateServerRoute(server); err != nil {
					s.log.Error("Failed to update proxy route for newly created server: %v", err)
				}
			}
		} else {
			// Update status to stopped once container is ready
			server.Status = storage.StatusStopped
			if err := s.store.UpdateServerStatus(bgCtx, server.ID, server.Status); err != nil {
				s.log.Error("Failed to update server status: %v", err)
			}
			s.log.Info("Server %s created but not started immediately", server.ID)
		}
	}(&createServer, startImmediately, transition)
	transitionHeld = false // ownership transferred to the asynchronous task

	peerIP := req.Peer().Addr
	s.auditServerLifecycle(ctx, peerIP, activity.EventServerCreate, server.ID, requestID)
	if docker.IsPrivilegedDockerOverride(msg.DockerOverrides) {
		s.auditPrivilegedOverrides(ctx, peerIP, server.ID, msg.DockerOverrides)
	}

	// Return immediately with the server in its recorded provisioning state:
	// creating when a start was requested, otherwise the safe stopped state.
	return connect.NewResponse(&v1.CreateServerResponse{
		Server: s.toProtoServer(ctx, server),
	}), nil
}

// preflightReviewedModRuntimeTarget refuses an UpdateServer that would move the
// effective Docker runtime target while a reviewed mod candidate is staged. It
// runs before the write phase, so a refusal leaves the committed server and
// config rows paired; otherwise the config sync's guard would fail after the
// server row was already saved and every later read or lifecycle call would
// fail on the inconsistent pair.
func (s *ServerService) preflightReviewedModRuntimeTarget(ctx context.Context, current *storage.Server, currentConfig *storage.ServerConfig, proposed *storage.Server, proposedConfig *storage.ServerConfig) error {
	err := s.store.RefuseReviewedModRuntimeChange(ctx, current, currentConfig, proposed, proposedConfig)
	if err == nil {
		return nil
	}
	if errors.Is(err, storage.ErrStagedModVersionChange) {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check the reviewed mod runtime target: %w", err))
}

// UpdateServer updates a server
func (s *ServerService) UpdateServer(ctx context.Context, req *connect.Request[v1.UpdateServerRequest]) (*connect.Response[v1.UpdateServerResponse], error) {
	transition, err := beginServerLifecycleTransition(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	defer transition.Release()

	msg := req.Msg

	server, err := s.store.GetServer(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}
	currentConfig, err := s.store.GetServerConfig(ctx, server.ID)
	if err != nil {
		if !errors.Is(err, storage.ErrServerConfigNotFound) {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get server configuration: %w", err))
		}
		currentConfig = nil
	}

	var modpack *storage.IndexedModpack
	if msg.ModpackId != "" {
		modpack, _ = s.store.GetIndexedModpack(ctx, msg.ModpackId)
		if err := validateModpackServerSupport(modpack); err != nil {
			return nil, err
		}
	}

	// Mutate a copy so the reviewed-mod runtime-target preflight can refuse
	// before any server or config row changes. `server` stays the committed
	// state the preflight compares against.
	proposed := *server
	var proposedConfig *storage.ServerConfig
	if currentConfig != nil {
		cfg := *currentConfig
		proposedConfig = &cfg
	}
	// Check if container recreation is needed
	needsRecreation := false
	originalMemory := proposed.Memory
	originalModLoader := proposed.ModLoader
	originalMCVersion := proposed.MCVersion
	originalDockerImage := proposed.DockerImage
	if msg.DockerImage != "" && msg.DockerImage != originalDockerImage {
		canonical, imageErr := docker.CanonicalServerImage(msg.DockerImage)
		if imageErr != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, imageErr)
		}
		msg.DockerImage = canonical
	}
	capacityLockHeld := false
	defer func() {
		if capacityLockHeld {
			s.capacityMu.Unlock()
		}
	}()

	// Update fields on the proposed copy. Nothing here may write.
	memoryChanged := false
	if msg.Name != "" {
		proposed.Name = msg.Name
	}
	if msg.Description != "" {
		proposed.Description = msg.Description
	}
	if msg.Port != nil && int(*msg.Port) != proposed.Port {
		newPort := int(*msg.Port)

		if proposed.ProxyHostname != "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cannot change port for proxy-enabled servers"))
		}

		if newPort < 1 || newPort > 65535 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid port %d", newPort))
		}

		existing, err := s.store.GetServerByPort(ctx, newPort)
		if err != nil {
			s.log.Error("Failed to check port: %v", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check port availability"))
		}
		if existing != nil && existing.ID != proposed.ID {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("port already in use"))
		}

		if s.config.Proxy.Enabled && slices.Contains(s.config.Proxy.ListenPorts, newPort) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("port is already in use by the proxy server"))
		}

		if err := s.checkNodeHostPort(ctx, proposed.NodeID, newPort); err != nil {
			return nil, err
		}

		proposed.Port = newPort
		needsRecreation = true
	}
	if msg.MaxPlayers > 0 {
		proposed.MaxPlayers = int(msg.MaxPlayers)
		needsRecreation = true
	}
	if msg.Memory > 0 && int(msg.Memory) != originalMemory {
		if int(msg.Memory) > originalMemory {
			s.capacityMu.Lock()
			capacityLockHeld = true
			if err := s.checkNodeMemoryCapacity(ctx, proposed.NodeID, int64(msg.Memory)-int64(originalMemory)); err != nil {
				return nil, err
			}
		}
		proposed.Memory = int(msg.Memory)
		needsRecreation = true
		memoryChanged = true
	}
	if msg.ModLoader != "" {
		newLoader := parseModLoaderString(msg.ModLoader)
		if newLoader != originalModLoader {
			proposed.ModLoader = newLoader
			proposed.TPSCommand = minecraft.GetTPSCommand(proposed.ModLoader)
			needsRecreation = true
		}
	}
	if msg.McVersion != "" && msg.McVersion != originalMCVersion {
		proposed.MCVersion = msg.McVersion
		needsRecreation = true
	}
	if msg.LoaderVersion != nil {
		loaderVersion, err := validateLoaderVersion(proposed.ModLoader, *msg.LoaderVersion)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		if loaderVersion != proposed.LoaderVersion {
			proposed.LoaderVersion = loaderVersion
			// The loader build is baked into the container at creation, so a
			// change only takes effect on the next recreate.
			needsRecreation = true
		}
	}
	if msg.DockerImage != "" && msg.DockerImage != originalDockerImage {
		proposed.DockerImage = msg.DockerImage
		needsRecreation = true
	}
	if msg.AutoStart != nil {
		proposed.AutoStart = *msg.AutoStart
	}
	if msg.Detached != nil {
		proposed.Detached = *msg.Detached
	}
	if msg.TpsCommand != nil {
		proposed.TPSCommand = *msg.TpsCommand
	}
	if msg.AutoHibernate != nil {
		proposed.AutoHibernate = *msg.AutoHibernate
	}
	if msg.IdleTimeoutMinutes != nil {
		if int(*msg.IdleTimeoutMinutes) < 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid idle timeout %d", *msg.IdleTimeoutMinutes))
		}
		proposed.IdleTimeoutMinutes = int(*msg.IdleTimeoutMinutes)
	}
	if msg.AutoDeepSleep != nil {
		proposed.AutoDeepSleep = *msg.AutoDeepSleep
	}
	if msg.DeepSleepTimeoutMinutes != nil {
		if int(*msg.DeepSleepTimeoutMinutes) < 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid deep sleep timeout %d", *msg.DeepSleepTimeoutMinutes))
		}
		proposed.DeepSleepTimeoutMinutes = int(*msg.DeepSleepTimeoutMinutes)
	}

	var rollbackAdditional bool
	var replaceAdditionalPorts bool
	var additionalPorts []*v1.AdditionalPort
	var previousAdditionalPorts []storage.HostPort
	// Handle additional ports update
	if len(msg.AdditionalPorts) > 0 {
		// Validate additional ports
		usedPorts := make(map[string]bool)

		for _, protoPort := range msg.AdditionalPorts {
			// Validate port range
			if protoPort.ContainerPort < 1 || protoPort.ContainerPort > 65535 {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid container port %d", protoPort.ContainerPort))
			}
			if protoPort.HostPort < 1 || protoPort.HostPort > 65535 {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid host port %d", protoPort.HostPort))
			}

			// Default protocol to TCP
			protocol := protoPort.Protocol
			if protocol == "" {
				protocol = "tcp"
			} else if protocol != "tcp" && protocol != "udp" {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid protocol %s", protocol))
			}

			// Check for duplicate ports
			portKey := fmt.Sprintf("%d/%s", protoPort.HostPort, protocol)
			if usedPorts[portKey] {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("duplicate host port %d/%s", protoPort.HostPort, protocol))
			}
			usedPorts[portKey] = true

			// The game port is TCP. A UDP publish of the same number is a different bind.
			if protocol == "tcp" && int(protoPort.HostPort) == proposed.Port {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("additional port %d conflicts with main server port", protoPort.HostPort))
			}
			if s.config.Proxy.Enabled && slices.Contains(s.config.Proxy.ListenPorts, int(protoPort.HostPort)) {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("port %d is already in use by the proxy server", protoPort.HostPort))
			}

			additionalPorts = append(additionalPorts, &v1.AdditionalPort{
				ContainerPort: protoPort.ContainerPort,
				HostPort:      protoPort.HostPort,
				Protocol:      protocol,
				Name:          protoPort.Name,
				ExposePublic:  protoPort.ExposePublic,
			})
		}

		if err := s.checkAdditionalHostPorts(ctx, proposed.NodeID, additionalPorts); err != nil {
			return nil, err
		}
		proxied := proposed.ProxyHostname != ""
		previousAdditionalPorts = hostPorts(proposed.AdditionalPorts, proxied)
		replaceAdditionalPorts = true
		proposed.AdditionalPorts = additionalPorts
		needsRecreation = true
	}

	// Handle docker overrides update
	if msg.DockerOverrides != nil {
		if err := s.checkDockerPrivilegedOverrides(ctx, msg.DockerOverrides); err != nil {
			return nil, err
		}
		if err := s.checkDockerOverridesConfinement(msg.DockerOverrides, proposed.DataPath); err != nil {
			return nil, err
		}

		// Check that labels do not start with "carbon-panel."
		for key := range msg.DockerOverrides.Labels {
			if strings.HasPrefix(key, "carbon-panel.") {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("docker label keys cannot start with 'carbon-panel.', namespace reserved for internal management"))
			}
		}

		proposed.DockerOverrides = msg.DockerOverrides
		needsRecreation = true
	}

	// Handle modpack version update
	saveModpackConfig := false
	if modpack != nil {
		if proposedConfig == nil {
			proposedConfig = s.store.CreateDefaultServerConfig(proposed.ID)
		}

		modpackURL := modpack.WebsiteURL
		// A re-attached pack replaces the previous display loader.
		loader, version := s.resolveAttachedModpack(ctx, modpack, msg.ModpackVersionId)
		proposed.ModpackLoader = loader

		switch modpack.Indexer {
		case "fuego", "manual":
			proposed.ModLoader = storage.ModLoaderAutoCurseForge
			needsRecreation = true

			if msg.ModpackVersionId != "" {
				versionedURL := fmt.Sprintf("%s/files/%s", modpackURL, msg.ModpackVersionId)
				proposedConfig.CFPageURL = &versionedURL
			} else {
				proposedConfig.CFPageURL = &modpackURL
			}
		case "modrinth":
			proposed.ModLoader = storage.ModLoaderModrinth
			needsRecreation = true

			configureModrinthModpack(proposedConfig, modpack.IndexerID, version)
		}
		saveModpackConfig = true
	}

	// Refuse a runtime-target move before any server or config row changes. A
	// refused request leaves the committed pair untouched, so later reads and
	// lifecycle calls still see a consistent config.
	if err := s.preflightReviewedModRuntimeTarget(ctx, server, currentConfig, &proposed, proposedConfig); err != nil {
		return nil, err
	}

	// Commit phase: every write follows the preflight.
	if replaceAdditionalPorts {
		if err := s.store.ReplaceAdditionalPortLeases(ctx, proposed.NodeID, proposed.ID, hostPorts(additionalPorts, proposed.ProxyHostname != "")); err != nil {
			if errors.Is(err, storage.ErrPortLeaseHeld) {
				return nil, connect.NewError(connect.CodeInvalidArgument, err)
			}
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to reserve additional ports: %w", err))
		}
		rollbackAdditional = true
		defer func() {
			if rollbackAdditional {
				_ = s.store.ReplaceAdditionalPortLeases(context.WithoutCancel(ctx), proposed.NodeID, proposed.ID, previousAdditionalPorts)
			}
		}()
	}
	if saveModpackConfig {
		if err := s.store.UpdateServerConfig(ctx, proposedConfig); err != nil {
			s.log.Error("Failed to update server config with modpack settings: %v", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save modpack configuration: %w", err))
		}
	}
	if memoryChanged {
		// Runs after the modpack save so its read-modify-write preserves the
		// modpack fields.
		if err := s.store.UpdateServerConfigMemory(ctx, proposed.ID, proposed.Memory); err != nil {
			s.log.Error("Failed to update server config memory: %v", err)
		}
	}

	server = &proposed
	// Save server updates first
	if err := s.store.UpdateServer(ctx, server); err != nil {
		s.log.Error("Failed to update server: %v", err)
		if errors.Is(err, storage.ErrStagedModVersionChange) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update server"))
	}
	rollbackAdditional = false
	if capacityLockHeld {
		s.capacityMu.Unlock()
		capacityLockHeld = false
	}

	peerIP := req.Peer().Addr
	var imageApplied bool
	if server.DockerImage != originalDockerImage {
		defer func() {
			s.auditServerImage(ctx, peerIP, server.ID, originalDockerImage, server.DockerImage, server.AppliedImageID, server.AppliedRepoDigest, imageApplied)
		}()
	}
	s.auditServerLifecycle(ctx, peerIP, activity.EventServerUpdate, server.ID, "")
	if docker.IsPrivilegedDockerOverride(msg.DockerOverrides) {
		s.auditPrivilegedOverrides(ctx, peerIP, server.ID, msg.DockerOverrides)
	}

	// If container needs recreation
	if needsRecreation {
		// Get server config for container creation
		serverConfig, err := s.store.GetServerConfig(ctx, server.ID)
		if err != nil {
			s.log.Error("Failed to get server config: %v", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get server configuration"))
		}

		// Recreate container
		dockerCli, err := s.getDockerClientStrict(server.NodeID)
		if err != nil {
			s.log.Error("Docker node %q unavailable for recreation: %v", server.NodeID, err)
			server.Status = storage.StatusError
			_ = s.store.UpdateServer(ctx, server)
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("docker node %q unavailable: %w", server.NodeID, err))
		}
		s.prepareServerConfig(ctx, server, serverConfig)
		result, err := serverruntime.Recreate(ctx, s.store, dockerCli, server, serverConfig)
		if err != nil {
			s.log.Error("Failed to recreate container: %v", err)
			operationErr := fmt.Errorf("server settings were saved, but container recreation failed: %w", err)
			return nil, connect.NewError(connect.CodeInternal, operationErr)
		} else {
			server.Status = statusAfterContainerRecreation(server.Status, result.WasRunning)
			if result.ImageID != "" {
				server.AppliedImageID = result.ImageID
				server.AppliedRepoDigest = result.RepoDigest
				imageApplied = true
			}
		}

		if err := s.store.UpdateServer(ctx, server); err != nil {
			s.log.Error("Failed to update server after container recreation: %v", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("container was recreated but server state could not be saved: %w", err))
		}
	}

	return connect.NewResponse(&v1.UpdateServerResponse{
		Server: s.toProtoServer(ctx, server),
	}), nil
}

// DeleteServer deletes a server
func (s *ServerService) DeleteServer(ctx context.Context, req *connect.Request[v1.DeleteServerRequest]) (*connect.Response[v1.DeleteServerResponse], error) {
	requestID, reqErr := lifecycleRequestID(req.Msg.GetRequestId())
	if reqErr != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, reqErr)
	}
	transition, err := beginServerLifecycleTransition(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	defer transition.Release()

	server, err := s.store.GetServer(ctx, req.Msg.Id)
	if err != nil {
		if settled, serr := s.settleFinishedDisposition(ctx, req.Msg.Id); serr != nil {
			return nil, connect.NewError(connect.CodeInternal, serr)
		} else if settled {
			return connect.NewResponse(&v1.DeleteServerResponse{}), nil
		}
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}

	detach := req.Msg.GetDetach()
	keepData := detach || req.Msg.GetKeepData()
	mode := storage.DispositionDelete
	if detach {
		mode = storage.DispositionDetach
	} else if keepData {
		mode = storage.DispositionKeepData
	}
	disposition, err := s.store.BeginServerDisposition(ctx, &storage.ServerDisposition{
		ServerID: server.ID, Name: server.Name, NodeID: server.NodeID,
		ContainerID: server.ContainerID, DataPath: server.DataPath, Port: server.Port,
		Mode: mode,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to record deletion disposition: %w", err))
	}

	peerIP := req.Peer().Addr

	// One journal row covers every destructive step of a delete: modules, the
	// container, the proxy route, the world, and the panel row. An active
	// backup, restore, or migration lock refuses the delete before any of those
	// are touched. A server whose container is already gone still takes the
	// lock, so its world cannot be removed while another job owns it.
	if err := s.runJournaledMutation(ctx, server, "delete", requestID, func() error {
		// A restart can observe that the world disposition was already recorded
		// and only the panel row remains. Do not delete the world again.
		if disposition.Phase != storage.DispositionDataSettled {
			var dockerCli *docker.Client
			if server.ContainerID != "" && !detach {
				var cliErr error
				dockerCli, cliErr = s.getDockerClientStrict(server.NodeID)
				if cliErr != nil {
					return connect.NewError(connect.CodeUnavailable, fmt.Errorf("docker node %q unavailable during deletion: %w", server.NodeID, cliErr))
				}
			}

			// Child policy: a full delete stops and removes sidecar containers
			// before the parent row. keep_data drops module records but leaves
			// containers and volumes so sidecar data is not purged with
			// RemoveVolumes. Detach leaves those containers running with the
			// server container. A module cleanup or lookup failure must keep
			// the parent row so remaining resources stay addressable.
			if s.moduleManager != nil && !detach {
				modules, listErr := s.store.ListServerModules(ctx, server.ID)
				if listErr != nil {
					return fmt.Errorf("failed to list server modules before deletion: %w", listErr)
				}
				for _, mod := range modules {
					var childErr error
					if keepData {
						childErr = s.moduleManager.ForgetModule(ctx, mod.ID)
					} else {
						childErr = s.moduleManager.DeleteModule(ctx, mod.ID)
					}
					if childErr != nil {
						s.log.Error("Failed to settle module %s during deletion: %v", mod.ID, childErr)
						return fmt.Errorf("failed to settle module %s: %w", mod.ID, childErr)
					}
				}
			}

			// Stop and remove the server container before deleting its database row
			// or data directory. A failed removal must not orphan a live container.
			if server.ContainerID != "" && !detach {
				if _, stopErr := dockerCli.StopContainer(ctx, server.ContainerID); stopErr != nil {
					s.log.Warn("Failed to stop container %s during deletion; removal will still be attempted: %v", server.ContainerID, stopErr)
				}
				if rmErr := dockerCli.RemoveContainer(ctx, server.ContainerID); rmErr != nil && !errdefs.IsNotFound(rmErr) {
					return fmt.Errorf("failed to remove server container %s: %w", server.ContainerID, rmErr)
				}
			}

			// Remove proxy route or direct knock listener only after Docker resources
			// are gone. If earlier cleanup failed, the live server remains reachable
			// while the caller retries. Detach keeps the published port and does not
			// tear down the route in this call. A later panel start rebuilds routes
			// from remaining rows, so the proxy name is not kept after a restart.
			if s.proxy != nil && !detach {
				if server.ProxyHostname != "" {
					if routeErr := s.proxy.RemoveServerRoute(server.ID); routeErr != nil {
						return fmt.Errorf("failed to remove server proxy route: %w", routeErr)
					}
				} else {
					s.proxy.StopDirectKnock(server.ID)
				}
			}

			// Remove the data directory before the row. A failure leaves the server
			// addressable so the same delete can be retried. keep_data and detach
			// leave the world directory in place and still remove the panel record.
			if !keepData {
				if rmErr := s.removeServerData(ctx, server); rmErr != nil {
					s.log.Error("Failed to delete server data: %v", rmErr)
					code := connect.CodeInternal
					if nodestorage.IsUnavailable(rmErr) {
						code = connect.CodeUnavailable
					}
					return connect.NewError(code, fmt.Errorf("failed to delete server data: %w", rmErr))
				}
			}
			if phaseErr := s.store.SetServerDispositionPhase(ctx, disposition.ID, storage.DispositionDataSettled); phaseErr != nil {
				return fmt.Errorf("failed to record data disposition: %w", phaseErr)
			}
		}

		// Delete from database. Detach keeps the port lease so a new server cannot
		// publish the port the left-behind container still binds.
		deleteRecord := s.store.DeleteServer
		if detach {
			deleteRecord = s.store.DeleteServerKeepPorts
		}
		if delErr := deleteRecord(ctx, req.Msg.Id); delErr != nil {
			s.log.Error("Failed to delete server: %v", delErr)
			return fmt.Errorf("failed to delete server")
		}
		return s.store.SetServerDispositionPhase(ctx, disposition.ID, storage.DispositionSettled)
	}); err != nil {
		if errors.Is(err, storage.ErrActiveOperation) || errors.Is(err, operation.ErrInProgress) {
			return nil, connect.NewError(connect.CodeAborted, fmt.Errorf("delete refused while another operation owns the server: %w", err))
		}
		var connErr *connect.Error
		if errors.As(err, &connErr) {
			return nil, connErr
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	s.auditServerDelete(ctx, peerIP, server, keepData, detach, requestID)

	return connect.NewResponse(&v1.DeleteServerResponse{}), nil
}

// createContainerError maps a refused runtime profile to a precondition the
// caller can act on. Every other create failure keeps the generic message so
// daemon internals are not returned to the user.
func createContainerError(err error) *connect.Error {
	if errors.Is(err, docker.ErrRuntimeIncompatible) {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("this server cannot start with the requested image and runtime settings: %w", err))
	}
	return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create server container"))
}

// ResumeUnsettledDispositions finishes deletions that were recorded and then
// interrupted. A pending row continues the original delete, keep_data, or
// detach choice. A row that already recorded the world disposition only
// removes the panel record. It does not delete files a second time.
func (s *ServerService) ResumeUnsettledDispositions(ctx context.Context) (int, error) {
	rows, err := s.store.ListUnsettledDispositions(ctx)
	if err != nil {
		return 0, err
	}
	finished := 0
	for _, row := range rows {
		req := connect.NewRequest(&v1.DeleteServerRequest{
			Id:       row.ServerID,
			KeepData: row.Mode == storage.DispositionKeepData || row.Mode == storage.DispositionDetach,
			Detach:   row.Mode == storage.DispositionDetach,
		})
		if _, err := s.DeleteServer(ctx, req); err != nil {
			if s.log != nil {
				s.log.Error("Failed to resume deletion for server %s: %v", row.ServerID, err)
			}
			continue
		}
		finished++
	}
	return finished, nil
}

// settleFinishedDisposition completes a delete whose panel row is already
// gone and whose world disposition was recorded. It does not delete files.
func (s *ServerService) settleFinishedDisposition(ctx context.Context, serverID string) (bool, error) {
	disp, err := s.store.LatestServerDisposition(ctx, serverID)
	if err != nil || disp == nil {
		return false, err
	}
	if disp.Phase == storage.DispositionSettled {
		return true, nil
	}
	if disp.Phase != storage.DispositionDataSettled {
		return false, nil
	}
	if err := s.store.SetServerDispositionPhase(ctx, disp.ID, storage.DispositionSettled); err != nil {
		return false, err
	}
	return true, nil
}

// removeServerData deletes the server directory on the owning node's storage.
// A remote node without shared storage is refused so a controller-local path
// is not removed in its place.
func (s *ServerService) removeServerData(ctx context.Context, server *storage.Server) error {
	if server == nil || strings.TrimSpace(server.DataPath) == "" {
		return errors.New("server data path is empty")
	}
	info, err := storageNodeInfo(ctx, s.store, server.NodeID)
	if err != nil {
		return err
	}
	mapped, err := nodestorage.MapDataPath(info, server.DataPath)
	if err != nil {
		return err
	}
	if unsafeDataRemoval(mapped) {
		return fmt.Errorf("refusing to delete unsafe server data path %q", mapped)
	}
	if !info.IsLocal {
		root := info.ControllerStorageRoot
		if root == "" {
			root = info.StorageRoot
		}
		if root != "" && filepath.Clean(mapped) == filepath.Clean(root) {
			return fmt.Errorf("refusing to delete storage root %q", mapped)
		}
	}
	if s.removeData != nil {
		return s.removeData(mapped)
	}
	return os.RemoveAll(mapped)
}

func unsafeDataRemoval(path string) bool {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" || cleaned == "." || cleaned == string(filepath.Separator) {
		return true
	}
	rest := strings.TrimPrefix(cleaned, filepath.VolumeName(cleaned))
	rest = strings.Trim(rest, string(filepath.Separator))
	return rest == ""
}

// errLifecycleRequestCompleted refuses a replay whose journaled callback was
// suppressed: the runtime mutation already ran for this request ID and no
// staged row may be published under it.
var errLifecycleRequestCompleted = errors.New("this lifecycle request ID was already completed; retry the staged change with a new request ID")

// refuseCompletedLifecycleRequest refuses a replay of a request whose journaled
// step already completed. The journal key carries the step kind, so checking
// only the kind the current state would select would let a replay of the same
// request ID select another kind (unpause, create, recreate or in-place
// restart) and mutate the runtime. This gate spans every kind and runs before
// the first live write or dispatch.
func (s *ServerService) refuseCompletedLifecycleRequest(ctx context.Context, serverID, requestID string) error {
	completed, err := s.store.HasCompletedServerOperationForRequest(ctx, serverID, requestID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check the lifecycle request journal: %w", err))
	}
	if completed {
		return connect.NewError(connect.CodeInternal, errLifecycleRequestCompleted)
	}
	return nil
}

// A completed request ID may suppress the mutation callback. It cannot
// acknowledge fresh staged changes that were published by this attempt. The
// dispatch intent is written inside the journaled callback so a suppressed
// request cannot leave a durable dispatch for bytes that were never sent to a
// runtime.
func (s *ServerService) runPublicationMutation(ctx context.Context, publication *staging.RestartPublication, server *storage.Server, kind, requestID string, mutate func() error) error {
	ran := false
	err := s.runJournaledMutation(ctx, server, kind, requestID, func() error {
		if dispatchErr := publication.BeginDispatch(ctx); dispatchErr != nil {
			return fmt.Errorf("failed to record staged publication dispatch: %w", dispatchErr)
		}
		ran = true
		return mutate()
	})
	if err == nil && !ran && publication.HasChanges() {
		return errLifecycleRequestCompleted
	}
	return err
}

// StartServer starts a server
func (s *ServerService) StartServer(ctx context.Context, req *connect.Request[v1.StartServerRequest]) (out *connect.Response[v1.StartServerResponse], retErr error) {
	requestID, reqErr := lifecycleRequestID(req.Msg.GetRequestId())
	if reqErr != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, reqErr)
	}
	transition, err := beginServerLifecycleTransition(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	defer transition.Release()

	server, err := s.store.GetServer(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}
	if err := s.refuseActiveOperation(ctx, server.ID); err != nil {
		return nil, err
	}
	if err := s.refuseCompletedLifecycleRequest(ctx, server.ID, requestID); err != nil {
		return nil, err
	}

	dockerCli, err := s.getDockerClientStrict(server.NodeID)
	if err != nil {
		server.Status = storage.StatusError
		_ = s.store.UpdateServerStatus(ctx, server.ID, server.Status)
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("docker node %q is unavailable: %w", server.NodeID, err))
	}
	release, err := s.acquireNodeAdmission(ctx, server)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.checkServerStartCapacity(ctx, server); err != nil {
		return nil, err
	}

	// If container doesn't exist (e.g. ContainerID lost/never persisted), try to recover an
	// existing-but-untracked container before creating a duplicate (MINE-103).
	if server.ContainerID == "" {
		if resolved, resolveErr := dockerCli.ResolveContainer(ctx, server.ID); resolveErr == nil && resolved != nil {
			s.log.Info("Recovered untracked container %s for server %s via label/name resolution", resolved.ID, server.ID)
			server.ContainerID = resolved.ID
			if err := s.store.UpdateServerContainerID(ctx, server.ID, server.ContainerID); err != nil {
				s.log.Error("Failed to update server with resolved container ID: %v", err)
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update server"))
			}
		}
	}

	// Apply staged configs on restart/start (STAGED-CONFIG). Content waiting
	// for a clock time or an idle window stays staged.
	commit, fileErr := staging.CommitNextRestartFiles(ctx, s.store, server, s.recreateStagedModule, s.storageResolver)
	if fileErr != nil {
		s.log.Error("Failed to apply staged content on start for server %s: %v", server.ID, fileErr)
		return nil, stagedPublicationError("failed to apply staged content", fileErr)
	}
	// Applied content whose publication an earlier lifecycle already accepted
	// owes cleanup only. It must not trigger a new recreation; the normal start
	// below still starts the existing container when the user asked for one.
	contentReady := commit.NeedsRuntimePublication()
	publication := staging.RestartPublication{
		Commit: commit,
	}
	defer func() {
		if finishErr := publication.Resolve(ctx, s.store, retErr); finishErr != nil {
			out = nil
			retErr = connect.NewError(connect.CodeInternal, errors.Join(retErr, finishErr))
		}
	}()
	appliedStaged, err := commit.ApplyConfigs(ctx, s.store)
	publication.Configs = appliedStaged
	if err != nil {
		s.log.Error("Failed to apply staged config changes on start for server %s: %v", server.ID, err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to apply staged config changes: %w", err))
	}
	appliedNow := commit.AppliedConfigsThisAttempt()
	if n := len(appliedStaged); n > 0 {
		s.log.Info("Applied %d staged config change(s) on start for server %s", n, server.ID)
	}
	pendingRecreate, err := scheduler.HasPendingRestartStagedConfigRecreate(ctx, s.store, server.ID, server.LastStarted)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check staged config recovery: %w", err))
	}
	needsStagedRecreate := appliedNow > 0 || pendingRecreate || contentReady
	if commit.NeedsIdentityVerification() {
		// A dispatch intent is durable but unverified. Repair the tracked
		// identity if needed, then accept only the exact replacement identity
		// captured at creation. Any other canonical container is drift.
		if _, reconcileErr := serverruntime.ReconcileContainerIdentity(ctx, s.store, dockerCli, server); reconcileErr != nil {
			return nil, connect.NewError(connect.CodeInternal, errors.Join(commit.Retain(ctx), fmt.Errorf("failed to reconcile server container identity: %w", reconcileErr)))
		}
		verified, verifyErr := commit.VerifyExpectedReplacement(ctx, server.ContainerID)
		if verifyErr != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to verify the recorded container replacement: %w", verifyErr))
		}
		if !verified {
			return nil, connect.NewError(connect.CodeInternal, errors.Join(commit.Retain(ctx),
				fmt.Errorf("canonical container %q is not the recorded replacement %q; refusing to acknowledge the publication", server.ContainerID, commit.ExpectedReplacementID())))
		}
		// The runtime is verified; this attempt owes cleanup, not another
		// replacement. The operator's explicit start continues below.
		publication.RuntimeAccepted = true
		needsStagedRecreate = false
	}
	stagedContainerStarted := false

	// If server was in deep sleep, release direct knock listener before starting
	if server.Status == storage.StatusDeepSleep && s.proxy != nil {
		s.proxy.StopDirectKnock(server.ID)
	}

	// Resume a paused container directly only when no staged changes require
	// replacement. For staged replacement, keep it frozen until config loading
	// succeeds and RecreateContainer owns the stop/remove transition.
	if server.Status == storage.StatusPaused && server.ContainerID != "" && !needsStagedRecreate {
		if err := s.runPublicationMutation(ctx, &publication, server, "start_unpause", requestID, func() error {
			publication.RuntimeAttempted = true
			return dockerCli.UnpauseContainer(ctx, server.ContainerID)
		}); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resume server: %w", err))
		}
		server.Status = storage.StatusRunning
		if err := s.store.UpdateServerStatus(ctx, server.ID, server.Status); err != nil {
			resumeErr := fmt.Errorf("server resumed but status could not be saved: %w", err)
			rollbackErr := s.rollbackPausedResume(ctx, dockerCli, server)
			return nil, connect.NewError(connect.CodeInternal, errors.Join(resumeErr, rollbackErr))
		}
		if s.proxy != nil && server.ProxyHostname != "" {
			_ = s.proxy.UpdateServerRoute(server)
		}
		// Unpause lands on running inside this RPC, so it announces here.
		// Create and StartServer announce from the reconciler when it records running.
		publication.RuntimeAccepted = true
		if s.bus != nil {
			s.bus.Emit(ctx, events.Event{
				Type:     v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_START,
				ServerID: server.ID,
			})
		}
		return connect.NewResponse(&v1.StartServerResponse{Status: "running"}), nil
	}

	if needsStagedRecreate && server.ContainerID != "" {
		serverConfig, err := s.store.GetServerConfig(ctx, server.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get server configuration: %w", err))
		}
		if serverConfig == nil {
			return nil, connect.NewError(connect.CodeInternal, errors.New("server configuration is empty"))
		}
		s.prepareServerConfig(ctx, server, serverConfig)
		// The dispatch and the replacement share the journaled mutation: a
		// completed request ID suppresses both, so fresh rows are never
		// published under a runtime this request did not touch.
		var result *docker.RecreateContainerResult
		recreateRan := false
		recreateErr := s.runPublicationMutation(ctx, &publication, server, "start_recreate", requestID, func() error {
			publication.RuntimeAttempted = true
			recreateRan = true
			var err error
			result, err = serverruntime.Recreate(ctx, s.store, dockerCli, server, serverConfig)
			return err
		})
		if recreateErr == nil && !recreateRan {
			// The request ID already completed and nothing needed publishing:
			// never recreate or start a runtime for a replayed request.
			return nil, connect.NewError(connect.CodeInternal, errLifecycleRequestCompleted)
		}
		if result != nil && result.NewContainerID != "" {
			if recordErr := publication.RecordReplacement(ctx, result.NewContainerID); recordErr != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to record the replacement identity: %w", recordErr))
			}
		}
		if recreateErr != nil {
			var outcomeErr *serverruntime.RecreationError
			if errors.As(recreateErr, &outcomeErr) {
				if outcomeErr.Operation == nil {
					publication.RuntimeIdentityUnresolved = true
					return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("container recreated but its ID could not be saved: %w", recreateErr))
				}
				publication.RuntimePreflight = outcomeErr.Preflight
			}
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to recreate server with staged configuration: %w", recreateErr))
		}
		if !result.WasRunning {
			if err := serverruntime.Start(ctx, s.store, dockerCli, server); err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to start recreated server: %w", err))
			}
		}
		publication.RuntimeAccepted = true
		stagedContainerStarted = true
	}

	// Still no container - create it
	if server.ContainerID == "" {
		serverConfig, err := s.store.GetServerConfig(ctx, server.ID)
		if err != nil {
			s.log.Error("Failed to get server config: %v", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get server configuration"))
		}

		if serverConfig == nil {
			return nil, connect.NewError(connect.CodeInternal, errors.New("server configuration is empty"))
		}
		s.prepareServerConfig(ctx, server, serverConfig)
		// A completed request ID suppresses this journaled mutation before the
		// dispatch and the create, so a replay cannot build a container for
		// freshly applied rows.
		var containerID string
		createRan := false
		if createErr := s.runPublicationMutation(ctx, &publication, server, "start_create", requestID, func() error {
			publication.RuntimeAttempted = true
			createRan = true
			var err error
			containerID, err = dockerCli.CreateContainer(ctx, server, serverConfig)
			return err
		}); createErr != nil {
			s.log.Error("Failed to create container: %v", createErr)
			return nil, createContainerError(createErr)
		}
		if !createRan {
			// The request ID already completed and nothing needed publishing:
			// never create or start a runtime for a replayed request.
			return nil, connect.NewError(connect.CodeInternal, errLifecycleRequestCompleted)
		}
		if recordErr := publication.RecordReplacement(ctx, containerID); recordErr != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to record the replacement identity: %w", recordErr))
		}

		server.ContainerID = containerID
		if imageID, repoDigest, idErr := dockerCli.ServerImageIdentity(ctx, server); idErr != nil {
			s.log.Error("Failed to read applied image for server %s: %v", server.ID, idErr)
		} else {
			server.AppliedImageID = imageID
			server.AppliedRepoDigest = repoDigest
		}
		if err := s.store.UpdateServerFields(ctx, server.ID, map[string]any{"container_id": containerID, "applied_image_id": server.AppliedImageID, "applied_repo_digest": server.AppliedRepoDigest}); err != nil {
			s.log.Error("Failed to update server with container ID: %v", err)
			_ = dockerCli.RemoveContainer(ctx, containerID)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update server"))
		}
	}

	if !stagedContainerStarted {
		// Start container. The dispatch is written inside the journaled
		// mutation, immediately before this live write, and only for a request
		// that still runs.
		if err := s.runPublicationMutation(ctx, &publication, server, "start", requestID, func() error {
			publication.RuntimeAttempted = true
			return serverruntime.Start(ctx, s.store, dockerCli, server)
		}); err != nil {
			if publication.HasChanges() {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to start staged server: %w", err))
			}
			s.log.Error("Failed to start container, attempting to recreate: %v", err)

			// Get server config for container creation
			serverConfig, err := s.store.GetServerConfig(ctx, server.ID)
			if err != nil {
				s.log.Error("Failed to get server config: %v", err)
				server.Status = storage.StatusError
				_ = s.store.UpdateServerStatus(ctx, server.ID, server.Status)
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get server configuration"))
			}

			// Recreate container
			if serverConfig == nil {
				return nil, connect.NewError(connect.CodeInternal, errors.New("server configuration is empty"))
			}
			s.prepareServerConfig(ctx, server, serverConfig)
			publication.RuntimeAttempted = true
			result, err := serverruntime.Recreate(ctx, s.store, dockerCli, server, serverConfig)
			if result != nil && result.NewContainerID != "" {
				if recordErr := publication.RecordReplacement(ctx, result.NewContainerID); recordErr != nil {
					return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to record the replacement identity: %w", recordErr))
				}
			}
			if err != nil {
				var outcomeErr *serverruntime.RecreationError
				if errors.As(err, &outcomeErr) && outcomeErr.Operation == nil {
					publication.RuntimeIdentityUnresolved = true
				}
				s.log.Error("Failed to recreate container: %v", err)
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to start server: %w", err))
			}

			// RecreateContainer only starts the container if result.WasRunning was true.
			// Since StartServer was requested, start the newly recreated container.
			if !result.WasRunning {
				if startErr := serverruntime.Start(ctx, s.store, dockerCli, server); startErr != nil {
					s.log.Error("Failed to start recreated container: %v", startErr)
					server.Status = storage.StatusError
					_ = s.store.UpdateServerStatus(ctx, server.ID, server.Status)
					return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to start server: %w", startErr))
				}
			}

			publication.RuntimeAccepted = true
			// Update server with new container ID
			if err := s.store.UpdateServerContainerID(ctx, server.ID, server.ContainerID); err != nil {
				s.log.Error("Failed to update server after container recreation: %v", err)
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update server after container recreation"))
			}
		}

	}

	publication.RuntimeAccepted = true
	// Update server status
	now := time.Now()
	server.Status = storage.StatusStarting
	server.LastStarted = &now

	if err := serverruntime.PersistStarted(ctx, s.store, server); err != nil {
		s.log.Error("Failed to update server status: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("server started but status could not be saved: %w", err))
	}

	// Update proxy route if enabled
	if s.proxy != nil && server.ProxyHostname != "" {
		if err := s.proxy.UpdateServerRoute(server); err != nil {
			s.log.Error("Failed to update proxy route: %v", err)
		}
	}

	// Clear ephemeral configuration fields
	if err := s.store.ClearEphemeralConfigFields(ctx, server.ID); err != nil {
		s.log.Error("Failed to clear ephemeral config fields: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("server started but temporary config could not be cleared: %w", err))
	}

	// SERVER_START is emitted by the reconciler when status becomes running,
	// so create, unpause, and this RPC share one announcement.

	// Audit trail (MINE-141): never break the op on audit failure.
	s.auditPower(ctx, req.Peer().Addr, activity.EventServerStart, server.ID, requestID)

	return connect.NewResponse(&v1.StartServerResponse{
		Status: "starting",
	}), nil
}

// rollbackPausedResume keeps the persisted PAUSED intent aligned with Docker
// when a resume succeeds but the database cannot record RUNNING. The rollback
// uses a detached bounded context because the RPC may already be cancelled.
func (s *ServerService) rollbackPausedResume(ctx context.Context, dockerCli *docker.Client, server *storage.Server) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	rollbackErr := dockerCli.PauseContainer(rollbackCtx, server.ContainerID)
	if rollbackErr == nil {
		status, statusErr := dockerCli.GetContainerStatus(rollbackCtx, server.ContainerID)
		if statusErr == nil && status == storage.StatusPaused {
			server.Status = storage.StatusPaused
			return nil
		}
		if statusErr != nil {
			rollbackErr = fmt.Errorf("failed to verify refrozen container: %w", statusErr)
		} else {
			rollbackErr = fmt.Errorf("container reported %s after refreeze", status)
		}
	}

	server.Status = storage.StatusError
	persistErr := s.store.UpdateServer(rollbackCtx, server)
	if persistErr != nil && s.log != nil {
		s.log.Error("Failed to persist error state after paused-server resume rollback failed: %v", persistErr)
	}
	return errors.Join(fmt.Errorf("failed to restore paused container state: %w", rollbackErr), persistErr)
}

// StopServer stops a server
func (s *ServerService) StopServer(ctx context.Context, req *connect.Request[v1.StopServerRequest]) (*connect.Response[v1.StopServerResponse], error) {
	requestID, reqErr := lifecycleRequestID(req.Msg.GetRequestId())
	if reqErr != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, reqErr)
	}
	transition, err := beginServerLifecycleTransition(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	defer transition.Release()

	server, err := s.store.GetServer(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}
	if err := s.refuseActiveOperation(ctx, server.ID); err != nil {
		return nil, err
	}

	if server.ContainerID == "" {
		// If there's no container, server is already stopped
		server.Status = storage.StatusStopped
		if err := s.store.UpdateServer(ctx, server); err != nil {
			s.log.Error("Failed to update server status: %v", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("server stopped but status could not be saved: %w", err))
		}
		return connect.NewResponse(&v1.StopServerResponse{
			Status: "stopped",
		}), nil
	}

	if server.Status == storage.StatusDeepSleep {
		// DEEPSLEEP containers are already exited: skip DockerStop, record the
		// journaled no-op, clear the wake listener, and park as STOPPED.
		if s.proxy != nil {
			if server.ProxyHostname != "" {
				if err := s.proxy.RemoveServerRoute(server.ID); err != nil {
					s.log.Error("Failed to remove proxy route: %v", err)
				}
			} else {
				s.proxy.StopDirectKnock(server.ID)
			}
		}
		if stopErr := s.runJournaledMutation(ctx, server, "stop", requestID, func() error { return nil }); stopErr != nil {
			s.log.Error("Failed to stop container: %v", stopErr)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to stop server"))
		}
		server.Status = storage.StatusStopped
		if err := s.store.UpdateServer(ctx, server); err != nil {
			s.log.Error("Failed to update server status: %v", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("server stopped but status could not be saved: %w", err))
		}
		if s.bus != nil {
			s.bus.Emit(ctx, events.Event{
				Type:     v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP,
				ServerID: server.ID,
			})
		}
		s.auditPower(ctx, req.Peer().Addr, activity.EventServerStop, server.ID, requestID)
		return connect.NewResponse(&v1.StopServerResponse{
			Status: "stopped",
		}), nil
	}

	// Stop container
	dockerCli, err := s.getDockerClientStrict(server.NodeID)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("docker node %q is unavailable: %w", server.NodeID, err))
	}

	// If server is paused, unpause it first so Docker daemon doesn't reject stop (Docker requires unpaused before stop)
	if server.Status == storage.StatusPaused && server.ContainerID != "" {
		if err := dockerCli.UnpauseContainer(ctx, server.ContainerID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resume server before stopping: %w", err))
		}
	}

	window := docker.DefaultStopWindow
	if cfg, cfgErr := s.store.GetServerConfig(ctx, server.ID); cfgErr == nil {
		window = docker.StopWindow(cfg)
	}

	// Persist stopping before the save window so a panel restart resumes the
	// stop instead of starting the JVM again.
	server.Status = storage.StatusStopping
	if err := s.store.UpdateServer(ctx, server); err != nil {
		s.log.Error("Failed to persist stopping status: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to record stop: %w", err))
	}

	// Drop the player route before the save wait so the proxy is not held open.
	if s.proxy != nil {
		if server.ProxyHostname != "" {
			if err := s.proxy.RemoveServerRoute(server.ID); err != nil {
				s.log.Error("Failed to remove proxy route: %v", err)
			}
		} else {
			s.proxy.StopDirectKnock(server.ID)
		}
	}

	stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(ctx), window+15*time.Second)
	defer stopCancel()
	var (
		found   bool
		stopErr error
	)
	stopErr = s.runJournaledMutation(ctx, server, "stop", requestID, func() error {
		var err error
		found, err = dockerCli.StopContainerWithin(stopCtx, server.ContainerID, window)
		return err
	})
	if stopErr != nil {
		s.log.Error("Failed to stop container: %v", stopErr)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to stop server"))
	}

	// If container wasn't found, clean up stale reference
	if !found {
		s.log.Warn("Container %s not found, cleaning up stale reference", server.ContainerID)
		server.ContainerID = ""
	}
	server.Status = storage.StatusStopped

	if err := s.store.UpdateServer(ctx, server); err != nil {
		s.log.Error("Failed to update server status: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("server stopped but status could not be saved: %w", err))
	}

	// Emit the server-stop event
	if s.bus != nil {
		s.bus.Emit(ctx, events.Event{
			Type:     v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP,
			ServerID: server.ID,
		})
	}

	// Audit trail (MINE-141): never break the op on audit failure.
	s.auditPower(ctx, req.Peer().Addr, activity.EventServerStop, server.ID, requestID)

	return connect.NewResponse(&v1.StopServerResponse{
		Status: "stopped",
	}), nil
}

// RestartServer restarts a server
func (s *ServerService) RestartServer(ctx context.Context, req *connect.Request[v1.RestartServerRequest]) (out *connect.Response[v1.RestartServerResponse], retErr error) {
	requestID, reqErr := lifecycleRequestID(req.Msg.GetRequestId())
	if reqErr != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, reqErr)
	}
	transition, err := beginServerLifecycleTransition(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	defer transition.Release()

	server, err := s.store.GetServer(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}
	if err := s.refuseActiveOperation(ctx, server.ID); err != nil {
		return nil, err
	}
	if err := s.refuseCompletedLifecycleRequest(ctx, server.ID, requestID); err != nil {
		return nil, err
	}

	dockerCli, err := s.getDockerClientStrict(server.NodeID)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("docker node %q is unavailable: %w", server.NodeID, err))
	}
	release, err := s.acquireNodeAdmission(ctx, server)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.checkServerStartCapacity(ctx, server); err != nil {
		return nil, err
	}

	// Apply any staged config changes marked for restart. A future time or idle
	// window is not published by this restart.
	commit, fileErr := staging.CommitNextRestartFiles(ctx, s.store, server, s.recreateStagedModule, s.storageResolver)
	if fileErr != nil {
		s.log.Error("Failed to apply staged content on restart for server %s: %v", server.ID, fileErr)
		return nil, stagedPublicationError("failed to apply staged content", fileErr)
	}
	publication := staging.RestartPublication{
		Commit: commit,
	}
	defer func() {
		if finishErr := publication.Resolve(ctx, s.store, retErr); finishErr != nil {
			out = nil
			retErr = connect.NewError(connect.CodeInternal, errors.Join(retErr, finishErr))
		}
	}()
	appliedStaged, err := commit.ApplyConfigs(ctx, s.store)
	publication.Configs = appliedStaged
	if err != nil {
		s.log.Error("Failed to apply staged config changes on restart for server %s: %v", server.ID, err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to apply staged config changes: %w", err))
	}
	stagedApplied := commit.AppliedConfigsThisAttempt()
	// If server is in deep sleep, release direct knock listener before starting
	if server.Status == storage.StatusDeepSleep && s.proxy != nil {
		s.proxy.StopDirectKnock(server.ID)
	}
	pendingRecreate, err := scheduler.HasPendingRestartStagedConfigRecreate(ctx, s.store, server.ID, server.LastStarted)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check staged config recovery: %w", err))
	}
	if commit.NeedsIdentityVerification() {
		// A previous attempt recorded a dispatch but the canonical outcome was
		// not verified. Repair the tracked ID if needed, then accept only the
		// exact captured replacement identity; anything else is drift.
		if _, reconcileErr := serverruntime.ReconcileContainerIdentity(ctx, s.store, dockerCli, server); reconcileErr != nil {
			return nil, connect.NewError(connect.CodeInternal, errors.Join(commit.Retain(ctx), fmt.Errorf("failed to reconcile server container identity: %w", reconcileErr)))
		}
		verified, verifyErr := commit.VerifyExpectedReplacement(ctx, server.ContainerID)
		if verifyErr != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to verify the recorded container replacement: %w", verifyErr))
		}
		if !verified {
			return nil, connect.NewError(connect.CodeInternal, errors.Join(commit.Retain(ctx),
				fmt.Errorf("canonical container %q is not the recorded replacement %q; refusing to acknowledge the publication", server.ContainerID, commit.ExpectedReplacementID())))
		}
		publication.RuntimeAccepted = true
		stagedApplied = 0
		pendingRecreate = false
	}

	// If staged config changes were applied and container exists, recreate it with new config
	if (stagedApplied > 0 || pendingRecreate) && server.ContainerID != "" {
		serverConfig, err := s.store.GetServerConfig(ctx, server.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get server configuration: %w", err))
		}
		if serverConfig == nil {
			return nil, connect.NewError(connect.CodeInternal, errors.New("server configuration is empty"))
		}
		s.prepareServerConfig(ctx, server, serverConfig)
		// The dispatch and the replacement share the journaled mutation: a
		// completed request ID suppresses both, so fresh config is never
		// published under a runtime this request did not touch.
		var result *docker.RecreateContainerResult
		recreateRan := false
		recreateErr := s.runPublicationMutation(ctx, &publication, server, "restart_recreate", requestID, func() error {
			publication.RuntimeAttempted = true
			recreateRan = true
			var err error
			result, err = serverruntime.Recreate(ctx, s.store, dockerCli, server, serverConfig)
			return err
		})
		if recreateErr == nil && !recreateRan {
			// The request ID already completed and nothing needed publishing:
			// never recreate or start a runtime for a replayed request.
			return nil, connect.NewError(connect.CodeInternal, errLifecycleRequestCompleted)
		}
		if result != nil && result.NewContainerID != "" {
			if recordErr := publication.RecordReplacement(ctx, result.NewContainerID); recordErr != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to record the replacement identity: %w", recordErr))
			}
		}
		if recreateErr != nil {
			var outcomeErr *serverruntime.RecreationError
			if errors.As(recreateErr, &outcomeErr) {
				if outcomeErr.Operation == nil {
					publication.RuntimeIdentityUnresolved = true
					return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("container recreated but its ID could not be saved: %w", recreateErr))
				}
				publication.RuntimePreflight = outcomeErr.Preflight
			}
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to recreate server with staged configuration: %w", recreateErr))
		}
		if !result.WasRunning {
			if err := s.runPublicationMutation(ctx, &publication, server, "restart_start", requestID, func() error {
				publication.RuntimeAttempted = true
				return serverruntime.Start(ctx, s.store, dockerCli, server)
			}); err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to start recreated server: %w", err))
			}
		}
		publication.RuntimeAccepted = true
		now := time.Now()
		server.Status = storage.StatusStarting
		server.LastStarted = &now
		if err := serverruntime.PersistStarted(ctx, s.store, server); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("server recreated but status could not be saved: %w", err))
		}
		if err := s.store.ClearEphemeralConfigFields(ctx, server.ID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("server restarted but temporary config could not be cleared: %w", err))
		}
		if s.bus != nil {
			s.bus.Emit(ctx, events.Event{
				Type:     v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_RESTART,
				ServerID: server.ID,
			})
		}
		s.auditPower(ctx, req.Peer().Addr, activity.EventServerRestart, server.ID, requestID)
		return connect.NewResponse(&v1.RestartServerResponse{Status: "starting"}), nil
	}
	// If container doesn't exist, create it and start it
	if server.ContainerID == "" {
		// Get server config for container creation
		serverConfig, err := s.store.GetServerConfig(ctx, server.ID)
		if err != nil {
			s.log.Error("Failed to get server config: %v", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get server configuration"))
		}

		// Create container
		if serverConfig == nil {
			return nil, connect.NewError(connect.CodeInternal, errors.New("server configuration is empty"))
		}
		s.prepareServerConfig(ctx, server, serverConfig)
		var containerID string
		if err := s.runPublicationMutation(ctx, &publication, server, "restart_create", requestID, func() error {
			publication.RuntimeAttempted = true
			var createErr error
			containerID, createErr = dockerCli.CreateContainer(ctx, server, serverConfig)
			return createErr
		}); err != nil {
			s.log.Error("Failed to create container: %v", err)
			return nil, createContainerError(err)
		}
		if recordErr := publication.RecordReplacement(ctx, containerID); recordErr != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to record the replacement identity: %w", recordErr))
		}

		server.ContainerID = containerID
		if imageID, repoDigest, idErr := dockerCli.ServerImageIdentity(ctx, server); idErr != nil {
			s.log.Error("Failed to read applied image for server %s: %v", server.ID, idErr)
		} else {
			server.AppliedImageID = imageID
			server.AppliedRepoDigest = repoDigest
		}
		if err := s.store.UpdateServerFields(ctx, server.ID, map[string]any{"container_id": containerID, "applied_image_id": server.AppliedImageID, "applied_repo_digest": server.AppliedRepoDigest}); err != nil {
			// A recorded replacement identity is recovery evidence. Keep the
			// created container: it may already contain the staged bytes and it
			// is the only object that identity can verify on retry. The deferred
			// resolution seals the batch. Without that evidence the container is
			// untracked and is removed instead of becoming an orphan.
			s.log.Error("Failed to update server with container ID: %v", err)
			if !publication.Commit.NeedsIdentityVerification() {
				_ = dockerCli.RemoveContainer(ctx, containerID)
			}
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update server"))
		}

		// Now start the container. These kinds stay distinct from StartServer
		// so a reused request id cannot replay a completed start as this restart.
		if err := s.runPublicationMutation(ctx, &publication, server, "restart_start", requestID, func() error {
			publication.RuntimeAttempted = true
			return serverruntime.Start(ctx, s.store, dockerCli, server)
		}); err != nil {
			s.log.Error("Failed to start container: %v", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to start server: %w", err))
		}

		publication.RuntimeAccepted = true
		// Update server status
		now := time.Now()
		server.Status = storage.StatusStarting
		server.LastStarted = &now

		if err := serverruntime.PersistStarted(ctx, s.store, server); err != nil {
			s.log.Error("Failed to update server status: %v", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("server restarted but status could not be saved: %w", err))
		}

		// Clear ephemeral configuration fields
		if err := s.store.ClearEphemeralConfigFields(ctx, server.ID); err != nil {
			s.log.Error("Failed to clear ephemeral config fields: %v", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("server restarted but temporary config could not be cleared: %w", err))
		}

		// Emit the server-restart event
		if s.bus != nil {
			s.bus.Emit(ctx, events.Event{
				Type:     v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_RESTART,
				ServerID: server.ID,
			})
		}

		// Update proxy route if enabled
		if s.proxy != nil && server.ProxyHostname != "" {
			if err := s.proxy.UpdateServerRoute(server); err != nil {
				s.log.Error("Failed to update proxy route on restart: %v", err)
			}
		}

		return connect.NewResponse(&v1.RestartServerResponse{
			Status: "starting",
		}), nil
	}

	// If server is paused, unpause it first so Docker can restart it. The
	// unpause is a journaled mutation: a completed request ID must not resume
	// the container or record a dispatch for freshly staged rows.
	if server.Status == storage.StatusPaused && server.ContainerID != "" {
		if err := s.runPublicationMutation(ctx, &publication, server, "restart_unpause", requestID, func() error {
			publication.RuntimeAttempted = true
			publication.RuntimeInPlaceMayHaveMutated = true
			return dockerCli.UnpauseContainer(ctx, server.ContainerID)
		}); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to resume server before restarting: %w", err))
		}
	}

	// Restart container. The request stays inside the publication mutation so
	// a consumed request ID cannot silently skip freshly published changes.
	if err := s.runPublicationMutation(ctx, &publication, server, "restart", requestID, func() error {
		publication.RuntimeAttempted = true
		return dockerCli.RestartContainer(ctx, server.ContainerID, 2*time.Second)
	}); err != nil {
		// Record what the in-place attempt actually proved. A typed preflight
		// means Docker never stopped or started the container; every other
		// failure may already have exposed the staged bytes and survives any
		// later fallback preflight.
		if publication.RuntimeAttempted {
			if errors.Is(err, docker.ErrRecreatePreflight) {
				publication.RuntimePreflight = true
			} else {
				publication.RuntimeInPlaceMayHaveMutated = true
			}
		}
		if publication.HasChanges() && !publication.RuntimePreflight {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to restart staged server: %w", err))
		}
		s.log.Warn("Failed to restart container directly (%v), attempting recreation", err)
		serverConfig, cfgErr := s.store.GetServerConfig(ctx, server.ID)
		if cfgErr == nil {
			if serverConfig == nil {
				return nil, connect.NewError(connect.CodeInternal, errors.New("server configuration is empty"))
			}
			s.prepareServerConfig(ctx, server, serverConfig)
			// The fallback is a separate runtime attempt. It starts without the
			// in-place preflight's proof; only its own typed preflight may
			// restore it.
			publication.RuntimePreflight = false
			publication.RuntimeAttempted = true
			recResult, recErr := serverruntime.Recreate(ctx, s.store, dockerCli, server, serverConfig)
			if recResult != nil && recResult.NewContainerID != "" {
				if recordErr := publication.RecordReplacement(ctx, recResult.NewContainerID); recordErr != nil {
					return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to record the replacement identity: %w", recordErr))
				}
			}
			if recErr == nil && !recResult.WasRunning {
				recErr = serverruntime.Start(ctx, s.store, dockerCli, server)
			}
			if recErr == nil {
				publication.RuntimeAccepted = true
				server.ContainerID = recResult.NewContainerID
				now := time.Now()
				server.Status = storage.StatusStarting
				server.LastStarted = &now
				if saveErr := serverruntime.PersistStarted(ctx, s.store, server); saveErr != nil {
					return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("server restarted but status could not be saved: %w", saveErr))
				}
				if clearErr := s.store.ClearEphemeralConfigFields(ctx, server.ID); clearErr != nil {
					return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("server restarted but temporary config could not be cleared: %w", clearErr))
				}
				if s.bus != nil {
					s.bus.Emit(ctx, events.Event{
						Type:     v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_RESTART,
						ServerID: server.ID,
					})
				}
				if s.proxy != nil && server.ProxyHostname != "" {
					if err := s.proxy.UpdateServerRoute(server); err != nil {
						s.log.Error("Failed to update proxy route on restart: %v", err)
					}
				}
				return connect.NewResponse(&v1.RestartServerResponse{
					Status: "starting",
				}), nil
			}
			var outcomeErr *serverruntime.RecreationError
			if errors.As(recErr, &outcomeErr) {
				if outcomeErr.Operation == nil {
					publication.RuntimeIdentityUnresolved = true
				} else {
					publication.RuntimePreflight = outcomeErr.Preflight
				}
			}
			err = errors.Join(err, recErr)
		} else {
			err = errors.Join(err, cfgErr)
		}
		s.log.Error("Failed to restart container: %v", err)
		server.Status = storage.StatusError
		_ = s.store.UpdateServerStatus(ctx, server.ID, server.Status)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to restart server: %w", err))
	}

	publication.RuntimeAccepted = true
	// Update server status
	now := time.Now()
	server.Status = storage.StatusStarting
	server.LastStarted = &now
	if err := serverruntime.PersistStarted(ctx, s.store, server); err != nil {
		s.log.Error("Failed to update server status: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("server restarted but status could not be saved: %w", err))
	}

	// Clear ephemeral configuration fields
	if err := s.store.ClearEphemeralConfigFields(ctx, server.ID); err != nil {
		s.log.Error("Failed to clear ephemeral config fields: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("server restarted but temporary config could not be cleared: %w", err))
	}

	// Emit the server-restart event
	if s.bus != nil {
		s.bus.Emit(ctx, events.Event{
			Type:     v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_RESTART,
			ServerID: server.ID,
		})
	}

	// Update proxy route if enabled
	if s.proxy != nil && server.ProxyHostname != "" {
		if err := s.proxy.UpdateServerRoute(server); err != nil {
			s.log.Error("Failed to update proxy route on restart: %v", err)
		}
	}

	s.auditPower(ctx, req.Peer().Addr, activity.EventServerRestart, server.ID, requestID)

	return connect.NewResponse(&v1.RestartServerResponse{
		Status: "restarting",
	}), nil
}

// Destroys and recreates a server container from scratch - brute force reset
func (s *ServerService) RecreateServer(ctx context.Context, req *connect.Request[v1.RecreateServerRequest]) (*connect.Response[v1.RecreateServerResponse], error) {
	transition, err := beginServerLifecycleTransition(req.Msg.Id)
	if err != nil {
		return nil, err
	}
	defer transition.Release()

	server, err := s.store.GetServer(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}
	if err := s.refuseActiveOperation(ctx, server.ID); err != nil {
		return nil, err
	}

	// Get server config for container creation
	serverConfig, err := s.store.GetServerConfig(ctx, server.ID)
	if err != nil {
		s.log.Error("Failed to get server config: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get server configuration"))
	}

	// Recreate container
	dockerCli, err := s.getDockerClientStrict(server.NodeID)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("docker node %q is unavailable: %w", server.NodeID, err))
	}
	release, err := s.acquireNodeAdmission(ctx, server)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := s.checkServerStartCapacity(ctx, server); err != nil {
		return nil, err
	}
	s.prepareServerConfig(ctx, server, serverConfig)
	result, err := serverruntime.Recreate(ctx, s.store, dockerCli, server, serverConfig)
	if err != nil {
		s.log.Error("Failed to recreate container: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to recreate server container: %w", err))
	}

	// If the container was not already running (and thus not started by RecreateContainer),
	// start it now since RecreateServer intends to start the freshly recreated container.
	if !result.WasRunning {
		if startErr := serverruntime.Start(ctx, s.store, dockerCli, server); startErr != nil {
			s.log.Error("Failed to start recreated container: %v", startErr)
			server.Status = storage.StatusError
			_ = s.store.UpdateServer(ctx, server)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to start recreated container: %w", startErr))
		}
	}

	// Update server status
	now := time.Now()
	server.Status = storage.StatusStarting
	server.LastStarted = &now

	if err := s.store.UpdateServer(ctx, server); err != nil {
		s.log.Error("Failed to update server: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update server"))
	}

	// Update proxy route if enabled
	if s.proxy != nil && server.ProxyHostname != "" {
		if err := s.proxy.UpdateServerRoute(server); err != nil {
			s.log.Error("Failed to update proxy route: %v", err)
		}
	}

	// Clear ephemeral configuration fields
	if err := s.store.ClearEphemeralConfigFields(ctx, server.ID); err != nil {
		s.log.Error("Failed to clear ephemeral config fields: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("server recreated but temporary config could not be cleared: %w", err))
	}

	s.log.Info("Server %s recreated successfully with new container %s", server.Name, result.NewContainerID)

	return connect.NewResponse(&v1.RecreateServerResponse{
		Status: "recreated",
	}), nil
}

// SendCommand sends a command to a server
func (s *ServerService) SendCommand(ctx context.Context, req *connect.Request[v1.SendCommandRequest]) (*connect.Response[v1.SendCommandResponse], error) {
	server, err := s.store.GetServer(ctx, req.Msg.Id)

	silent := false
	if req.Msg.Silent != nil {
		silent = *req.Msg.Silent
	}

	if err != nil {
		s.log.Error("Failed to get server: %v", err)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}

	// Check if server is running
	if server.ContainerID == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("server container not found"))
	}

	dockerCli, err := s.getDockerClientStrict(server.NodeID)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("docker node %q is unavailable: %w", server.NodeID, err))
	}
	status, err := dockerCli.GetContainerStatus(ctx, server.ContainerID)
	if err != nil || status != storage.StatusRunning {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("server is not running"))
	}

	if req.Msg.Command == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("command is required"))
	}

	// Add command to log stream if available
	commandTime := time.Now()
	if !silent && s.logStreamer != nil {
		s.logStreamer.AddCommandEntry(server.ContainerID, req.Msg.Command, commandTime)
	}

	// Send command
	output, err := s.sender.SendCommand(ctx, server.ID, req.Msg.Command)
	success := err == nil

	// Add command output to log stream if available
	if !silent && s.logStreamer != nil && (output != "" || !success) {
		s.logStreamer.AddCommandOutput(server.ContainerID, output, success, commandTime)
	}

	if err != nil {
		s.log.Error("Failed to execute command: %v", err)
		return connect.NewResponse(&v1.SendCommandResponse{
			Success: false,
			Error:   err.Error(),
		}), nil
	}

	return connect.NewResponse(&v1.SendCommandResponse{
		Success: true,
		Output:  output,
	}), nil
}

// ListServerPlayers returns the live online roster from the metrics
// collector (RCON `list` / SLP sample), empty when offline (MINE-140).
func (s *ServerService) ListServerPlayers(ctx context.Context, req *connect.Request[v1.ListServerPlayersRequest]) (*connect.Response[v1.ListServerPlayersResponse], error) {
	server, err := s.store.GetServer(ctx, req.Msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}
	players := []string{}
	online := 0
	if s.metricsCollector != nil {
		if m := s.metricsCollector.GetMetrics(req.Msg.ServerId); m != nil {
			players, online = livePlayerRoster(server, m, time.Now())
		}
	}
	return connect.NewResponse(&v1.ListServerPlayersResponse{
		Players: players, OnlineCount: int32(online),
	}), nil
}

// writeManualServerPack copies a panel-extracted manual server pack onto the
// owning node's storage through ForNode. It refuses a missing remote adapter
// and never writes a controller-local fallback zip.
func (s *ServerService) writeManualServerPack(ctx context.Context, server *storage.Server, modpackID string) (bool, error) {
	modpack, err := s.store.GetIndexedModpack(ctx, modpackID)
	if err != nil || modpack == nil || modpack.Indexer != "manual" {
		return false, nil
	}
	modpackFile, err := s.store.GetIndexedModpackFiles(ctx, modpackID)
	if err != nil {
		return false, err
	}
	if len(modpackFile) == 0 {
		return false, nil
	}
	source, err := os.Open(modpackFile[0].DownloadURL)
	if err != nil {
		return false, err
	}
	defer func() { _ = source.Close() }()
	info, err := storageNodeInfo(ctx, s.store, server.NodeID)
	if err != nil {
		return false, err
	}
	acc, err := s.storageResolver.ForNode(ctx, info, server.DataPath)
	if err != nil {
		return false, err
	}
	defer func() { _ = acc.Close() }()
	if _, err := nodestorage.WriteFrom(ctx, acc, "modpack.zip", source, 64<<30); err != nil {
		return false, err
	}
	return true, nil
}

// serverLatestLog reads logs/latest.log from the owning node's mapped root.
// A remote node without reachable storage is refused, so a controller-local
// copy is not uploaded in its place.
func serverLatestLog(ctx context.Context, store *storage.Store, server *storage.Server, resolver *nodestorage.Resolver) ([]byte, error) {
	info, err := storageNodeInfo(ctx, store, server.NodeID)
	if err != nil {
		return nil, err
	}
	acc, err := resolver.ForNode(ctx, info, server.DataPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = acc.Close() }()
	rel := filepath.Join("logs", "latest.log")
	source, err := acc.OpenRead(ctx, rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = source.Close() }()
	fi, err := acc.Stat(rel)
	if err != nil {
		return nil, err
	}
	// Diagnostics retain the recent tail instead of failing on a large log.
	return io.ReadAll(io.NewSectionReader(source, max(0, fi.Size()-(8<<20)), 8<<20))
}

// Reads the server's latest.log and uploads it to mclo.gs
func (s *ServerService) UploadToMCLogs(ctx context.Context, req *connect.Request[v1.UploadToMCLogsRequest]) (*connect.Response[v1.UploadToMCLogsResponse], error) {
	server, err := s.store.GetServer(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}

	content, err := serverLatestLog(ctx, s.store, server, s.storageResolver)
	if nodestorage.IsUnavailable(err) {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if err != nil {
		s.log.Error("Failed to read server log file: %v", err)
		if os.IsNotExist(err) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("log file not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read server log"))
	}

	if len(content) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("log file is empty"))
	}

	// Truncate to 25000 lines if needed
	lines := bytes.Split(content, []byte("\n"))
	if len(lines) > 25000 {
		lines = lines[len(lines)-25000:]
		content = bytes.Join(lines, []byte("\n"))
	}

	// Build mclo.gs request
	payload, _ := json.Marshal(map[string]string{
		"content": string(content),
		"source":  fmt.Sprintf("carbon-panel-%s", server.Name),
	})

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.mclo.gs/1/log", bytes.NewReader(payload))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create request"))
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		s.log.Error("Failed to upload to mclo.gs: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to upload to mclo.gs"))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read mclo.gs response"))
	}

	var result struct {
		Success bool   `json:"success"`
		URL     string `json:"url"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to parse mclo.gs response"))
	}

	if !result.Success {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("mclo.gs error: %s", result.Error))
	}

	return connect.NewResponse(&v1.UploadToMCLogsResponse{
		Url: result.URL,
	}), nil
}

// GetServerLogs gets server logs
func (s *ServerService) GetServerLogs(ctx context.Context, req *connect.Request[v1.GetServerLogsRequest]) (*connect.Response[v1.GetServerLogsResponse], error) {
	// Parse tail parameter
	tail := int(req.Msg.Tail)
	if tail == 0 {
		tail = 100
	}

	server, err := s.store.GetServer(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}

	// If container not created yet, return empty logs
	if server.ContainerID == "" {
		return connect.NewResponse(&v1.GetServerLogsResponse{
			Logs:  []*v1.LogEntry{},
			Total: 0,
		}), nil
	}

	// Get structured log entries from the log streamer if available
	var protoLogs []*v1.LogEntry
	if s.logStreamer != nil {
		protoLogs = s.logStreamer.GetLogs(server.ContainerID, tail)
	}

	return connect.NewResponse(&v1.GetServerLogsResponse{
		Logs:  protoLogs,
		Total: int32(len(protoLogs)),
	}), nil
}

// ClearServerLogs clears server logs
func (s *ServerService) ClearServerLogs(ctx context.Context, req *connect.Request[v1.ClearServerLogsRequest]) (*connect.Response[v1.ClearServerLogsResponse], error) {
	server, err := s.store.GetServer(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}

	if server.ContainerID == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("server container not created"))
	}

	// Clear structured log entries if log streamer is available
	if s.logStreamer != nil {
		s.logStreamer.ClearLogs(server.ContainerID)
	}

	return connect.NewResponse(&v1.ClearServerLogsResponse{}), nil
}

// isHostPortBound checks if a port is already bound by an OS process in the local network namespace.
//
// On some platforms (notably Windows) a socket bound to a specific address (for
// example 127.0.0.1) does not conflict with a bind to the wildcard address, so
// a single bind attempt is not reliable. Probe a set of common local addresses
// and treat the port as bound if any of them cannot be acquired.
func isHostPortBound(port int) bool {
	return isHostPortBoundWith(port, net.Listen)
}

func isHostPortBoundOn(port int, network string) bool {
	if network != "udp" {
		return isHostPortBound(port)
	}
	for _, addr := range hostPortCandidates() {
		conn, err := net.ListenPacket("udp", net.JoinHostPort(addr, strconv.Itoa(port)))
		if err == nil {
			_ = conn.Close()
			continue
		}
		if portBindInUse(err) {
			return true
		}
	}
	return false
}

func hostPortCandidates() []string {
	candidates := []string{"127.0.0.1", "0.0.0.0", "::1", "::"}
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			addrs, _ := iface.Addrs()
			for _, a := range addrs {
				var ip net.IP
				switch v := a.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if ip != nil {
					candidates = append(candidates, ip.String())
				}
			}
		}
	}
	return candidates
}

func portBindInUse(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "in use") || strings.Contains(msg, "only one usage")
}

func isHostPortBoundWith(port int, listen func(string, string) (net.Listener, error)) bool {
	seen := make(map[string]bool)
	for _, addr := range hostPortCandidates() {
		if seen[addr] {
			continue
		}
		seen[addr] = true

		ln, err := listen("tcp", net.JoinHostPort(addr, strconv.Itoa(port)))
		if err == nil {
			_ = ln.Close()
			continue
		}
		// Only treat address-in-use failures as "bound"; ignore unrelated
		// errors (e.g. disabled IPv6, permission) so they don't produce
		// false positives.
		if portBindInUse(err) {
			return true
		}
	}
	return false
}

// GetNextAvailablePort gets the next available port
func (s *ServerService) GetNextAvailablePort(ctx context.Context, req *connect.Request[v1.GetNextAvailablePortRequest]) (*connect.Response[v1.GetNextAvailablePortResponse], error) {
	// Get all servers
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		s.log.Error("Failed to list servers: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get available port"))
	}

	// Build a map of used ports (only for non-proxied servers)
	usedPortsMap := make(map[int32]bool)
	for _, server := range servers {
		// Only count ports for servers that don't use proxy
		if server.ProxyHostname == "" && server.Port > 0 {
			usedPortsMap[int32(server.Port)] = true
		}
	}

	// Mark proxy listening ports as used (only if proxy is enabled)
	if s.config.Proxy.Enabled {
		for _, port := range s.config.Proxy.ListenPorts {
			usedPortsMap[int32(port)] = true
		}
	}

	// Mark ports allocated on the Docker daemon as used
	dockerCli, err := s.getDockerClientStrict("")
	if err != nil || dockerCli == nil {
		if err == nil {
			err = fmt.Errorf("no default Docker client available")
		}
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("cannot check default Docker host ports for availability: %w", err))
	}
	allocated, err := dockerCli.AllocatedHostPorts(ctx, "tcp")
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("cannot check default Docker host ports for availability: %w", err))
	}
	for port := range allocated {
		usedPortsMap[int32(port)] = true
	}

	// Find the next available port starting from 25565
	var nextPort int32 = 25565
	for usedPortsMap[nextPort] || s.isHostPortBound(int(nextPort)) {
		usedPortsMap[nextPort] = true
		nextPort++
		// Safety check to avoid infinite loop
		if nextPort > 65535 {
			return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("no available ports"))
		}
	}

	// Convert map to proto UsedPort array
	usedPorts := make([]*v1.UsedPort, 0, len(usedPortsMap))
	for port, inUse := range usedPortsMap {
		usedPorts = append(usedPorts, &v1.UsedPort{
			Port:  port,
			InUse: inUse,
		})
	}

	return connect.NewResponse(&v1.GetNextAvailablePortResponse{
		Port:      nextPort,
		UsedPorts: usedPorts,
	}), nil
}

// prepareServerConfig fills in global settings and defaults for server config before container launch
func (s *ServerService) prepareServerConfig(ctx context.Context, server *storage.Server, serverConfig *storage.ServerConfig) {
	serverruntime.PrepareConfig(ctx, s.store, server, serverConfig)
}

// MigrateServer moves a server instance from its current node to a target node
func (s *ServerService) MigrateServer(ctx context.Context, req *connect.Request[v1.MigrateServerRequest]) (*connect.Response[v1.MigrateServerResponse], error) {
	requestID, reqErr := lifecycleRequestID(req.Msg.GetRequestId())
	if reqErr != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, reqErr)
	}
	startTime := time.Now()
	serverID := req.Msg.Id
	targetNodeID := req.Msg.TargetNodeId
	force := req.Msg.GetForce()

	if serverID == "" || targetNodeID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("server ID and target node ID are required"))
	}

	server, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server %s not found: %w", serverID, err))
	}

	sourceNodeID := server.NodeID

	if sourceNodeID == targetNodeID {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("server is already hosted on node '%s'", targetNodeID))
	}

	// Retrieve target node & validate
	targetNode, err := s.store.GetNode(ctx, targetNodeID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("target node '%s' not found: %w", targetNodeID, err))
	}

	if !targetNode.Enabled {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("target node '%s' is disabled", targetNode.Name))
	}

	// Verify target node connectivity
	if s.pool != nil {
		online, pingErr := s.pool.PingNode(ctx, targetNodeID)
		if (!online || pingErr != nil) && !force {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("target node daemon is unreachable: %v", pingErr))
		}
	}

	// Check capacity limits if not forced
	if !force && (targetNode.MaxMemoryMB > 0 || targetNode.MaxServers > 0) {
		allocMem, serverCount, _, err := s.store.GetNodeStats(ctx, targetNode.ID)
		if err == nil {
			if targetNode.MaxServers > 0 && serverCount >= targetNode.MaxServers {
				return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("target node has reached its maximum server capacity (%d/%d)", serverCount, targetNode.MaxServers))
			}
			if targetNode.MaxMemoryMB > 0 && (allocMem+int64(server.Memory)) > targetNode.MaxMemoryMB {
				return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("target node lacks memory headroom (allocated %dMB + required %dMB > limit %dMB)", allocMem, server.Memory, targetNode.MaxMemoryMB))
			}
		}
	}

	var sourceDockerCli, targetDockerCli *docker.Client
	if s.pool != nil || s.docker != nil {
		var err error
		sourceDockerCli, err = s.getDockerClientStrict(sourceNodeID)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("source docker node %q is unavailable: %w", sourceNodeID, err))
		}
		targetDockerCli, err = s.getDockerClientStrict(targetNodeID)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("target docker node %q is unavailable: %w", targetNodeID, err))
		}
	}

	// Check if server is currently running
	var isRunning bool
	if server.ContainerID != "" && sourceDockerCli != nil {
		status, err := sourceDockerCli.GetContainerStatus(ctx, server.ContainerID)
		if err == nil && (status == storage.StatusRunning || status == storage.StatusStarting) {
			isRunning = true
		}
	}

	// One durable operation per server (OSS-229): the journal's active lock
	// refuses concurrent lifecycle mutations of the same world, and the
	// transition lease serializes in-process callers (OSS-230).
	transition, terr := docker.TryBeginServerTransition(server.ID)
	if terr != nil {
		return nil, connect.NewError(connect.CodeAborted, terr)
	}
	defer transition.Release()

	// Resolve the world transfer paths before touching anything, so a world
	// that cannot be transferred aborts with the source fully intact.
	srcRoot, dstRoot, rootErr := s.migrationWorldRoots(ctx, server, targetNode)
	if rootErr != nil {
		return nil, migrationResponseError(rootErr)
	}

	oldContainerID := server.ContainerID
	var migrateResp *v1.MigrateServerResponse
	migrateErr := s.runJournaledMutation(ctx, server, "migrate", requestID, func() error {
		var mErr error
		migrateResp, mErr = s.migrateServerNow(ctx, server, targetNode, sourceDockerCli, targetDockerCli, srcRoot, dstRoot, isRunning, oldContainerID, startTime)
		return mErr
	})
	if migrateErr != nil {
		return nil, migrationResponseError(migrateErr)
	}

	if s.bus != nil {
		s.bus.Emit(ctx, events.Event{
			Type:     v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_RESTART,
			ServerID: server.ID,
		})
	}

	return connect.NewResponse(migrateResp), nil
}
