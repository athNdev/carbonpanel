// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rpc/handlers"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// ModBrowseService exposes the generated contract for mod browse, version
// lookup and install. It delegates to handlers.ModOnlineManager, the same
// shared operation the legacy /api/v1/servers/{id}/mods/* JSON adapter uses,
// so authorization, provider policy, error semantics and audit entries cannot
// diverge between transports.
type ModBrowseService struct {
	store   *storage.Store
	manager *handlers.ModOnlineManager
}

// NewModBrowseService creates the generated mod browse service. The manager may
// be built with handlers.NewModOnlineManager; tests inject fixture transports.
func NewModBrowseService(store *storage.Store, manager *handlers.ModOnlineManager) *ModBrowseService {
	return &ModBrowseService{store: store, manager: manager}
}

// resolveServer mirrors the legacy adapter: "none" and "" are the explicit
// Studio browse mode and intentionally resolve to no server, while an unknown
// id fails with the same "server not found" response.
func (s *ModBrowseService) resolveServer(ctx context.Context, id string) (*storage.Server, error) {
	if id == "" || id == "none" {
		return nil, nil
	}
	if s.store == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("store not initialized"))
	}
	server, err := s.store.GetServer(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}
	return server, nil
}

// modBrowseConnectError maps a shared browse failure to the Connect code that
// matches the legacy HTTP status, preserving the operation's message.
func modBrowseConnectError(err error) error {
	var browseErr *handlers.ModBrowseError
	if !errors.As(err, &browseErr) {
		return connect.NewError(connect.CodeInternal, err)
	}
	var code connect.Code
	switch browseErr.Status {
	case http.StatusBadRequest:
		code = connect.CodeInvalidArgument
	case http.StatusNotFound:
		code = connect.CodeNotFound
	case http.StatusConflict:
		code = connect.CodeAborted
	case http.StatusInternalServerError:
		code = connect.CodeInternal
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		code = connect.CodeUnavailable
	default:
		code = connect.CodeUnknown
	}
	return connect.NewError(code, errors.New(browseErr.Message))
}

// SearchMods queries the configured mod index with the shared browse policy.
func (s *ModBrowseService) SearchMods(ctx context.Context, req *connect.Request[v1.SearchModsRequest]) (*connect.Response[v1.SearchModsResponse], error) {
	server, err := s.resolveServer(ctx, req.Msg.ServerId)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	results, opErr := s.manager.Search(ctx, server, handlers.ModSearchParams{
		Query:     msg.Query,
		Platform:  msg.Platform,
		Loader:    msg.Loader,
		MCVersion: msg.McVersion,
		Side:      msg.Side,
	})
	if opErr != nil {
		return nil, modBrowseConnectError(opErr)
	}
	out := make([]*v1.BrowseModResult, 0, len(results))
	for _, r := range results {
		out = append(out, &v1.BrowseModResult{
			Id:          r.ID,
			Slug:        r.Slug,
			Title:       r.Title,
			Description: r.Description,
			IconUrl:     r.IconURL,
			Author:      r.Author,
			Downloads:   r.Downloads,
			Categories:  r.Categories,
			Platform:    r.Platform,
			ClientSide:  r.ClientSide,
			ServerSide:  r.ServerSide,
			Installed:   r.Installed,
		})
	}
	return connect.NewResponse(&v1.SearchModsResponse{Results: out, Count: int32(len(out))}), nil
}

// GetModVersions lists installable versions of one project.
func (s *ModBrowseService) GetModVersions(ctx context.Context, req *connect.Request[v1.GetModVersionsRequest]) (*connect.Response[v1.GetModVersionsResponse], error) {
	server, err := s.resolveServer(ctx, req.Msg.ServerId)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	versions, browseErr := s.manager.Versions(ctx, server, msg.Project, handlers.ModVersionParams{
		Platform:  msg.Platform,
		Loader:    msg.Loader,
		MCVersion: msg.McVersion,
	})
	if browseErr != nil {
		return nil, modBrowseConnectError(browseErr)
	}
	out := make([]*v1.ModVersion, 0, len(versions))
	for _, v := range versions {
		deps := make([]*v1.ModDependency, 0, len(v.Dependencies))
		for _, d := range v.Dependencies {
			deps = append(deps, &v1.ModDependency{
				ProjectId:      d.ProjectID,
				VersionId:      d.VersionID,
				DependencyType: d.DependencyType,
				Title:          d.Title,
			})
		}
		out = append(out, &v1.ModVersion{
			Id:            v.ID,
			VersionNumber: v.VersionNumber,
			Name:          v.Name,
			VersionType:   v.VersionType,
			FileName:      v.FileName,
			DownloadUrl:   v.DownloadURL,
			FileSize:      v.FileSize,
			Dependencies:  deps,
		})
	}
	return connect.NewResponse(&v1.GetModVersionsResponse{Versions: out, Count: int32(len(out))}), nil
}

// InstallMods downloads a batch of mod jars with the shared install policy.
func (s *ModBrowseService) InstallMods(ctx context.Context, req *connect.Request[v1.InstallModsRequest]) (*connect.Response[v1.InstallModsResponse], error) {
	server, err := s.resolveServer(ctx, req.Msg.ServerId)
	if err != nil {
		return nil, err
	}
	items := make([]handlers.InstallModItem, 0, len(req.Msg.Items))
	for _, item := range req.Msg.Items {
		items = append(items, handlers.InstallModItem{
			Name:        item.Name,
			FileName:    item.FileName,
			DownloadURL: item.DownloadUrl,
		})
	}
	actor := handlers.ModInstallActor{IP: req.Peer().Addr}
	if user := auth.GetUserFromContext(ctx); user != nil {
		actor.ID, actor.Name = user.ID, user.Username
	}
	resp, browseErr := s.manager.Install(ctx, server, items, actor)
	if browseErr != nil {
		return nil, modBrowseConnectError(browseErr)
	}
	return connect.NewResponse(&v1.InstallModsResponse{
		Success:   resp.Success,
		Installed: resp.Installed,
		Errors:    resp.Errors,
		Message:   resp.Message,
	}), nil
}
