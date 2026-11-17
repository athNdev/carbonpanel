// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldlib

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ConfigControl is one version-aware control in the grouped wizard.
type ConfigControl struct {
	Key            string
	Label          string
	Group          string
	Description    string
	Classification string
	ValueType      string
	Options        []string
	DefaultValue   string
	Advanced       bool
	Expert         bool
	AppliesWhen    string
}

// ConfigPreset is a named, validated starting point for a new world.
type ConfigPreset struct {
	ID          string
	Label       string
	Description string
	Values      map[string]string
}

// ConfigSchema is the capability schema for one target version tuple.
type ConfigSchema struct {
	Edition       string
	MCVersion     string
	ModLoader     string
	DataVersion   string
	Controls      []ConfigControl
	Presets       []ConfigPreset
	ConflictRules []string
	Partial       bool
	Observations  []Observation
}

// ConfigChange is one classified change in a read-only draft diff.
type ConfigChange struct {
	Key            string
	Classification string
	Before         string
	After          string
	AppliesWhen    string
	Reason         string
}

// DraftPreview never mutates anything; it is the dry run shown before apply.
type DraftPreview struct {
	Changes         []ConfigChange
	ValidationErrors []string
	Conflicts       []string
	RestartRequired []string
	Summary         string
}

// gamerule catalog. Names changed from camelCase to snake_case in 1.21; the
// schema selects the correct spelling for the target version instead of
// showing a universal list.
type gamerule struct {
	camel  string
	snake  string
	kind   string
	note   string
	since  string
}

var gamerules = []gamerule{
	{camel: "doDaylightCycle", snake: "do_daylight_cycle", kind: "bool", note: "Affects time advancing from now on."},
	{camel: "doWeatherCycle", snake: "do_weather_cycle", kind: "bool", note: "Affects future weather only."},
	{camel: "doFireTick", snake: "do_fire_tick", kind: "bool", note: "Affects existing and future fire spread."},
	{camel: "mobGriefing", snake: "mob_griefing", kind: "bool", note: "Affects future mob interactions."},
	{camel: "keepInventory", snake: "keep_inventory", kind: "bool", note: "Affects future deaths."},
	{camel: "announceAdvancements", snake: "announce_advancements", kind: "bool", note: "Affects future advancement messages."},
	{camel: "generateStructures", snake: "generate_structures", kind: "bool", note: "Only affects chunks generated after this change."},
	{camel: "spawnMonsters", snake: "spawn_monsters", kind: "bool", note: "Affects future natural spawning."},
	{camel: "showDeathMessages", snake: "show_death_messages", kind: "bool", note: "Affects future death messages."},
}

// levelTypeOptions are version-sensitive namespaced ids from 1.16 onward; the
// legacy aliases remain for older targets.
var levelTypesModern = []string{"minecraft:normal", "minecraft:flat", "minecraft:large_biomes", "minecraft:amplified", "minecraft:single_biome_surface"}
var levelTypesLegacy = []string{"DEFAULT", "FLAT", "LARGEBIOMES", "AMPLIFIED", "SINGLE_BIOME_SURFACE"}

// parseMCVersion returns major and minor for a release version, and false when
// the version cannot be interpreted. Snapshots are intentionally unknown.
func parseMCVersion(version string) (int, int, bool) {
	version = strings.TrimSpace(version)
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil || major < 1 {
		return 0, 0, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil || minor < 0 {
		return 0, 0, false
	}
	return major, minor, true
}

// snakeGamerules reports whether the target uses snake_case gamerule names.
func snakeGamerules(version string) (bool, bool) {
	major, minor, ok := parseMCVersion(version)
	if !ok {
		return false, false
	}
	return major > 1 || (major == 1 && minor >= 21), true
}

// modernLevelTypes reports whether namespaced level-type ids are valid.
func modernLevelTypes(version string) (bool, bool) {
	major, minor, ok := parseMCVersion(version)
	if !ok {
		return false, false
	}
	return major > 1 || (major == 1 && minor >= 16), true
}

// BuildSchema derives the visible controls from a version tuple. Unknown
// versions produce a partial schema with explicit unknown observations; they
// never produce a universal list of guessed options.
func BuildSchema(edition, mcVersion, loader, dataVersion string) *ConfigSchema {
	s := &ConfigSchema{
		Edition:     edition,
		MCVersion:   mcVersion,
		ModLoader:   loader,
		DataVersion: dataVersion,
		ConflictRules: []string{
			"flat level type with a custom biome datapack requires a reviewed generator preset",
			"void preset cannot enable structure generation",
			"experimental datapacks cannot be combined with a downgrade",
		},
	}
	if mcVersion == "" {
		s.Partial = true
		s.Observations = append(s.Observations, Observation{
			Source: "panel_db", Confidence: Unknown,
			Applicability: "target version",
			Note:          "No target Minecraft version supplied; version-sensitive controls are hidden.",
		})
	}

	add := func(c ConfigControl) { s.Controls = append(s.Controls, c) }
	add(ConfigControl{Key: "level-name", Label: "Level folder", Group: "Identity", Description: "Folder name for the world family.",
		Classification: ClassCreationOnly, ValueType: "string", DefaultValue: "world", AppliesWhen: "only at creation"})
	add(ConfigControl{Key: "level-seed", Label: "Seed", Group: "Seed & type", Description: "Generation seed; has no effect on an existing level.",
		Classification: ClassCreationOnly, ValueType: "string", AppliesWhen: "only at creation"})
	add(ConfigControl{Key: "difficulty", Label: "Difficulty", Group: "Gameplay", Description: "Persisted difficulty applied at creation.",
		Classification: ClassCreationOnly, ValueType: "enum", Options: []string{"peaceful", "easy", "normal", "hard"}, DefaultValue: "normal", AppliesWhen: "at creation; live change uses a command"})
	add(ConfigControl{Key: "gamemode", Label: "Game mode", Group: "Gameplay", Description: "Default game mode for new players.",
		Classification: ClassCreationOnly, ValueType: "enum", Options: []string{"survival", "creative", "adventure", "spectator"}, DefaultValue: "survival", AppliesWhen: "at creation; live change uses a command"})

	modern, known := modernLevelTypes(mcVersion)
	if known {
		options := levelTypesLegacy
		if modern {
			options = levelTypesModern
		}
		add(ConfigControl{Key: "level-type", Label: "World type", Group: "Seed & type", Description: "World generator type for this version.",
			Classification: ClassCreationOnly, ValueType: "enum", Options: options, DefaultValue: options[0], AppliesWhen: "only at creation"})
	} else {
		s.Partial = true
		s.Observations = append(s.Observations, Observation{Source: "minecraft_catalog", Confidence: Unknown,
			Applicability: mcVersion, Note: "World type names are not verifiable for this version; the control is hidden."})
	}

	add(ConfigControl{Key: "generator-settings", Label: "Generator settings", Group: "Seed & type", Description: "Superflat layer preset or generator JSON.",
		Classification: ClassCreationOnly, ValueType: "string", Advanced: true, AppliesWhen: "only at creation"})
	add(ConfigControl{Key: "generate-structures", Label: "Generate structures", Group: "Seed & type", Description: "Whether structures generate in new chunks.",
		Classification: ClassCreationOnly + ":" + ClassLiveCommand, ValueType: "bool", DefaultValue: "true",
		AppliesWhen: "creation value; live command affects only ungenerated chunks"})

	snake, knownRules := snakeGamerules(mcVersion)
	if knownRules {
		for _, rule := range gamerules {
			key := rule.camel
			if snake {
				key = rule.snake
			}
			add(ConfigControl{Key: "gamerule." + key, Label: strings.ReplaceAll(key, "_", " "), Group: "Gamerules",
				Description: rule.note, Classification: ClassLiveCommand, ValueType: rule.kind,
				Options: []string{"true", "false"}, DefaultValue: "true", Advanced: true,
				AppliesWhen: "live command; " + rule.note})
		}
	} else {
		s.Partial = true
		s.Observations = append(s.Observations, Observation{Source: "minecraft_catalog", Confidence: Unknown,
			Applicability: mcVersion, Note: "Gamerule names are version-dependent and cannot be verified; the catalog is hidden."})
	}

	add(ConfigControl{Key: "spawn-protection", Label: "Spawn protection radius", Group: "Spawn", Description: "Protected radius around spawn.",
		Classification: ClassRestartNeeded, ValueType: "int", DefaultValue: "16", Advanced: true, AppliesWhen: "next restart"})
	add(ConfigControl{Key: "region-file-compression", Label: "Region file compression", Group: "Advanced", Description: "Image environment for region compression.",
		Classification: ClassRestartNeeded, ValueType: "enum", Options: []string{"DEFLATE", "NONE"}, DefaultValue: "DEFLATE", Advanced: true, AppliesWhen: "next restart"})
	add(ConfigControl{Key: "datapack-path", Label: "Datapack archive", Group: "Datapacks", Description: "Datapack to stage for the next world generation.",
		Classification: ClassOfflineOnly, ValueType: "string", Advanced: true, AppliesWhen: "staged for next restart"})
	add(ConfigControl{Key: "level-dat-raw", Label: "Advanced NBT", Group: "Expert", Description: "Expert-only type-preserving level.dat edit with a dry-run diff.",
		Classification: ClassUnsafe, ValueType: "json", Expert: true, AppliesWhen: "disabled: offline NBT edit requires an explicit guard"})

	s.Presets = []ConfigPreset{
		{ID: "survival", Label: "Survival default", Description: "Standard overworld with structures and normal difficulty.",
			Values: map[string]string{"difficulty": "normal", "gamemode": "survival", "generate-structures": "true"}},
		{ID: "flat", Label: "Flat / Redstone", Description: "Superflat world; structure generation is disabled.",
			Values: map[string]string{"level-type": superflatType(mcVersion), "generator-settings": superflatPreset(mcVersion), "generate-structures": "false"}},
		{ID: "void", Label: "Void", Description: "Empty world from a datapack; no structures.",
			Values: map[string]string{"level-type": superflatType(mcVersion), "generator-settings": voidPreset(mcVersion), "generate-structures": "false"}},
		{ID: "hardcore", Label: "Hardcore", Description: "Hard difficulty; requires a review of difficulty and respawn behaviour.",
			Values: map[string]string{"difficulty": "hard", "gamemode": "survival"}},
	}
	return s
}

func superflatType(mcVersion string) string {
	if modern, ok := modernLevelTypes(mcVersion); ok && modern {
		return "minecraft:flat"
	}
	return "FLAT"
}

func superflatPreset(mcVersion string) string {
	// Version 1.18 changed the superflat preset grammar; older versions use
	// the layer-string form. Both are emitted honestly for their target.
	if major, minor, ok := parseMCVersion(mcVersion); ok && (major > 1 || (major == 1 && minor >= 18)) {
		return `{"layers":[{"block":"minecraft:bedrock","height":1},{"block":"minecraft:dirt","height":2},{"block":"minecraft:grass_block","height":1}],"biome":"minecraft:plains","structures":{}}`
	}
	return "3;minecraft:bedrock,2*minecraft:dirt,minecraft:grass_block;1;biome=minecraft:plains"
}

func voidPreset(mcVersion string) string {
	if major, minor, ok := parseMCVersion(mcVersion); ok && (major > 1 || (major == 1 && minor >= 18)) {
		return `{"layers":[{"block":"minecraft:air","height":1}],"biome":"minecraft:the_void","structures":{}}`
	}
	return "1;minecraft:air;2;biome=minecraft:the_void"
}

// Preview produces a classified diff without touching files or the database.
func Preview(schema *ConfigSchema, values map[string]string, preset string) (*DraftPreview, error) {
	if schema == nil {
		return nil, fmt.Errorf("%w: no configuration schema", ErrUnknownVersion)
	}
	byKey := make(map[string]ConfigControl, len(schema.Controls))
	for _, c := range schema.Controls {
		byKey[c.Key] = c
	}
	merged := map[string]string{}
	for _, p := range schema.Presets {
		if p.ID == preset {
			for k, v := range p.Values {
				merged[k] = v
			}
			break
		}
	}
	if preset != "" && len(merged) == 0 {
		return nil, fmt.Errorf("unknown preset %q", preset)
	}
	for k, v := range values {
		merged[k] = v
	}

	preview := &DraftPreview{Summary: "No changes."}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		control, ok := byKey[k]
		if !ok {
			preview.ValidationErrors = append(preview.ValidationErrors, fmt.Sprintf("%q is not available for this Minecraft version", k))
			continue
		}
		value := merged[k]
		if control.Classification == ClassUnsafe {
			preview.ValidationErrors = append(preview.ValidationErrors, fmt.Sprintf("%q is unsafe and disabled", k))
			continue
		}
		if len(control.Options) > 0 && !contains(control.Options, value) {
			preview.ValidationErrors = append(preview.ValidationErrors, fmt.Sprintf("%q has an invalid value %q", k, value))
			continue
		}
		preview.Changes = append(preview.Changes, ConfigChange{
			Key: k, Classification: control.Classification, Before: control.DefaultValue, After: value,
			AppliesWhen: control.AppliesWhen, Reason: control.Description,
		})
		if control.Classification == ClassRestartNeeded || strings.Contains(control.Classification, ClassRestartNeeded) {
			preview.RestartRequired = append(preview.RestartRequired, k)
		}
	}
	if preview.Summary == "No changes." && len(preview.Changes) > 0 {
		preview.Summary = fmt.Sprintf("%d classified changes; dry run only, nothing applied.", len(preview.Changes))
	}
	for _, rule := range schema.ConflictRules {
		if conflict := ruleConflict(rule, merged); conflict != "" {
			preview.Conflicts = append(preview.Conflicts, conflict)
		}
	}
	return preview, nil
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func ruleConflict(rule string, values map[string]string) string {
	switch {
	case strings.Contains(rule, "flat level type") && strings.Contains(values["level-type"], "flat") && values["generator-settings"] != "":
		return "Flat world type with a custom generator needs a reviewed preset."
	case strings.Contains(rule, "void preset") && values["generate-structures"] == "true" && strings.Contains(values["generator-settings"], "the_void"):
		return "Void worlds cannot enable structure generation."
	case strings.Contains(rule, "experimental datapacks") && values["datapack-path"] != "" && values["allow-downgrade"] == "true":
		return "Experimental datapacks cannot be combined with a downgrade."
	}
	return ""
}
