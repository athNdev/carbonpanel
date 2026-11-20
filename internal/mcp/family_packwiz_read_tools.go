// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	packwizListTool      = "packwiz_list_packs"
	packwizGetTool       = "packwiz_get_pack"
	packwizReadMaxBytes  = 4 << 20
	packwizDefaultPage   = 25
	packwizMaxFieldBytes = 160
	packwizMaxIDBytes    = 255
)

type packwizReadPage struct {
	Offset int `json:"offset,omitempty" jsonschema:"Zero-based position in the current inventory."`
	Limit  int `json:"limit,omitempty" jsonschema:"Maximum items to return, from 1 to 50. Defaults to 25."`
}

type packwizGetInput struct {
	PackID string `json:"pack_id" jsonschema:"Exact Packwiz pack ID returned by packwiz_list_packs."`
	packwizReadPage
}

type packwizPageInfo struct {
	Offset     int  `json:"offset"`
	Total      int  `json:"total"`
	NextOffset *int `json:"next_offset,omitempty"`
}

func (page packwizReadPage) check(tool string) error {
	if page.Offset < 0 {
		return contentError(tool, "offset", "must be non-negative")
	}
	if page.Limit < 0 || page.Limit > contentMaxList {
		return contentError(tool, "limit", "must be between 1 and 50 when set")
	}
	return nil
}

func (page packwizReadPage) window(total int) (packwizPageInfo, int, int) {
	limit := page.Limit
	if limit == 0 {
		limit = packwizDefaultPage
	}
	start := min(page.Offset, total)
	end := start + min(limit, total-start)
	info := packwizPageInfo{Offset: page.Offset, Total: total}
	if end < total {
		info.NextOffset = &end
	}
	return info, start, end
}

type packwizPackHeader struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Author        string `json:"author"`
	Version       string `json:"version"`
	MCVersion     string `json:"mc_version"`
	ModLoader     string `json:"mod_loader"`
	LoaderVersion string `json:"loader_version"`
	UpdatedAt     string `json:"updated_at"`
}

type packwizPackSummary struct {
	packwizPackHeader
	ModCount int32 `json:"mod_count"`
}

type packwizContentEntry struct {
	Kind      string `json:"kind"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	Platform  string `json:"platform"`
	ProjectID string `json:"project_id"`
	VersionID string `json:"version_id"`
	Side      string `json:"side"`
	Pinned    bool   `json:"pinned"`
	Optional  bool   `json:"optional"`
	Default   bool   `json:"default"`
}

func packwizText(s string) string {
	return strings.ToValidUTF8(s[:min(len(s), packwizMaxFieldBytes)], "")
}

func packwizID(s string) string {
	return strings.ToValidUTF8(s[:min(len(s), packwizMaxIDBytes)], "")
}

// The controller's errors can contain filesystem details. Keep the shared
// status classification and use fixed text instead of reflecting raw errors.
func packwizReadFailure(tool string, err error) error {
	result := toToolError(contentSpecFor(tool), err)
	switch result.Kind {
	case ErrKindCapability:
		// contentFailure supplies the minimum build and safe status text.
	case ErrKindNotFound:
		result.Message = "Packwiz pack not found."
	case ErrKindPermissionDenied:
		result.Message = "The API token lacks the global modpacks/read grant."
	case ErrKindUnauthenticated:
		result.Message = "The controller rejected the API token."
	default:
		result.Message = "The controller could not return the requested Packwiz inventory."
		if result.Code == connect.CodeResourceExhausted.String() {
			result.Message = "The Packwiz inventory exceeds the supported read size."
		}
	}
	return &result
}

func registerPackwizReadTools(s *sdkmcp.Server, cl *ContentClients) {
	addContentTool(s, packwizListTool, cl, listPackwizPacks)
	addContentTool(s, packwizGetTool, cl, getPackwizPack)
}

func listPackwizPacks(ctx context.Context, cl *ContentClients, page packwizReadPage) (any, error) {
	if err := page.check(packwizListTool); err != nil {
		return nil, err
	}
	resp, err := cl.Packwiz.ListPacks(ctx, connect.NewRequest(&v1.ListPacksRequest{}))
	if err != nil {
		return nil, packwizReadFailure(packwizListTool, err)
	}
	packs := resp.Msg.GetPacks()
	info, start, end := page.window(len(packs))
	out := struct {
		packwizPageInfo
		Packs []packwizPackSummary `json:"packs"`
	}{packwizPageInfo: info, Packs: make([]packwizPackSummary, 0, end-start)}
	for _, p := range packs[start:end] {
		out.Packs = append(out.Packs, packwizPackSummary{
			packwizPackHeader: packwizPackHeader{
				ID: packwizID(p.GetId()), Name: packwizText(p.GetName()), Author: packwizText(p.GetAuthor()),
				Version: packwizText(p.GetVersion()), MCVersion: packwizText(p.GetMcVersion()),
				ModLoader: packwizText(p.GetModLoader()), LoaderVersion: packwizText(p.GetLoaderVersion()),
				UpdatedAt: packwizText(p.GetUpdatedAt()),
			},
			ModCount: p.GetModCount(),
		})
	}
	return out, nil
}

func getPackwizPack(ctx context.Context, cl *ContentClients, in packwizGetInput) (any, error) {
	if err := in.check(packwizGetTool); err != nil {
		return nil, err
	}
	if in.PackID == "" || len(in.PackID) > packwizMaxIDBytes ||
		in.PackID == "." || in.PackID == ".." || strings.ContainsAny(in.PackID, "/\\") {
		return nil, contentError(packwizGetTool, "pack_id", "must be one exact Packwiz ID")
	}
	resp, err := cl.Packwiz.GetPack(ctx, connect.NewRequest(&v1.GetPackRequest{PackId: in.PackID}))
	if err != nil {
		return nil, packwizReadFailure(packwizGetTool, err)
	}
	p := resp.Msg.GetPack()
	if p == nil {
		return nil, packwizReadFailure(packwizGetTool, errors.New("missing pack"))
	}
	collections := []struct {
		kind  string
		items []*v1.PackMod
	}{
		{"mod", p.GetMods()},
		{"datapack", p.GetDatapacks()},
		{"resourcepack", p.GetResourcepacks()},
		{"shaderpack", p.GetShaderpacks()},
	}
	total := 0
	for _, collection := range collections {
		total += len(collection.items)
	}
	info, start, end := in.window(total)
	out := struct {
		packwizPageInfo
		Pack    packwizPackHeader     `json:"pack"`
		Content []packwizContentEntry `json:"content"`
	}{
		packwizPageInfo: info,
		Pack: packwizPackHeader{
			ID: packwizID(p.GetId()), Name: packwizText(p.GetName()), Author: packwizText(p.GetAuthor()),
			Version: packwizText(p.GetVersion()), MCVersion: packwizText(p.GetMcVersion()),
			ModLoader: packwizText(p.GetModLoader()), LoaderVersion: packwizText(p.GetLoaderVersion()),
			UpdatedAt: packwizText(p.GetUpdatedAt()),
		},
		Content: make([]packwizContentEntry, 0, end-start),
	}
	for _, collection := range collections {
		if end <= 0 {
			break
		}
		size := len(collection.items)
		for _, item := range collection.items[min(start, size):min(end, size)] {
			out.Content = append(out.Content, packwizContentEntry{
				Kind: collection.kind, Slug: packwizText(item.GetSlug()), Name: packwizText(item.GetName()),
				Platform: packwizText(item.GetPlatform()), ProjectID: packwizText(item.GetProjectId()),
				VersionID: packwizText(item.GetVersionId()), Side: packwizText(item.GetSide()),
				Pinned: item.GetPinned(), Optional: item.GetOptional(), Default: item.GetDefault(),
			})
		}
		start = max(0, start-size)
		end -= size
	}
	return out, nil
}
