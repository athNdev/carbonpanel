// SPDX-License-Identifier: MPL-2.0
package worldlib

import (
	"strings"
	"testing"
)

func TestSchemaIsVersionDriven(t *testing.T) {
	modern := BuildSchema("java", "1.21.4", "VANILLA", "")
	legacy := BuildSchema("java", "1.15.2", "VANILLA", "")
	unknown := BuildSchema("java", "future-version", "VANILLA", "")

	var modernType, legacyType string
	for _, c := range modern.Controls {
		if c.Key == "level-type" {
			modernType = c.DefaultValue
		}
	}
	for _, c := range legacy.Controls {
		if c.Key == "level-type" {
			legacyType = c.DefaultValue
		}
	}
	if !strings.HasPrefix(modernType, "minecraft:") {
		t.Fatalf("modern level type = %q, want namespaced id", modernType)
	}
	if strings.HasPrefix(legacyType, "minecraft:") {
		t.Fatalf("legacy level type = %q, want legacy alias", legacyType)
	}
	if !unknown.Partial {
		t.Fatal("unknown version must produce a partial schema")
	}
	for _, c := range unknown.Controls {
		if c.Key == "level-type" || strings.HasPrefix(c.Key, "gamerule.") {
			t.Fatalf("unknown version must not guess %q", c.Key)
		}
	}
}

func TestGameruleNamesFollowVersion(t *testing.T) {
	modern := BuildSchema("java", "1.21.4", "VANILLA", "")
	legacy := BuildSchema("java", "1.20.4", "VANILLA", "")
	var modernKey, legacyKey string
	for _, c := range modern.Controls {
		if strings.Contains(strings.ToLower(c.Key), "daylight") {
			modernKey = c.Key
		}
	}
	for _, c := range legacy.Controls {
		if strings.Contains(strings.ToLower(c.Key), "daylight") {
			legacyKey = c.Key
		}
	}
	if modernKey != "gamerule.do_daylight_cycle" {
		t.Fatalf("modern gamerule = %q", modernKey)
	}
	if legacyKey != "gamerule.doDaylightCycle" {
		t.Fatalf("legacy gamerule = %q", legacyKey)
	}
}

func TestPreviewIsReadOnlyAndClassifies(t *testing.T) {
	schema := BuildSchema("java", "1.21.4", "VANILLA", "")
	preview, err := Preview(schema, map[string]string{
		"level-seed": "42", "region-file-compression": "NONE", "level-dat-raw": "{}", "bogus": "x",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.ValidationErrors) != 2 {
		t.Fatalf("validation errors = %v, want unsafe + unknown key", preview.ValidationErrors)
	}
	foundRestart := false
	for _, c := range preview.Changes {
		if strings.Contains(c.Classification, ClassRestartNeeded) {
			foundRestart = true
			if c.AppliesWhen == "" {
				t.Fatal("restart-required change must say when it applies")
			}
		}
	}
	if !foundRestart {
		t.Fatal("expected a restart-required change")
	}
}

func TestPresetConflictIsReported(t *testing.T) {
	schema := BuildSchema("java", "1.21.4", "VANILLA", "")
	preview, err := Preview(schema, map[string]string{
		"level-type": "minecraft:flat", "generator-settings": "3;...", "level-seed": "1",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Conflicts) == 0 {
		t.Fatal("flat plus custom generator must report a conflict")
	}
}

func TestSchemaGatesOnEditionLoaderAndDataVersion(t *testing.T) {
	bedrock := BuildSchema("bedrock", "1.21.4", "VANILLA", "3953")
	if !bedrock.Partial {
		t.Fatal("non-Java edition must be partial")
	}
	for _, c := range bedrock.Controls {
		if c.Key == "level-type" || strings.HasPrefix(c.Key, "gamerule.") {
			t.Fatalf("Bedrock schema must not expose Java control %q", c.Key)
		}
	}
	modded := BuildSchema("java", "1.21.4", "FABRIC", "3953")
	plugin := BuildSchema("java", "1.21.4", "PAPER", "3953")
	if !hasControl(modded, "mod-loader-profile") || hasControl(modded, "plugin-world-flags") {
		t.Fatal("modded loader controls are wrong")
	}
	if !hasControl(plugin, "plugin-world-flags") || hasControl(plugin, "mod-loader-profile") {
		t.Fatal("plugin loader controls are wrong")
	}
	unknownLoader := BuildSchema("java", "1.21.4", "some-future-loader", "3953")
	if !unknownLoader.Partial || hasControl(unknownLoader, "plugin-world-flags") {
		t.Fatal("unknown loader must be partial with no guessed controls")
	}
	noDV := BuildSchema("java", "1.21.4", "VANILLA", "")
	if hasControl(noDV, "level-dat-raw") || hasControl(noDV, "target-data-version") {
		t.Fatal("unknown data version must hide data-version-specific controls")
	}
	withDV := BuildSchema("java", "1.21.4", "VANILLA", "3953")
	if !hasControl(withDV, "target-data-version") {
		t.Fatal("known data version must expose the target control")
	}
}

func hasControl(s *ConfigSchema, key string) bool {
	for _, c := range s.Controls {
		if c.Key == key {
			return true
		}
	}
	return false
}

func TestUnsupportedReasonsAreExplicit(t *testing.T) {
	for _, key := range []string{"edition_conversion", "in_place_downgrade", "arbitrary_mod_removal", "live_nbt_edit", "shared_writable", "world_catalog_auto"} {
		reason, ok := UnsupportedReason(key)
		if !ok || reason == "" {
			t.Fatalf("missing explicit reason for %q", key)
		}
	}
}
