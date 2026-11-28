// SPDX-License-Identifier: MIT AND MPL-2.0
// This file includes DiscoPanel material under the MIT License,
// Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
// and still applies to that inherited material.
// Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
// If a copy of the MPL was not distributed with this file, You can obtain
// one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpcreflect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/bluemapdiscovery"
	"github.com/athNdev/carbon-panel/internal/command"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/events"
	"github.com/athNdev/carbon-panel/internal/health"
	"github.com/athNdev/carbon-panel/internal/metrics"
	"github.com/athNdev/carbon-panel/internal/module"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/packwiz"
	"github.com/athNdev/carbon-panel/internal/proxy"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc/handlers"
	"github.com/athNdev/carbon-panel/internal/rpc/services"
	"github.com/athNdev/carbon-panel/internal/scheduler"
	"github.com/athNdev/carbon-panel/internal/serverruntime"
	"github.com/athNdev/carbon-panel/internal/worldview"
	"github.com/athNdev/carbon-panel/internal/ws"
	"github.com/athNdev/carbon-panel/pkg/download"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	"github.com/athNdev/carbon-panel/pkg/upload"
	web "github.com/athNdev/carbon-panel/web/carbon-panel"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Server represents the Connect RPC server
type Server struct {
	store            *storage.Store
	docker           *docker.Client
	clientPool       *docker.ClientPool
	storageResolver  *nodestorage.Resolver
	admission        *serverruntime.AdmissionCoordinator
	placementEngine  *docker.PlacementEngine
	sender           *command.Sender
	config           *config.Config
	log              *logger.Logger
	handler          http.Handler
	proxyManager     *proxy.Manager
	authManager      *auth.Manager
	enforcer         *rbac.Enforcer
	oidcHandler      *auth.OIDCHandler
	logStreamer      *logger.LogStreamer
	scheduler        *scheduler.Scheduler
	metricsCollector *metrics.Collector
	moduleManager    *module.Manager
	bus              *events.Bus
	uploadManager    *upload.Manager
	downloadManager  *download.Manager
	wsHub            *ws.Hub
	metricsHandler   *handlers.MetricsHandler
	// modBrowseManager is the single shared operation used by both the
	// generated ModBrowseService and the legacy /mods/* JSON adapter.
	modBrowseManager *handlers.ModOnlineManager
	// stagedConfigOps and packwizOps are the single shared operations used by
	// both the generated services and the legacy JSON adapters.
	stagedConfigOps *handlers.StagedConfigOperations
	packwizOps      *handlers.PackwizOperations

	// startedAt is the server construction time, surfaced by /healthz and
	// /readyz as an uptime.
	startedAt time.Time

	// lastObservation reports when the reconciler last finished a full sweep.
	// It is set after the reconciler starts, so it may be nil.
	lastObservation func() time.Time

	// operationService backs SetReconcilerNudge. It is created in
	// registerServices, so it may be nil on a server that never registered.
	operationService *services.OperationService

	// playerService owns the skin cache janitor and any management-protocol
	// WebSocket reader; it is stopped with the controller.
	playerService *services.PlayerService

	// fileService owns OSS-387 controller stream sessions, their bounded
	// contexts/storage handles and the TTL reaper. Normal controller shutdown
	// must call Stop so none of them outlive the server.
	fileService *services.FileService

	// worldViewProxy is the authenticated same-origin renderer proxy. It is
	// created in registerServices and mounted before the frontend catch-all
	// in setupHandler.
	worldViewProxy http.Handler

	// serverService backs SetAdmissionCoordinator. It is created in
	// registerServices, so it may be nil on a server that never registered.
	serverService *services.ServerService
	// configService and proxyService also start containers (recreation) and
	// receive the shared gate in SetAdmissionCoordinator (OSS-405).
	configService *services.ConfigService
	proxyService  *services.ProxyService
}

// Creates new Connect RPC server
func NewServer(
	store *storage.Store,
	dockerCli *docker.Client,
	sender *command.Sender,
	cfg *config.Config,
	proxyManager *proxy.Manager,
	sched *scheduler.Scheduler,
	metricsCollector *metrics.Collector,
	moduleManager *module.Manager,
	bus *events.Bus,
	log *logger.Logger,
	clientPool *docker.ClientPool,
	placementEngine *docker.PlacementEngine,
	storageResolvers ...*nodestorage.Resolver,
) *Server {
	// Initialize RBAC enforcer
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		log.Error("Failed to initialize RBAC enforcer: %v", err)
	}
	if enforcer != nil {
		if err := enforcer.SeedDefaultPolicies(cfg.Auth.AnonymousAccess); err != nil {
			log.Error("Failed to seed default policies: %v", err)
		}
	}

	// Initialize auth manager. The recovery key file lives next to the
	// panel database and is adopted on startup instead of rotated (MINE-209).
	authCfg := cfg.Auth
	if authCfg.RecoveryKeyFile == "" && cfg.Storage.DataDir != "" {
		authCfg.RecoveryKeyFile = filepath.Join(cfg.Storage.DataDir, "recovery.key")
	}
	authManager, err := auth.NewManager(store, enforcer, &authCfg)
	if err != nil {
		log.Error("Failed to initialize auth manager: %v", err)
	}

	// Initialize OIDC handler
	oidcHandler, err := auth.NewOIDCHandler(authManager, store, &cfg.Auth.OIDC, log)
	if err != nil {
		log.Warn("Failed to initialize OIDC handler: %v", err)
		oidcHandler, _ = auth.NewOIDCHandler(authManager, store, &config.OIDCConfig{}, log)
	}

	// Initialize log streamer
	var logStreamer *logger.LogStreamer
	if dockerCli != nil {
		logStreamer = logger.NewLogStreamer(dockerCli.GetDockerClient(), log, 10000)
		dockerCli.SetLogStreamer(logStreamer)
	}

	// Initialize upload manager
	uploadTTL := time.Duration(cfg.Upload.SessionTTL) * time.Minute
	// Initialize upload manager. The effective limit is upload.max_upload_size
	// when set, otherwise storage.max_upload_size (which would otherwise be
	// dead configuration and leave streaming uploads unbounded).
	maxUploadSize := cfg.Upload.MaxUploadSize
	if maxUploadSize <= 0 {
		maxUploadSize = cfg.Storage.MaxUploadSize
	}
	uploadManager := upload.NewManager(cfg.Storage.TempDir, uploadTTL, maxUploadSize, log)

	// Initialize download manager
	downloadManager := download.NewManager(cfg.Storage.TempDir, uploadTTL, log)

	// Initialize WebSocket hub
	wsHub := ws.NewHub(logStreamer, authManager, enforcer, store, dockerCli, sender, log)
	wsHub.SetOriginPolicy(cfg.Server.AllowedOrigins, cfg.Server.TrustProxy, cfg.Server.RequireWSOrigin)
	go wsHub.Run()

	if clientPool == nil && dockerCli != nil {
		clientPool = docker.NewClientPool(store, dockerCli, log)
	}
	if clientPool != nil {
		wsHub.SetPool(clientPool)
	}
	if placementEngine == nil && store != nil {
		placementEngine = docker.NewPlacementEngine(store)
	}

	// Let the log streamer attribute a container to its node so it can tail
	// containers on remote nodes rather than silently inspecting the local
	// daemon (MINE-108).
	if logStreamer != nil && store != nil && clientPool != nil {
		logStreamer.SetClientResolver(docker.NewContainerLogClientResolver(store, clientPool))
	}

	var storageResolver *nodestorage.Resolver
	if len(storageResolvers) > 0 {
		storageResolver = storageResolvers[0]
	}
	s := &Server{
		store:            store,
		docker:           dockerCli,
		clientPool:       clientPool,
		storageResolver:  storageResolver,
		placementEngine:  placementEngine,
		sender:           sender,
		config:           cfg,
		log:              log,
		proxyManager:     proxyManager,
		authManager:      authManager,
		enforcer:         enforcer,
		oidcHandler:      oidcHandler,
		logStreamer:      logStreamer,
		scheduler:        sched,
		metricsCollector: metricsCollector,
		moduleManager:    moduleManager,
		bus:              bus,
		uploadManager:    uploadManager,
		downloadManager:  downloadManager,
		wsHub:            wsHub,
		startedAt:        time.Now(),
	}
	s.modBrowseManager = handlers.NewModOnlineManager(s.store, s.log, s.authManager, s.enforcer, handlers.ModOnlineOptions{StorageResolver: s.storageResolver})
	s.stagedConfigOps = handlers.NewStagedConfigOperations(s.store)
	packwizDir := filepath.Join(cfg.Storage.DataDir, "packwiz")
	s.packwizOps = handlers.NewPackwizOperations(packwiz.NewManager(packwizDir, log))

	s.setupHandler()
	return s
}

// Setup all Connect RPC handlers
func (s *Server) setupHandler() {
	mux := http.NewServeMux()

	// Configure Connect options
	interceptors := []connect.Interceptor{
		s.loggingInterceptor(),
		s.authInterceptor(),
	}

	opts := []connect.HandlerOption{
		connect.WithInterceptors(interceptors...),
		// Enable gRPC, gRPC-Web, and Connect protocols
		connect.WithHandlerOptions(
			connect.WithCompression("gzip", nil, nil),
		),
	}

	// Register all service handlers
	s.registerServices(mux, opts)

	// Add reflection for gRPC clients. The reflector enumerates the authoritative
	// set of mounted Connect services so it cannot drift from registerServices;
	// RBAC permission coverage is checked independently by the descriptor-driven
	// conformance gate in internal/rbac/mapping_test.go (OSS-403).
	reflector := grpcreflect.NewStaticReflector(RegisteredConnectServices...)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))

	// Register WebSocket handler
	mux.Handle("/ws", s.wsHub)

	// Liveness and readiness endpoints (must be registered before the frontend
	// catch-all, which otherwise answers 200 HTML for any unknown path).
	mux.HandleFunc("/healthz", handlers.NewHealthHandler(s.startedAt, nil))
	mux.HandleFunc("/readyz", handlers.NewHealthHandler(s.startedAt, s.dbReady))
	mux.HandleFunc("/health/detail", handlers.NewLayeredHealthHandler(s.healthSnapshot))

	// Register OIDC HTTP handlers. These are deliberately plain HTTP routes,
	// not Connect RPCs: the callback is IdP-driven and the exchange hands the
	// SPA its session token without ever putting it in a URL.
	if s.oidcHandler != nil && s.oidcHandler.IsEnabled() {
		mux.HandleFunc("/api/v1/auth/oidc/login", s.oidcHandler.HandleLogin)
		mux.HandleFunc("/api/v1/auth/oidc/callback", s.oidcHandler.HandleCallback)
		mux.HandleFunc("/api/v1/auth/oidc/exchange", s.oidcHandler.HandleExchange)
	}

	// Streaming file upload endpoint
	mux.Handle("/api/v1/upload/", handlers.NewUploadStreamHandler(s.uploadManager, s.authManager, s.enforcer, s.log))

	// Streaming file download endpoint
	mux.Handle("/api/v1/download/", handlers.NewDownloadStreamHandler(s.downloadManager, s.authManager, s.enforcer, s.log))

	// Serve dynamic OpenAPI spec
	mux.HandleFunc("/api/v1/openapi.yaml", handlers.NewOpenAPIHandler(s.log, s.authManager.IsAnyAuthEnabled))

	// API key and credentials validation endpoint (MINE-24)
	mux.Handle("/api/v1/settings/validate-key", handlers.NewKeyValidatorHandler(s.authManager, s.enforcer, s.log))

	// Docker daemon auto-detect scan across local network interfaces
	mux.Handle("/api/v1/nodes/scan", handlers.NewNodeScanHandler(s.store, s.authManager, s.enforcer, s.log))

	// Online mod search and 1-click install endpoints (MINE-25). This legacy
	// JSON adapter delegates to the same shared operation as the generated
	// ModBrowseService and is retained for the ADR 0012 deprecation window.
	mux.Handle("/api/v1/servers/", s.modBrowseManager)

	// Packwiz Modpack Studio and Server Deployment Engine (MINE-30, MINE-31).
	// The compatibility adapter and generated PackwizService share one operation.
	packwizHandler := handlers.NewPackwizHandlerWithOperations(s.packwizOps, s.store, s.uploadManager, s.log, s.authManager, s.enforcer, s.config)
	packwizHandler.SetStorageResolver(s.storageResolver)
	mux.Handle("/api/v1/packwiz/", packwizHandler)

	// Scheduled/staged config rollout for instances (staged diffs applied on restart or cron)
	mux.Handle("/api/v1/staged-config/", handlers.NewStagedConfigHandlerWithOperations(s.stagedConfigOps, s.log, s.authManager, s.enforcer))

	// Prometheus OpenMetrics snapshots (MINE-144, PERF-5, SCH-6)
	metricsHandler := handlers.NewMetricsHandler(s.store, s.metricsCollector, s.authManager, s.enforcer, s.log)
	if s.clientPool != nil {
		metricsHandler.SetDockerPool(s.clientPool)
	}
	s.metricsHandler = metricsHandler
	mux.Handle("/api/v2/metrics/", metricsHandler)

	// Authenticated same-origin renderer proxy (ADR 0015). It must be mounted
	// before the frontend catch-all, otherwise unknown map paths return the
	// SPA shell instead of a fail-closed 401/403/503.
	if s.worldViewProxy != nil {
		mux.Handle(worldview.ProxyPrefix, s.worldViewProxy)
	}

	// Serve frontend for non-RPC routes
	s.setupFrontend(mux)

	// Default security headers on every response (MINE-163). Unencrypted
	// HTTP/2 is negotiated by the caller's http.Server via Protocols, so no
	// h2c wrapper is needed here (the deprecated h2c.NewHandler was removed).
	s.handler = securityHeaders(mux, s.config != nil && s.config.Server.TrustProxy)
}

// SetAdmissionCoordinator supplies the shared in-process admission gate to the
// server service created during registration.
func (s *Server) SetAdmissionCoordinator(c *serverruntime.AdmissionCoordinator) {
	s.admission = c
	if s.serverService != nil {
		s.serverService.SetAdmissionCoordinator(c)
	}
	if s.configService != nil {
		s.configService.SetAdmissionCoordinator(c)
	}
	if s.proxyService != nil {
		s.proxyService.SetAdmissionCoordinator(c)
	}
}

// Registers all Connect RPC service handlers.
//
// The mux is the narrow interface registerServices actually needs, so a test
// can record every mounted path and verify it against RegisteredConnectServices
// (OSS-403) without depending on http.ServeMux internals.
func (s *Server) registerServices(mux connectHandlerMux, opts []connect.HandlerOption) {
	// Create service instances
	authService := services.NewAuthService(s.store, s.authManager, s.enforcer, s.oidcHandler, s.log)
	authService.SetTrustProxy(s.config != nil && s.config.Server.TrustProxy)
	configService := services.NewConfigService(s.store, s.config, s.docker, s.clientPool, s.log)
	s.configService = configService
	configService.SetStorageResolver(s.storageResolver)
	fileService := services.NewFileService(s.store, s.docker, s.uploadManager, s.downloadManager, s.log)
	fileService.SetStorageResolver(s.storageResolver)
	fileService.SetAllowPrivateArchiveEgress(s.config != nil && s.config.Security.AllowPrivateArchiveEgress)
	s.fileService = fileService
	minecraftService := services.NewMinecraftService(s.store, s.docker, s.log)
	modService := services.NewModService(s.store, s.docker, s.uploadManager, s.log)
	modService.SetStorageResolver(s.storageResolver)
	// One planner instance serves manual review/apply and automatic or
	// scheduled policies. A nil scheduler fails closed at the task executor.
	if s.scheduler != nil {
		s.scheduler.SetModUpdatePlanner(modService)
	}
	// The generated RPC and the legacy JSON adapter share one operation
	// instance so provider, credential and audit policy cannot diverge.
	modBrowseService := services.NewModBrowseService(s.store, s.modBrowseManager)
	stagedConfigService := services.NewStagedConfigService(s.store, s.stagedConfigOps, s.enforcer)
	packwizService := services.NewPackwizService(s.packwizOps)
	modpackService := services.NewModpackService(s.store, s.config, s.uploadManager, s.log)
	datapackService := services.NewDatapackService(s.store, s.config, s.uploadManager, s.log)
	datapackService.SetStorageResolver(s.storageResolver)
	nodeService := services.NewNodeService(s.store, s.clientPool, s.log)
	proxyService := services.NewProxyService(s.store, s.docker, s.clientPool, s.proxyManager, s.config, s.logStreamer, s.log)
	s.proxyService = proxyService
	serverService := services.NewServerService(s.store, s.docker, s.sender, s.config, s.proxyManager, s.logStreamer, s.metricsCollector, s.moduleManager, s.bus, s.log, s.clientPool, s.placementEngine, s.enforcer)
	s.serverService = serverService
	if s.admission != nil {
		serverService.SetAdmissionCoordinator(s.admission)
	}
	serverService.SetStorageResolver(s.storageResolver)
	serverService.SetServerArchiveDownloads(s.downloadManager)
	serverService.SetServerArchiveUploads(s.uploadManager)
	if finished, err := serverService.ResumeUnsettledDispositions(context.Background()); err != nil {
		s.log.Error("Failed to list unfinished deletions: %v", err)
	} else if finished > 0 {
		s.log.Warn("Resumed %d unfinished deletion(s)", finished)
	}
	supportService := services.NewSupportService(s.store, s.docker, s.config, s.log)
	supportService.SetMetricsCollector(s.metricsCollector)
	taskService := services.NewTaskService(s.store, s.scheduler, s.log)
	activityService := services.NewActivityService(s.store, s.log)
	operationService := services.NewOperationService(s.store, s.log)
	s.operationService = operationService
	subuserService := services.NewSubuserService(s.store, s.log)
	blueprintService := services.NewBlueprintService(s.store, s.log)
	backupService := services.NewBackupService(s.store, s.docker, s.clientPool, s.config.Storage.S3, s.log)
	backupService.SetStorageResolver(s.storageResolver)
	backupService.SetWorldDataDir(s.config.Storage.DataDir)
	if err := backupService.SetBackupEncryption(s.config.Storage.BackupEncryption.KeyID, s.config.Storage.BackupEncryption.Key); err != nil {
		// Never silently run unencrypted (OSS-236): a misconfigured key fails
		// backup tasks through the scheduler's per-backup resolution and
		// refuses encrypted restores here until it is fixed.
		s.log.Error("Backup encryption is misconfigured; backup tasks and encrypted restores will fail until it is fixed: %v", err)
	}
	// Backup delete/restore/lock carry a backup id rather than a server id, so
	// the interceptor cannot scope them per-server. Inject the enforcer so the
	// service can resolve the owning server and enforce against it; without
	// this the handlers would fail closed for every caller.
	backupService.SetEnforcer(s.enforcer)
	backupService.SetScheduler(s.scheduler)
	userService := services.NewUserService(s.store, s.authManager, s.log)
	userService.SetOIDCHandler(s.oidcHandler)
	userService.SetEnforcer(s.enforcer)
	roleService := services.NewRoleService(s.store, s.enforcer, s.log)
	moduleService := services.NewModuleService(s.store, s.docker, s.moduleManager, s.proxyManager, s.authManager, s.config, s.logStreamer, s.log)
	moduleService.SetEnforcer(s.enforcer)
	moduleService.SetStorageResolver(s.storageResolver)
	uploadService := services.NewUploadService(s.uploadManager, s.config, s.log)
	// Player management uses the injected node resolver for read-only deep data
	// and the node-scoped command sender for the exec fallback. The skin cache
	// lives under the configured data directory, never a workstation path.
	playerService := services.NewConfiguredPlayerService(
		s.store, s.config, s.sender, s.storageResolver, s.metricsCollector, s.log,
		filepath.Join(s.config.Storage.DataDir, "player-skin-cache"),
	)
	s.playerService = playerService

	// World viewing is observation-only. Explicit operator endpoints ALWAYS
	// take precedence. When none is configured, discovery of an installed
	// builtin-bluemap module uses the private owned network and node-scoped
	// container metadata (never port probing or a controller-local fallback).
	// The same resolver backs the same-origin proxy, so a discovered renderer
	// can only be reached through the authenticated scoped proxy.
	worldViewConfig := worldview.Config{AllowedHosts: s.config.WorldView.AllowedHosts}
	for _, ep := range s.config.WorldView.Endpoints {
		worldViewConfig.Endpoints = append(worldViewConfig.Endpoints, worldview.Endpoint{
			NodeID:     ep.NodeID,
			ServerID:   ep.ServerID,
			Kind:       worldview.RendererKind(strings.ToLower(strings.TrimSpace(ep.Kind))),
			Host:       ep.Host,
			Port:       ep.Port,
			BasePath:   ep.BasePath,
			HealthPath: ep.HealthPath,
			PlayerFeed: ep.PlayerFeed,
		})
	}
	bluemapDiscovery := bluemapdiscovery.New(s.store, s.clientPool, s.storageResolver,
		s.config.Docker.NetworkName, s.log)
	worldViewResolver := worldview.NewResolverWithDiscovery(
		worldViewConfig,
		docker.NewLocator(s.clientPool, s.docker, s.store),
		bluemapDiscovery,
	)
	nativeMap := worldview.NewNativeMap(s.store, s.storageResolver)
	worldViewService := worldview.NewService(worldview.ServiceDeps{
		Store: s.store, Authorizer: s.enforcer, Resolver: worldViewResolver, Native: nativeMap,
		Auditor: worldview.NewAuditLogger(s.store.DB()), Log: s.log,
	})
	s.worldViewProxy = worldview.NewProxyHandler(worldview.ProxyDeps{
		Auth: s.authManager, Authorizer: s.enforcer, Subusers: s.store, Servers: s.store,
		Resolver: worldViewResolver, Sessions: worldViewService.SessionRegistry(), Native: nativeMap, Log: s.log,
	})

	// World Library (OSS-347). Its binder takes explicit lifecycle, resolver
	// and authorization dependencies; a worlds:bind grant never implies a
	// servers:stop/start grant (checked again in the lifecycle adapter).
	worldService := services.NewWorldService(s.store, s.config, s.log, s.storageResolver, s.uploadManager, services.WorldLibraryRoot(s.config))
	worldBinder := services.NewWorldBinder(s.store, s.config, s.log, s.storageResolver, s.sender, serverService, s.enforcer)
	worldService.SetBinder(worldBinder)
	worldService.ConfigureProviders()
	services.RecoverWorldBindings(context.Background(), s.store, worldBinder, s.log)

	// Register service handlers
	authPath, authHandler := carbonpanelv1connect.NewAuthServiceHandler(authService, opts...)
	mux.Handle(authPath, authHandler)

	configPath, configHandler := carbonpanelv1connect.NewConfigServiceHandler(configService, opts...)
	mux.Handle(configPath, configHandler)

	filePath, fileHandler := carbonpanelv1connect.NewFileServiceHandler(fileService, opts...)
	mux.Handle(filePath, fileHandler)

	minecraftPath, minecraftHandler := carbonpanelv1connect.NewMinecraftServiceHandler(minecraftService, opts...)
	mux.Handle(minecraftPath, minecraftHandler)

	modPath, modHandler := carbonpanelv1connect.NewModServiceHandler(modService, opts...)
	mux.Handle(modPath, modHandler)

	modBrowsePath, modBrowseHandler := carbonpanelv1connect.NewModBrowseServiceHandler(modBrowseService, opts...)
	mux.Handle(modBrowsePath, modBrowseHandler)

	stagedConfigPath, stagedConfigHandler := carbonpanelv1connect.NewStagedConfigServiceHandler(stagedConfigService, opts...)
	mux.Handle(stagedConfigPath, stagedConfigHandler)

	packwizPath, packwizHandler := carbonpanelv1connect.NewPackwizServiceHandler(packwizService, opts...)
	mux.Handle(packwizPath, packwizHandler)

	modpackPath, modpackHandler := carbonpanelv1connect.NewModpackServiceHandler(modpackService, opts...)
	mux.Handle(modpackPath, modpackHandler)

	datapackPath, datapackHandler := carbonpanelv1connect.NewDatapackServiceHandler(datapackService, opts...)
	mux.Handle(datapackPath, datapackHandler)

	proxyPath, proxyHandler := carbonpanelv1connect.NewProxyServiceHandler(proxyService, opts...)
	mux.Handle(proxyPath, proxyHandler)

	serverPath, serverHandler := carbonpanelv1connect.NewServerServiceHandler(serverService, opts...)
	mux.Handle(serverPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case carbonpanelv1connect.ServerServiceCreateServerProcedure,
			carbonpanelv1connect.ServerServiceExportServerArchiveProcedure,
			carbonpanelv1connect.ServerServicePreviewServerArchiveProcedure:
			extend := http.NewResponseController(w).SetWriteDeadline
			r = r.WithContext(services.WithArchiveResponseDeadline(r.Context(), extend))
		}
		serverHandler.ServeHTTP(w, r)
	}))

	nodePath, nodeHandler := carbonpanelv1connect.NewNodeServiceHandler(nodeService, opts...)
	mux.Handle(nodePath, nodeHandler)

	supportPath, supportHandler := carbonpanelv1connect.NewSupportServiceHandler(supportService, opts...)
	mux.Handle(supportPath, supportHandler)

	taskPath, taskHandler := carbonpanelv1connect.NewTaskServiceHandler(taskService, opts...)
	mux.Handle(taskPath, taskHandler)

	activityPath, activityHandler := carbonpanelv1connect.NewActivityServiceHandler(activityService, opts...)
	mux.Handle(activityPath, activityHandler)

	operationPath, operationHandler := carbonpanelv1connect.NewOperationServiceHandler(operationService, opts...)
	mux.Handle(operationPath, operationHandler)

	subuserPath, subuserHandler := carbonpanelv1connect.NewSubuserServiceHandler(subuserService, opts...)
	mux.Handle(subuserPath, subuserHandler)

	blueprintPath, blueprintHandler := carbonpanelv1connect.NewBlueprintServiceHandler(blueprintService, opts...)
	mux.Handle(blueprintPath, blueprintHandler)

	backupPath, backupHandler := carbonpanelv1connect.NewBackupServiceHandler(backupService, opts...)
	mux.Handle(backupPath, backupHandler)

	userPath, userHandler := carbonpanelv1connect.NewUserServiceHandler(userService, opts...)
	mux.Handle(userPath, userHandler)

	rolePath, roleHandler := carbonpanelv1connect.NewRoleServiceHandler(roleService, opts...)
	mux.Handle(rolePath, roleHandler)

	modulePath, moduleHandler := carbonpanelv1connect.NewModuleServiceHandler(moduleService, opts...)
	mux.Handle(modulePath, moduleHandler)

	uploadPath, uploadHandler := carbonpanelv1connect.NewUploadServiceHandler(uploadService, opts...)
	mux.Handle(uploadPath, uploadHandler)

	playerPath, playerHandler := carbonpanelv1connect.NewPlayerServiceHandler(playerService, opts...)
	mux.Handle(playerPath, playerHandler)

	worldViewPath, worldViewHandler := carbonpanelv1connect.NewWorldViewServiceHandler(worldViewService, opts...)
	mux.Handle(worldViewPath, worldViewHandler)

	worldPath, worldHandler := carbonpanelv1connect.NewWorldServiceHandler(worldService, opts...)
	mux.Handle(worldPath, worldHandler)
}

// The HTTP handler for the server
func (s *Server) Handler() http.Handler {
	return s.handler
}

// Creates a Connect interceptor for logging
func (s *Server) loggingInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			// Skip logging for polling endpoints
			if !s.isPollingProcedure(req.Spec().Procedure) {
				s.log.Info("RPC %s %s", req.Peer().Addr, req.Spec().Procedure)
			}
			return next(ctx, req)
		}
	}
}

// Creates a Connect interceptor for authentication and authorization.
// The implementation lives in NewAuthInterceptor so disposable fixtures can
// mount generated handlers behind the same fail-closed authorization path.
func (s *Server) authInterceptor() connect.UnaryInterceptorFunc {
	return NewAuthInterceptor(s.authManager, s.enforcer, s.store, s.log)
}

// Shutdown releases the server's background components.
//
// http.Server.Shutdown does NOT wait for hijacked connections, so without this
// the WebSocket hub kept running and every connected client's pumps stayed
// alive until the process exited. Call this before srv.Shutdown so live
// consoles are closed deliberately instead of being torn down at exit.
func (s *Server) Shutdown() {
	if s.wsHub != nil {
		s.wsHub.Stop()
	}
	if s.playerService != nil {
		s.playerService.Stop()
	}
	// OSS-387: release every controller-owned stream session, cancel their
	// bounded contexts and stop the TTL reaper on normal shutdown.
	if s.fileService != nil {
		s.fileService.Stop()
	}
}

// pollingProcedures lists endpoints that are called frequently and should be excluded from logging.
var pollingProcedures = []string{
	"/carbonpanel.v1.AuthService/GetAuthStatus",
	"/carbonpanel.v1.ServerService/ListServers",
	"/carbonpanel.v1.ServerService/GetServer",
	"/carbonpanel.v1.ServerService/GetServerLogs",
	"/carbonpanel.v1.ProxyService/GetProxyStatus",
	"/carbonpanel.v1.SupportService/GetApplicationLogs",
	"/carbonpanel.v1.UploadService/UploadChunk",
	"/carbonpanel.v1.UploadService/GetUploadStatus",
	"/carbonpanel.v1.FileService/GetExtractionStatus",
}

// Checks if a procedure is a polling endpoint or high-frequency endpoint
func (s *Server) isPollingProcedure(procedure string) bool {
	return slices.Contains(pollingProcedures, procedure)
}

// Frontend serving
func (s *Server) setupFrontend(mux *http.ServeMux) {
	// Get frontend source
	fs := s.getFrontendFS()
	if fs == nil {
		s.log.Warn("No frontend found - API only mode")
		return
	}

	// Serve frontend for root path
	mux.Handle("/", s.createFrontendHandler(fs))
}

// Get frontend fs
func (s *Server) getFrontendFS() http.FileSystem {
	// Try embedded frontend first
	if buildFS, err := web.BuildFS(); err == nil {
		s.log.Info("Using embedded frontend")
		return http.FS(buildFS)
	}
	return nil
}

// Create frontend handler
func (s *Server) createFrontendHandler(fs http.FileSystem) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only serve frontend for non-Connect paths
		if isConnectPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}

		// Try to serve the file directly (static assets like JS, CSS, images)
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}

		file, err := fs.Open(path)
		if err == nil {
			defer func() { _ = file.Close() }()
			stat, _ := file.Stat()
			setFrontendCacheHeaders(w, path)
			http.ServeContent(w, r, path, stat.ModTime(), file)
			return
		}

		// Serve index.html for client-side routing
		indexFile, err := fs.Open("/index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer func() { _ = indexFile.Close() }()

		stat, _ := indexFile.Stat()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "/index.html", stat.ModTime(), indexFile)
	}
}

// setFrontendCacheHeaders prevents browsers from running a stale bundle
// after a panel upgrade: hashed SvelteKit assets are immutable, HTML entry
// points must always revalidate.
func setFrontendCacheHeaders(w http.ResponseWriter, path string) {
	if strings.HasPrefix(path, "/_app/immutable/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	if strings.HasSuffix(path, ".html") {
		w.Header().Set("Cache-Control", "no-cache")
	}
}

// Checks if a path is a Connect RPC path
func isConnectPath(path string) bool {
	// Connect paths start with service names
	connectPrefixes := []string{
		"/carbonpanel.v1.",
		"/grpc.reflection.",
		"/connect.",
	}

	for _, prefix := range connectPrefixes {
		if len(path) > len(prefix) && path[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// extractObjectID extracts a named string field from a protobuf request message
// using reflection. Falls back to "*" if the field is missing or empty.
func extractObjectID(req connect.AnyRequest, fieldName string) string {
	msg, ok := req.Any().(proto.Message)
	if !ok {
		return "*"
	}
	fd := msg.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(fieldName))
	if fd == nil {
		return "*"
	}
	val := msg.ProtoReflect().Get(fd)
	if str := val.String(); str != "" {
		return str
	}
	return "*"
}

// RecoveryKey returns the current recovery key from the auth manager.
func (s *Server) RecoveryKey() string {
	if s.authManager == nil {
		return ""
	}
	return s.authManager.GetRecoveryKey()
}

// RecoveryKeyCreated reports whether this process generated a new recovery key.
func (s *Server) RecoveryKeyCreated() bool {
	if s.authManager == nil {
		return false
	}
	return s.authManager.RecoveryKeyCreated()
}

// dbReady reports whether the database is reachable. It backs /readyz.
func (s *Server) dbReady() error {
	if s.store == nil {
		return errors.New("storage is not initialized")
	}
	sqlDB, err := s.store.DB().DB()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return sqlDB.PingContext(ctx)
}

// SetLastObservation supplies the time of the last completed observation
// sweep. It is installed after the reconciler starts, so a server construction
// that happens first still works: the health snapshot simply falls back to each
// row's own update time.
func (s *Server) SetLastObservation(fn func() time.Time) {
	s.lastObservation = fn
}

// SetReconcilerNudge supplies the hook that converges a server immediately
// after an operator releases a lifecycle lock, instead of leaving the server
// blocked until the next safety-net sweep.
func (s *Server) SetReconcilerNudge(fn func(serverID string)) {
	if s.operationService != nil {
		s.operationService.SetReconcilerNudge(fn)
	}
}

// healthSnapshot observes each layer with a bounded deadline. A layer that
// cannot be observed is reported as unknown, never as healthy: a green answer
// must mean evidence, not a missing check.
func (s *Server) healthSnapshot(ctx context.Context) health.Snapshot {
	in := health.Input{
		StartedAt:      s.startedAt,
		CheckedAt:      time.Now(),
		StaleAfter:     health.DefaultStaleAfter,
		StartupAfter:   health.DefaultStartupAfter,
		RunningStatus:  string(storage.StatusRunning),
		StartingStatus: string(storage.StatusCreating),
	}

	if err := s.dbReady(); err != nil {
		in.Database = health.Observation{State: health.StateUnavailable, Detail: err.Error(), Source: health.SourceSQLite, ObservedAt: in.CheckedAt}
	} else {
		in.Database = health.Observation{State: health.StateOK, Source: health.SourceSQLite, ObservedAt: in.CheckedAt}
	}

	s.observeRuntime(ctx, &in)

	if s.config != nil && s.config.Proxy.Enabled && s.proxyManager == nil {
		in.Routes = health.Observation{State: health.StateUnavailable, Detail: "the proxy is enabled but its manager is not running", Source: health.SourceProxy, ObservedAt: in.CheckedAt}
	} else if s.config != nil && !s.config.Proxy.Enabled {
		in.Routes = health.Observation{State: health.StateOK, Detail: "the proxy is disabled", Source: health.SourceProxy, ObservedAt: in.CheckedAt}
	} else {
		in.Routes = health.Observation{State: health.StateOK, Source: health.SourceProxy, ObservedAt: in.CheckedAt}
	}

	if s.lastObservation != nil {
		in.ObservedAt = s.lastObservation()
	}

	if s.store != nil {
		servers, err := s.store.ListServers(ctx)
		if err != nil {
			// The database layer already reports the failure; do not invent a
			// game state from a query that did not answer.
			in.Servers = nil
			if in.Database.State == health.StateOK {
				in.Database = health.Observation{State: health.StateDegraded, Detail: "server records could not be listed: " + err.Error(), Source: health.SourceSQLite, ObservedAt: in.CheckedAt}
			}
		} else {
			for _, server := range servers {
				obs := health.ServerObservation{
					ID:        server.ID,
					Status:    string(server.Status),
					UpdatedAt: server.UpdatedAt,
					Game:      joinProbe(s.metricsCollector, server.ID),
				}
				if server.LastStarted != nil {
					obs.StartedAt = *server.LastStarted
				}
				in.Servers = append(in.Servers, obs)
			}
		}
	}

	return health.Compute(in)
}

// observeRuntime fans out Docker pings across enabled nodes. One down node is
// a degraded/unavailable node in the aggregate; the default client is only
// probed when there is no node inventory.
func (s *Server) observeRuntime(ctx context.Context, in *health.Input) {
	checkedAt := in.CheckedAt
	if s.clientPool != nil {
		probes, err := s.clientPool.ProbeNodes(ctx)
		if err != nil {
			in.Runtime = health.Observation{
				State:      health.StateUnknown,
				Detail:     "docker nodes could not be listed",
				Source:     health.SourceDockerPing,
				ObservedAt: checkedAt,
			}
			return
		}
		if len(probes) > 0 {
			in.Runtime = health.Observation{Source: health.SourceDockerPing, ObservedAt: checkedAt}
			for _, probe := range probes {
				state := health.StateOK
				if !probe.Reachable {
					state = health.StateUnavailable
				}
				in.Nodes = append(in.Nodes, health.NodeObservation{State: state})
			}
			return
		}
	}

	if s.docker == nil {
		in.Runtime = health.Observation{State: health.StateUnknown, Detail: "no Docker client is configured", Source: health.SourceDockerPing, ObservedAt: checkedAt}
		return
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	_, err := s.docker.Ping(pingCtx)
	cancel()
	if err != nil {
		in.Runtime = health.Observation{State: health.StateUnavailable, Detail: "the Docker daemon did not answer within 2s", Source: health.SourceDockerPing, ObservedAt: checkedAt}
		return
	}
	in.Runtime = health.Observation{State: health.StateOK, Source: health.SourceDockerPing, ObservedAt: checkedAt}
}

// joinProbe copies already-collected SLP or RCON freshness. It never pings
// the game here: a missing metric stays unknown.
func joinProbe(collector *metrics.Collector, serverID string) health.Probe {
	if collector == nil {
		return health.Probe{}
	}
	m := collector.GetMetrics(serverID)
	if m == nil {
		return health.Probe{}
	}
	if !m.SLPLastUpdated.IsZero() {
		return health.Probe{Source: health.SourceSLP, ObservedAt: m.SLPLastUpdated, OK: m.SLPAvailable}
	}
	if !m.RCONLastUpdated.IsZero() {
		return health.Probe{Source: health.SourceRCON, ObservedAt: m.RCONLastUpdated, OK: true}
	}
	return health.Probe{}
}

// Starts log streaming for a container
func (s *Server) StartLogStreaming(containerID string) error {
	return s.logStreamer.StartStreaming(containerID)
}

// MigrateServerLogSubscriptions re-points live console subscriptions for a
// server from its old container to a new one (MINE-108).
//
// It is the reconciler's LogMigrator: the log streamer moves its subscriber
// registry (recording an alias so a late unsubscribe still resolves), and the
// WebSocket hub updates the container key each client will unsubscribe or clean
// up with. This is what keeps a live console uninterrupted across a container
// recreation and prevents the channel/goroutine leak that followed it.
func (s *Server) MigrateServerLogSubscriptions(serverID, oldContainerID, newContainerID string) {
	if serverID == "" || newContainerID == "" || oldContainerID == newContainerID {
		return
	}

	if s.logStreamer != nil && oldContainerID != "" {
		s.logStreamer.MigrateSubscribers(oldContainerID, newContainerID)
	}

	if s.wsHub != nil {
		s.wsHub.MigrateServerContainer(serverID, newContainerID)
	}
}

// SetHibernationManager attaches the HibernationManager to expose hibernation metrics (SCH-6).
func (s *Server) SetHibernationManager(m *docker.HibernationManager) {
	if s.metricsHandler != nil {
		s.metricsHandler.SetHibernationManager(m)
	}
}
