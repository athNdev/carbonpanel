// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package main

// This file extends the one-shot catalog generator with the combined tool
// catalog. It imports the mcp package so every tool declaration is read from
// the same machine-readable artifacts the tests consume; there is no parallel
// hand-maintained list and no second generator.

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/athNdev/carbon-panel/internal/mcp"
)

func specsFrom(source string, list []mcp.ToolSpec) []combinedSpec {
	out := make([]combinedSpec, 0, len(list))
	for _, s := range list {
		out = append(out, combinedSpec{
			Name: s.Name, Procedure: s.Procedure,
			Class: string(s.Class), Confirm: s.Confirm,
			Description: s.Description, Source: source,
		})
	}
	return out
}

// combinedToolSpecs resolves every family hand-off into one deduplicated,
// audit-filtered tool list. Only procedures classified SUPPORTED by the
// OSS-375 audit matrix are promoted: an audit exclusion (secret bootstrap,
// free-form console command, third-party egress, node TLS material, ...) stays
// a non-tool even when a family descriptor declares one.
func combinedToolSpecs() []combinedSpec {
	var all []combinedSpec
	all = append(all, specsFrom("first_slice", mcp.FirstSliceToolSpecs())...)
	all = append(all, specsFrom("server", mcp.FamilyServerToolSpecs())...)
	for _, d := range mcp.ContentToolDescriptors {
		if d.Tool == "" {
			continue
		}
		all = append(all, combinedSpec{
			Name: d.Tool, Procedure: d.Procedure, Class: string(d.Class),
			Confirm: d.Confirm, Description: d.Description, Source: "content",
		})
	}
	for _, d := range mcp.FamilyAdminDescriptors {
		if d.Tool == "" {
			continue
		}
		all = append(all, combinedSpec{
			Name: d.Tool, Procedure: d.Procedure, Class: string(d.Class),
			Confirm: d.Confirm, Description: d.Description, Source: "admin",
		})
	}
	// FamilyStreamToolSpec is a projection of an existing typed path onto a
	// catalog entry, not a callable MCP tool (its name is a procedure path).
	// Its surfaces are covered through the underlying server/content/admin
	// tools, so no stream spec is registered here.
	all = append(all, specsFrom("integration", mcp.IntegrationToolSpecs())...)

	// Keep only audit-supported procedures. A procedure absent from the
	// matrix is a generator error, not an implicit opt-in.
	supported := all[:0]
	for _, s := range all {
		op, ok := mcp.FamilyAuditOperationByProcedure(s.Procedure)
		if !ok {
			panic("mcp cataloggen: procedure absent from audit matrix: " + s.Procedure)
		}
		if !op.Supported() {
			fmt.Fprintf(os.Stderr, "cataloggen: skip audit-excluded tool %s (%s)\n", s.Name, s.Procedure)
			continue
		}
		supported = append(supported, s)
	}
	all = supported

	// Deterministic precedence: sort by source priority, then by procedure,
	// then by tool name. First-slice entries therefore win over a family
	// duplicate such as list_files/get_file.
	sort.SliceStable(all, func(i, j int) bool {
		if pi, pj := priority(all[i].Source), priority(all[j].Source); pi != pj {
			return pi < pj
		}
		if all[i].Procedure != all[j].Procedure {
			return all[i].Procedure < all[j].Procedure
		}
		return all[i].Name < all[j].Name
	})
	byName := map[string]combinedSpec{}
	byProc := map[string]combinedSpec{}
	var out []combinedSpec
	for _, s := range all {
		if _, ok := byName[s.Name]; ok {
			fmt.Fprintf(os.Stderr, "cataloggen: duplicate tool name %s (%s) dropped\n", s.Name, s.Procedure)
			continue
		}
		if _, ok := byProc[s.Procedure]; ok {
			fmt.Fprintf(os.Stderr, "cataloggen: duplicate procedure %s (%s) dropped\n", s.Procedure, s.Name)
			continue
		}
		byName[s.Name] = s
		byProc[s.Procedure] = s
		out = append(out, s)
	}
	// OSS-387 scoped streaming entry points are real MCP tools backed by
	// controller-owned authenticated frame RPCs.
	for _, s := range specsFrom("integration_stream", mcp.StreamToolSpecs()) {
		if _, dup := byName[s.Name]; dup {
			continue
		}
		byName[s.Name] = s
		out = append(out, s)
	}
	// OSS-370/371-375 byte-transport lanes. These are capability tools that
	// deliberately SHARE an audited procedure with the canonical first-slice or
	// content tool (for example download_get_file and download_stream both use
	// GetFile, and the upload tools use the content family's save/init/chunk
	// procedures). They are therefore added after the procedure-dedup pass with
	// a name-only dedup, exactly like the stream capability tools above. The
	// canonical catalog declaration keeps the first tool's class/confirm flag;
	// these extra tools are still advertised on the wire and invoked by their
	// own stdio fixtures. Every procedure must be audit-supported.
	for _, group := range [][]combinedSpec{
		specsFrom("download", mcp.DownloadToolSpecs()),
		specsFrom("upload", mcp.UploadFamilyToolSpecs()),
	} {
		for _, s := range group {
			op, ok := mcp.FamilyAuditOperationByProcedure(s.Procedure)
			if !ok || !op.Supported() {
				panic("mcp cataloggen: byte-transport tool has non-audited procedure: " + s.Name + " " + s.Procedure)
			}
			if _, dup := byName[s.Name]; dup {
				fmt.Fprintf(os.Stderr, "cataloggen: duplicate capability tool %s dropped\n", s.Name)
				continue
			}
			byName[s.Name] = s
			out = append(out, s)
		}
	}
	// Stable final order is by tool name, which is what MCP clients display.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func writeIntegrationCatalog(specs []combinedSpec) {
	var b strings.Builder
	b.WriteString("// Code generated by internal/mcp/cataloggen. DO NOT EDIT.\n")
	b.WriteString("// Sources: first-slice tools, OSS-371/372/373 family descriptors, and\n")
	b.WriteString("// integration-owned OSS-370 additions. Filtered by the OSS-375 audit matrix.\n\n")
	b.WriteString("package mcp\n\n")
	b.WriteString("var exposedTools = []ToolSpec{\n")
	for _, s := range specs {
		fmt.Fprintf(&b, "\t{Name: %q, Procedure: %q, Class: Class(%q), Confirm: %t, Description: %q, Source: %q},\n",
			s.Name, s.Procedure, s.Class, s.Confirm, s.Description, s.Source)
	}
	b.WriteString("}\n")

	// Audited live counts are emitted as constants so documentation, tests and
	// release checks report the same numbers instead of a stale hand-written
	// snapshot. Declared/unmounted counts come from the generated catalog.
	matrix := mcp.FamilyAudit()
	fmt.Fprintf(&b, "\nconst (\n\tIntegrationDeclaredRPCs = %d\n\tIntegrationMountedRPCs = %d\n\tIntegrationExcludedOps = %d\n\tIntegrationSupportedOps = %d\n\tIntegrationCombinedTools = %d\n)\n",
		matrix.Counts.DeclaredRPCs, matrix.Counts.MountedReachable,
		matrix.Counts.ExcludedByReason, matrix.Counts.SupportedOperations, len(specs))

	if err := os.WriteFile("internal/mcp/integration_catalog.go", []byte(b.String()), 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("combined tools=%d\n", len(specs))
}
