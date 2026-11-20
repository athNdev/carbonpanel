// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/bluemapdiscovery"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/worldview"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

const (
	blueMapTemplateID       = worldview.BuiltinBlueMapTemplateID
	blueMapConsentSignalKey = "bluemap_consent_accepted"
	// blueMapManagedSignalKey isolates the World View managed action. Existing
	// operators creating a module from the Modules page do not need the new
	// consent metadata and keep the legacy generic path; they are not broken.
	blueMapManagedSignalKey = "bluemap_managed"
)

// prepareBuiltinBlueMap apply the OSS-388 server-side rules to a CreateModule
// request for the built-in renderer. It runs inside ModuleService under the
// existing modules/create authorization and operation flow; there is no
// parallel unguarded install RPC.
//
// handled=true means the caller must return resp/err without creating a module.
// When handled=false the caller MUST call release() after it has persisted the
// module, so concurrent retries cannot slip between the check and the create.
// A module that already exists is ADOPTED: no create, start, reconfigure,
// restart or config overwrite; every concurrent caller receives that same row.
func (s *ModuleService) prepareBuiltinBlueMap(
	ctx context.Context,
	msg *v1.CreateModuleRequest,
	server *storage.Server,
	template *storage.ModuleTemplate,
) (handled bool, resp *connect.Response[v1.CreateModuleResponse], finalize func(ok bool, moduleID string) error, err error) {
	if msg == nil || msg.TemplateId != blueMapTemplateID || msg.ServerId == "" ||
		!strings.EqualFold(strings.TrimSpace(msg.Metadata[blueMapManagedSignalKey]), "true") {
		return false, nil, nil, nil
	}

	s.blueMapInstallMu.Lock()
	var transition *docker.Transition
	release := func() {
		transition.Release()
		s.blueMapInstallMu.Unlock()
	}
	fail := func(err error) (bool, *connect.Response[v1.CreateModuleResponse], func(bool, string) error, error) {
		release()
		return true, nil, nil, err
	}

	user := auth.GetUserFromContext(ctx)
	if user == nil || user.ID == "" {
		return fail(connect.NewError(connect.CodeUnauthenticated, errors.New("blue map install requires an authenticated operator")))
	}

	// ALL authorization happens before ANY filesystem side effect. The RPC
	// interceptor checks modules/create; re-check here for direct handler calls
	// and to bind the decision to this exact server object.
	if s.enforcer == nil {
		return fail(connect.NewError(connect.CodePermissionDenied,
			errors.New("renderer cannot be created: no authorization enforcer is configured")))
	}
	allowedCreate, createErr := s.enforcer.Enforce(user.Roles, rbac.ResourceModules, rbac.ActionCreate, server.ID)
	if createErr != nil || !allowedCreate {
		reason := "modules/create permission is required to install a renderer"
		if createErr != nil {
			reason = "renderer create authorization could not be evaluated"
		}
		return fail(connect.NewError(connect.CodePermissionDenied, errors.New(reason)))
	}
	// Module creation and config staging must see the same owning world as a
	// server migration. Re-read under the existing lifecycle transition.
	transition, err = docker.TryBeginServerTransition(server.ID)
	if err != nil {
		return fail(connect.NewError(connect.CodeAborted, err))
	}
	if err := s.store.AssertNoServerOperation(ctx, server.ID); err != nil {
		return fail(connect.NewError(connect.CodeAborted, err))
	}
	current, err := s.store.GetServer(ctx, server.ID)
	if err != nil {
		return fail(connect.NewError(connect.CodeNotFound, errors.New("the server no longer exists")))
	}
	*server = *current
	node, nodeErr := bluemapdiscovery.PrivateRendererNode(ctx, s.store, server.NodeID)
	if nodeErr != nil {
		return fail(connect.NewError(connect.CodeFailedPrecondition, nodeErr))
	}
	if node.ID != "" && s.docker == nil {
		return fail(connect.NewError(connect.CodeFailedPrecondition,
			errors.New("the registered node has no configured controller Docker client for private BlueMap")))
	}
	// Singleton per server: idempotent retry/concurrent tabs converge on the
	// exact same module identity. No host port or renderer probe is involved.
	existing, listErr := s.store.ListModulesByTemplate(ctx, blueMapTemplateID)
	if listErr != nil {
		return fail(connect.NewError(connect.CodeInternal, fmt.Errorf("check existing renderer: %w", listErr)))
	}
	var currentModule *storage.Module
	var previousAssignment bool
	for _, module := range existing {
		if module == nil || module.ServerID != server.ID {
			continue
		}
		if !bluemapdiscovery.SameRendererNode(module.NodeID, server.NodeID) {
			previousAssignment = true
			continue
		}
		if currentModule == nil || module.ID < currentModule.ID {
			currentModule = module
		}
	}
	if currentModule != nil {
		resp, convErr := s.blueMapResponse(ctx, currentModule)
		release()
		return true, resp, nil, convErr
	}
	if previousAssignment {
		return fail(connect.NewError(connect.CodeFailedPrecondition,
			errors.New("a BlueMap module belongs to a previous node assignment; reconnect the old node and remove or migrate the module before retrying")))
	}

	// startImmediately is a SECOND privileged action. Check it here, before
	// world inspection and before any config staging or DB write.
	if msg.StartImmediately {
		allowedStart, startErr := s.enforcer.Enforce(user.Roles, rbac.ResourceModules, rbac.ActionStart, server.ID)
		if startErr != nil || !allowedStart {
			reason := "modules/start permission is required to start the renderer after creation"
			if startErr != nil {
				reason = "renderer start authorization could not be evaluated"
			}
			return fail(connect.NewError(connect.CodePermissionDenied, errors.New(reason)))
		}
	}

	// Explicit operator consent is a precondition, but the actor and time are
	// derived server-side from the authenticated principal and clock. Client
	// metadata for those fields is deliberately ignored.
	if !strings.EqualFold(strings.TrimSpace(msg.Metadata[blueMapConsentSignalKey]), "true") {
		return fail(connect.NewError(connect.CodeInvalidArgument,
			errors.New("explicit acceptance of the Mojang download and Minecraft EULA is required to install BlueMap")))
	}

	cfg, cfgErr := s.store.GetServerConfig(ctx, server.ID)
	if cfgErr != nil {
		return fail(connect.NewError(connect.CodeFailedPrecondition,
			errors.New("the server's world configuration could not be read; BlueMap needs an actual level folder")))
	}
	level := worldview.ActualLevelName(cfg)
	if level == "" {
		return fail(connect.NewError(connect.CodeFailedPrecondition,
			errors.New("the server's actual level name is invalid or unknown; a non-default world folder needs a valid plain folder name")))
	}

	// Pin the renderer version on the BACKEND, not by trusting a client field.
	if template == nil || template.DockerImage != "ghcr.io/bluemap-minecraft/bluemap:v5.29" {
		return fail(connect.NewError(connect.CodeFailedPrecondition,
			errors.New("the built-in BlueMap template is not the pinned v5.29 renderer; managed install refuses an unpinned image")))
	}

	// Backend world eligibility: Java Edition is the only supported edition
	// (Carbon Panel's server model is Java Edition). Read the ACTUAL level
	// folder and its dimensions through the confined node data accessor.
	if s.storageResolver == nil {
		return fail(connect.NewError(connect.CodeFailedPrecondition,
			errors.New("node-owned storage is unavailable; the world folder cannot be verified for BlueMap")))
	}
	world, inspectErr := bluemapdiscovery.InspectManagedWorld(ctx, s.storageResolver, node, server, level)
	if inspectErr != nil {
		return fail(connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"BlueMap needs a readable Minecraft Java Edition world at %q: %w", level, inspectErr)))
	}

	// Construct the managed variant entirely from server-side facts. Client
	// overrides of ports, volumes, command, init or env are discarded.
	volumes, volErr := bluemapdiscovery.ManagedVolumes(server, level)
	if volErr != nil {
		return fail(connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("derive read-only world mount: %w", volErr)))
	}
	msg.Ports = bluemapdiscovery.ManagedPorts()
	msg.VolumeOverrides = volumes
	msg.CmdOverride = "-r -u -w"
	msg.Config = ""
	msg.EnvOverrides = "{}"
	msg.InitCommand = ""
	msg.InitCommandDelay = 0
	msg.RestartAfterInit = false
	msg.AccessUrls = nil

	// Deterministic, consent-gated map config derived from the real readable
	// world (including custom level folder, _nether/_end and custom dimensions).
	// It is STAGED now and committed only after the module row persists; an
	// existing operator config is preserved, never overwritten.
	stager, stageErr := bluemapdiscovery.StageManagedConfig(ctx, s.storageResolver, node, server, world)
	if stageErr != nil {
		if errors.Is(stageErr, bluemapdiscovery.ErrManagedConfigExists) {
			return fail(connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("existing BlueMap config is preserved; adopt it manually instead: %w", stageErr)))
		}
		return fail(connect.NewError(connect.CodeInternal, fmt.Errorf("stage managed BlueMap config: %w", stageErr)))
	}

	if msg.Metadata == nil {
		msg.Metadata = make(map[string]string)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	msg.Metadata["bluemap_consent"] = "accepted"
	msg.Metadata["bluemap_consent_by"] = user.ID
	msg.Metadata["bluemap_consent_at"] = now
	// Honest setup state: the RPC returns before the container is up. The
	// module status transitions through CREATING/STARTING/ERROR; clients must
	// read it and show the next action rather than claiming a ready map.
	msg.Metadata["bluemap_setup_state"] = "STARTING"
	msg.Metadata["bluemap_next_action"] = "Check module status and logs; if ERROR, retry start. A map appears only after the configured world renders tiles."

	// The actual image is pinned above; this is provenance for operators.
	msg.Metadata["bluemap_version"] = "v5.29"

	// finalize is idempotent. ok=true commits staged config after the module
	// row persists; ok=false (or persistence failure) rolls staging back.
	// The singleton lock is always released exactly once.
	var once sync.Once
	fin := func(ok bool, moduleID string) error {
		var commitErr error
		once.Do(func() {
			if ok {
				commitErr = stager.Commit()
			}
			if !ok || commitErr != nil {
				// Rollback stays INSIDE the singleton lock so no racing caller
				// can adopt a half-installed module row. Use a fresh bounded
				// context: the RPC context may already be canceled.
				stager.Cleanup()
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if ok && commitErr != nil && moduleID != "" {
					if deleteErr := s.store.DeleteModule(cleanupCtx, moduleID); deleteErr != nil && s.log != nil {
						s.log.Error("managed BlueMap: rollback of module %s failed: %v", moduleID, deleteErr)
					}
				}
			}
			_ = stager.Close()
			release()
		})
		return commitErr
	}
	return false, nil, fin, nil
}

func (s *ModuleService) blueMapResponse(ctx context.Context, module *storage.Module) (*connect.Response[v1.CreateModuleResponse], error) {
	username := ""
	if module.CreatedBy != "" {
		username = s.resolveCreatedByUsername(ctx, module.CreatedBy)
	}
	serverName, templateName, proxyHostname := "", "", ""
	if server, err := s.store.GetServer(ctx, module.ServerID); err == nil && server != nil {
		serverName, proxyHostname = server.Name, server.ProxyHostname
	}
	if template, err := s.store.GetModuleTemplate(ctx, module.TemplateID); err == nil && template != nil {
		templateName = template.Name
	}
	return connect.NewResponse(&v1.CreateModuleResponse{
		Module: dbModuleToProto(module, serverName, templateName, proxyHostname, username),
	}), nil
}
