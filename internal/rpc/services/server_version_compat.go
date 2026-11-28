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

	"connectrpc.com/connect"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/minecraft"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
)

// maxListedIncompatible bounds the per-file list in the version-change
// refusal so a pack directory with hundreds of jars cannot grow the error
// message without limit.
const maxListedIncompatible = 10

// refuseVersionIncompatibleContent is the UpdateServer pre-flight gate for a
// Minecraft version change. It refuses the change with failed_precondition
// when installed mods, plugins, or datapacks declare themselves incompatible
// with the target version. The gate fails open for a readable location whose
// content it cannot parse: unreadable archives and content without readable
// metadata never block a version change — the gate exists to catch the common
// case before a failed recreation persists the new version row.
//
// A server whose storage the panel cannot inspect at all — the typed
// nodestorage.ErrUnavailable case, where the node has no storage adapter or
// its controller storage root is inaccessible — fails closed instead: there is
// no way to prove the installed content is compatible, so the change is
// refused rather than silently skipping the gate before a recreation.
func (s *ServerService) refuseVersionIncompatibleContent(ctx context.Context, server *storage.Server, targetVersion string) error {
	root, err := dataRootFor(ctx, s.store, server, false, s.storageResolver)
	if err != nil {
		if nodestorage.IsUnavailable(err) {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
				"content compatibility cannot be inspected on this server's storage; refusing the Minecraft %s version change: %w",
				targetVersion, err))
		}
		if s.log != nil {
			s.log.Warn("Skipping content compatibility check for server %s: %v", server.ID, err)
		}
		return nil
	}
	levelName := ""
	if cfg, cfgErr := s.store.GetServerConfig(ctx, server.ID); cfgErr == nil && cfg != nil {
		levelName = cfg.ActualLevelName()
	}

	incompatible := incompatibleInstalledContent(root, server.ModLoader, targetVersion, levelName)
	if len(incompatible) == 0 {
		return nil
	}

	listed := incompatible
	if len(listed) > maxListedIncompatible {
		listed = listed[:maxListedIncompatible]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "incompatible with Minecraft %s", targetVersion)
	for _, item := range listed {
		detail := item.Reason
		if detail == "" {
			detail = "requires " + item.Constraint
		}
		fmt.Fprintf(&b, ": %s (%s)", item.File, detail)
	}
	if extra := len(incompatible) - len(listed); extra > 0 {
		fmt.Fprintf(&b, ", and %d more", extra)
	}
	return connect.NewError(connect.CodeFailedPrecondition, errors.New(b.String()))
}

// incompatibleInstalledContent collects the incompatible verdicts from
// installed loader content (mods/ or plugins/ per the current loader) and
// from the world's datapacks directory. levelName resolves the active save
// folder; an empty value falls back to the default world name.
func incompatibleInstalledContent(dataRoot string, loader storage.ModLoader, targetVersion string, levelName string) []minecraft.ModCompatReport {
	var out []minecraft.ModCompatReport
	for _, report := range minecraft.ScanContentCompatibility(dataRoot, loader, targetVersion) {
		if report.Status == minecraft.ModCompatIncompatible {
			out = append(out, report)
		}
	}
	for _, report := range minecraft.ScanDatapackCompatibility(minecraft.GetDatapacksPath(dataRoot, levelName), targetVersion) {
		if report.Status == minecraft.ModCompatIncompatible {
			out = append(out, report)
		}
	}
	return out
}
