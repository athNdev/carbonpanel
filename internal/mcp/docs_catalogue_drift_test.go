// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func TestMCPDocsCatalogueCountsMatchSourceOfTruth(t *testing.T) {
	declared := fmt.Sprintf("%d declared RPCs", IntegrationDeclaredRPCs)
	mounted := fmt.Sprintf("%d mounted/reachable", IntegrationMountedRPCs)
	combined := fmt.Sprintf("%d-tool combined catalogue", IntegrationCombinedTools)
	combinedAlt := fmt.Sprintf("combined catalogue of %d tools", IntegrationCombinedTools)
	excluded := fmt.Sprintf("%d excluded operations", IntegrationExcludedOps)

	checks := []struct {
		file string
		want []string
		any  []string
	}{
		{
			file: "README.md",
			want: []string{combined, declared, mounted, excluded},
		},
		{
			file: "docs/carbon-panel/src/content/docs/guides/mcp-server.mdx",
			want: []string{combined, declared, mounted, fmt.Sprintf("%d supported operations", IntegrationSupportedOps), excluded},
		},
		{
			file: "docs/runbook-mcp-smoke.md",
			want: []string{declared, mounted},
			any:  []string{combined, combinedAlt},
		},
		{
			file: "knowledge/architecture/mcp-server-rule-2.md",
			want: []string{fmt.Sprintf("enumerates the %d mounted RPCs", IntegrationMountedRPCs)},
		},
		{
			file: "docs/adr/0034-mcp-server-distribution.md",
			want: []string{
				combined,
				fmt.Sprintf("**%d declared\nRPCs**", IntegrationDeclaredRPCs),
				fmt.Sprintf("**%d are mounted/reachable**", IntegrationMountedRPCs),
				fmt.Sprintf("reduce the %d mounted reachable RPCs to %d exposed tools", IntegrationMountedRPCs, IntegrationCombinedTools),
			},
		},
		{
			file: "docs/evidence/mcp-gate-separation.md",
			want: []string{
				fmt.Sprintf("supported=%d listed=%d reachable=%d verified=%d excluded=%d",
					IntegrationSupportedOps, IntegrationSupportedOps, IntegrationSupportedOps, IntegrationSupportedOps, IntegrationExcludedOps),
			},
		},
	}

	for _, c := range checks {
		content := readRepoFile(t, c.file)
		for _, want := range c.want {
			if !strings.Contains(content, want) {
				t.Errorf("%s: missing source-of-truth count/claim %q", c.file, want)
			}
		}
		if len(c.any) > 0 {
			found := false
			for _, want := range c.any {
				if strings.Contains(content, want) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: missing any of source-of-truth claims %q", c.file, c.any)
			}
		}
	}
}
