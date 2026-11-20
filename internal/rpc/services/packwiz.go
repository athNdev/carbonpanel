// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/packwiz"
	"github.com/athNdev/carbon-panel/internal/rpc/handlers"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// PackwizService exposes the generated contract for pack control. It delegates
// to handlers.PackwizOperations, the same shared operation the legacy
// /api/v1/packwiz/packs JSON adapter uses, so pack handling and error mapping
// cannot diverge between transports.
type PackwizService struct {
	ops *handlers.PackwizOperations
}

// NewPackwizService creates the generated packwiz service.
func NewPackwizService(ops *handlers.PackwizOperations) *PackwizService {
	return &PackwizService{ops: ops}
}

func packwizConnectError(err error) error {
	var opErr *handlers.PackwizError
	if !errors.As(err, &opErr) {
		return connect.NewError(connect.CodeInternal, err)
	}
	var code connect.Code
	switch opErr.Status {
	case http.StatusBadRequest:
		code = connect.CodeInvalidArgument
	case http.StatusNotFound:
		code = connect.CodeNotFound
	default:
		code = connect.CodeInternal
	}
	return connect.NewError(code, errors.New(opErr.Message))
}

func packModToProto(m packwiz.ModItem) *v1.PackMod {
	return &v1.PackMod{
		Slug: m.Slug, Name: m.Name, FileName: m.FileName, Side: m.Side,
		Platform: m.Platform, ProjectId: m.ProjectID, VersionId: m.VersionID,
		DownloadUrl: m.DownloadURL, FileSize: m.FileSize, Pinned: m.Pinned,
		Kind: m.Kind, Optional: m.Optional, Default: m.Default,
		OptionDescription: m.OptionDescription, Hash: m.Hash,
		HashFormat: m.HashFormat, Sha512: m.SHA512,
	}
}

func packToProto(p *packwiz.Pack) *v1.Pack {
	if p == nil {
		return nil
	}
	items := func(list []packwiz.ModItem) []*v1.PackMod {
		out := make([]*v1.PackMod, 0, len(list))
		for _, m := range list {
			out = append(out, packModToProto(m))
		}
		return out
	}
	out := &v1.Pack{
		Id: p.ID, Name: p.Name, Author: p.Author, Version: p.Version,
		McVersion: p.MCVersion, ModLoader: p.ModLoader, LoaderVersion: p.LoaderVersion,
		UpdatedAt: p.UpdatedAt.String(), Mods: items(p.Mods),
		Datapacks: items(p.Datapacks), Resourcepacks: items(p.ResourcePacks),
		Shaderpacks: items(p.ShaderPacks), DatapackFolder: p.DatapackFolder,
	}
	// Options preserves the pack.toml [options] table. Keep it verbatim as JSON
	// so the generated contract does not flatten provider-specific settings.
	if len(p.Options) > 0 {
		if raw, err := json.Marshal(p.Options); err == nil {
			out.Options = string(raw)
		}
	}
	return out
}

func packSummaryToProto(p packwiz.PackSummary) *v1.PackSummary {
	return &v1.PackSummary{
		Id: p.ID, Name: p.Name, Author: p.Author, Version: p.Version,
		McVersion: p.MCVersion, ModLoader: p.ModLoader, LoaderVersion: p.LoaderVersion,
		ModCount: int32(p.ModCount), UpdatedAt: p.UpdatedAt.String(),
	}
}

func packUpdateToProto(u packwiz.ModUpdateInfo) *v1.PackUpdate {
	return &v1.PackUpdate{
		Slug: u.Slug, Name: u.Name, Kind: u.Kind, Platform: u.Platform,
		CurrentVersionId: u.CurrentVersionID, LatestVersionId: u.LatestVersionID,
		LatestFileName: u.LatestFileName, LatestDownloadUrl: u.LatestDownloadURL,
		LatestHashFormat: u.LatestHashFormat, LatestHash: u.LatestHash,
		LatestSha512: u.LatestSHA512, Changelog: u.Changelog,
		UpdateAvailable: u.UpdateAvailable, Pinned: u.Pinned,
	}
}

// ListPacks lists available Packwiz projects.
func (s *PackwizService) ListPacks(ctx context.Context, _ *connect.Request[v1.ListPacksRequest]) (*connect.Response[v1.ListPacksResponse], error) {
	packs, err := s.ops.List(ctx)
	if err != nil {
		return nil, packwizConnectError(err)
	}
	out := make([]*v1.PackSummary, 0, len(packs))
	for _, p := range packs {
		out = append(out, packSummaryToProto(p))
	}
	return connect.NewResponse(&v1.ListPacksResponse{Packs: out}), nil
}

// GetPack returns one pack with its content lists.
func (s *PackwizService) GetPack(ctx context.Context, req *connect.Request[v1.GetPackRequest]) (*connect.Response[v1.GetPackResponse], error) {
	pack, err := s.ops.Get(ctx, req.Msg.PackId)
	if err != nil {
		return nil, packwizConnectError(err)
	}
	return connect.NewResponse(&v1.GetPackResponse{Pack: packToProto(pack)}), nil
}

// ClonePack copies a pack under a new name.
func (s *PackwizService) ClonePack(ctx context.Context, req *connect.Request[v1.ClonePackRequest]) (*connect.Response[v1.ClonePackResponse], error) {
	cloned, err := s.ops.Clone(ctx, req.Msg.PackId, req.Msg.Name)
	if err != nil {
		return nil, packwizConnectError(err)
	}
	return connect.NewResponse(&v1.ClonePackResponse{Pack: packToProto(cloned)}), nil
}

// RefreshPack re-indexes a pack.
func (s *PackwizService) RefreshPack(ctx context.Context, req *connect.Request[v1.RefreshPackRequest]) (*connect.Response[v1.RefreshPackResponse], error) {
	if err := s.ops.Refresh(ctx, req.Msg.PackId); err != nil {
		return nil, packwizConnectError(err)
	}
	return connect.NewResponse(&v1.RefreshPackResponse{Success: true, Message: "Pack re-indexed and refreshed successfully"}), nil
}

// CheckUpdates lists available content updates after the shared kind filter.
func (s *PackwizService) CheckUpdates(ctx context.Context, req *connect.Request[v1.CheckUpdatesRequest]) (*connect.Response[v1.CheckUpdatesResponse], error) {
	updates, err := s.ops.CheckUpdates(ctx, req.Msg.PackId, req.Msg.Kind)
	if err != nil {
		return nil, packwizConnectError(err)
	}
	out := make([]*v1.PackUpdate, 0, len(updates))
	for _, u := range updates {
		out = append(out, packUpdateToProto(u))
	}
	return connect.NewResponse(&v1.CheckUpdatesResponse{Updates: out}), nil
}

// DeletePack removes a pack.
func (s *PackwizService) DeletePack(ctx context.Context, req *connect.Request[v1.DeletePackRequest]) (*connect.Response[v1.DeletePackResponse], error) {
	if err := s.ops.Delete(ctx, req.Msg.PackId); err != nil {
		return nil, packwizConnectError(err)
	}
	return connect.NewResponse(&v1.DeletePackResponse{Success: true}), nil
}

// protoToPackMod is the inverse of packModToProto. It lets the generated
// contract feed the same shared operation as the compatibility adapter instead
// of decoding a second, parallel mod shape.
func protoToPackMod(m *v1.PackMod) packwiz.ModItem {
	if m == nil {
		return packwiz.ModItem{}
	}
	return packwiz.ModItem{
		Slug: m.Slug, Name: m.Name, FileName: m.FileName, Side: m.Side,
		Platform: m.Platform, ProjectID: m.ProjectId, VersionID: m.VersionId,
		DownloadURL: m.DownloadUrl, FileSize: m.FileSize, Pinned: m.Pinned,
		Kind: m.Kind, Optional: m.Optional, Default: m.Default,
		OptionDescription: m.OptionDescription, Hash: m.Hash,
		HashFormat: m.HashFormat, SHA512: m.Sha512,
	}
}

func dependencyRefToProto(d packwiz.DependencyRef) *v1.PackDependencyRef {
	return &v1.PackDependencyRef{
		ProjectId: d.ProjectID, VersionId: d.VersionID,
		Title: d.Title, DependencyType: d.DependencyType,
		Platform: d.Platform,
	}
}

// AddPackMod adds one mod and, when requested, resolves required dependencies.
func (s *PackwizService) AddPackMod(ctx context.Context, req *connect.Request[v1.AddPackModRequest]) (*connect.Response[v1.AddPackModResponse], error) {
	msg := req.Msg
	result, err := s.ops.AddMod(ctx, msg.PackId, protoToPackMod(msg.Mod), msg.ResolveDependencies, msg.Loader, msg.McVersion)
	if err != nil {
		return nil, packwizConnectError(err)
	}
	out := &v1.AddPackModResponse{
		Success:         true,
		DependencyError: result.DependencyError,
		Skipped:         result.Skipped,
	}
	for _, m := range result.AddedDependencies {
		out.AddedDependencies = append(out.AddedDependencies, packModToProto(m))
	}
	for _, d := range result.Unresolved {
		out.Unresolved = append(out.Unresolved, dependencyRefToProto(d))
	}
	return connect.NewResponse(out), nil
}

// AddUrlMod adds a mod from a direct download URL.
func (s *PackwizService) AddUrlMod(ctx context.Context, req *connect.Request[v1.AddUrlModRequest]) (*connect.Response[v1.AddUrlModResponse], error) {
	msg := req.Msg
	mod, err := s.ops.AddModFromURL(ctx, msg.PackId, packwiz.DirectURLModRequest{
		Name: msg.Name, FileName: msg.FileName, DownloadURL: msg.DownloadUrl,
		Side: msg.Side, Pinned: msg.Pinned,
	})
	if err != nil {
		return nil, packwizConnectError(err)
	}
	return connect.NewResponse(&v1.AddUrlModResponse{Mod: packModToProto(*mod)}), nil
}

// UpdatePackMod changes one mod's side and pinned flag.
func (s *PackwizService) UpdatePackMod(ctx context.Context, req *connect.Request[v1.UpdatePackModRequest]) (*connect.Response[v1.UpdatePackModResponse], error) {
	msg := req.Msg
	if err := s.ops.UpdateMod(ctx, msg.PackId, msg.Slug, msg.Side, msg.Pinned); err != nil {
		return nil, packwizConnectError(err)
	}
	return connect.NewResponse(&v1.UpdatePackModResponse{Success: true}), nil
}

// DeletePackMod removes one mod from a pack.
func (s *PackwizService) DeletePackMod(ctx context.Context, req *connect.Request[v1.DeletePackModRequest]) (*connect.Response[v1.DeletePackModResponse], error) {
	msg := req.Msg
	if err := s.ops.DeleteMod(ctx, msg.PackId, msg.Slug); err != nil {
		return nil, packwizConnectError(err)
	}
	return connect.NewResponse(&v1.DeletePackModResponse{Success: true}), nil
}

// BatchPackMods applies one action to several mods. count is the number of
// successful entries; results preserves the outcome of every requested slug.
func (s *PackwizService) BatchPackMods(ctx context.Context, req *connect.Request[v1.BatchPackModsRequest]) (*connect.Response[v1.BatchPackModsResponse], error) {
	msg := req.Msg
	entries, err := s.ops.BatchMods(ctx, msg.PackId, msg.Action, msg.Slugs, msg.Side)
	if err != nil {
		return nil, packwizConnectError(err)
	}
	results := make([]*v1.BatchPackModResult, 0, len(entries))
	var succeeded int32
	for _, entry := range entries {
		results = append(results, &v1.BatchPackModResult{
			Slug:      entry.Slug,
			Success:   entry.Success,
			Message:   entry.Message,
			ErrorCode: entry.ErrorCode,
		})
		if entry.Success {
			succeeded++
		}
	}
	return connect.NewResponse(&v1.BatchPackModsResponse{
		Success: true,
		Count:   succeeded,
		Results: results,
	}), nil
}
