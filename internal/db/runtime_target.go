// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// ErrReviewedModRuntimeUnknown means Docker configuration does not identify
// a concrete loader and Minecraft version for a reviewed mod update.
var ErrReviewedModRuntimeUnknown = errors.New("reviewed mod updates require a known Docker loader and Minecraft version")

// These image options can select a launcher independently of VERSION.
var customLauncherEnvironment = map[ModLoader][]string{
	ModLoaderFabric:   {"FABRIC_LAUNCHER", "FABRIC_LAUNCHER_URL"},
	ModLoaderQuilt:    {"QUILT_LAUNCHER", "QUILT_LAUNCHER_URL", "QUILT_INSTALLER_URL"},
	ModLoaderForge:    {"FORGE_INSTALLER", "FORGE_INSTALLER_URL"},
	ModLoaderNeoForge: {"NEOFORGE_INSTALLER"},
	ModLoaderPaper:    {"PAPER_CUSTOM_JAR", "PAPER_DOWNLOAD_URL"},
	ModLoaderPurpur:   {"PURPUR_DOWNLOAD_URL"},
}

// reviewedRuntimeTargetTx resolves the same effective target as
// ReviewedModRuntimeTarget inside an admission transaction, so a persisted
// reviewed row and its target share one writer snapshot.
func reviewedRuntimeTargetTx(tx *gorm.DB, serverID string) (ModLoader, string, error) {
	var server Server
	if err := tx.Where("id = ?", serverID).First(&server).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", ErrReviewedModRuntimeUnknown
		}
		return "", "", err
	}
	var config ServerConfig
	err := tx.Where("server_id = ?", serverID).First(&config).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var absent *ServerConfig
		return absent.EffectiveRuntimeTarget(&server)
	}
	if err != nil {
		return "", "", err
	}
	return config.EffectiveRuntimeTarget(&server)
}

// RefuseReviewedModRuntimeChange refuses a proposed server/config pair when it
// would move the effective Docker runtime target while a reviewed mod candidate
// is staged. UpdateServer may call it as an early preflight; the serialized
// settings commit re-runs the same check inside its writer transaction, so a
// concurrent admission cannot invalidate the review between that check and the
// server/config writes. It compares the effective targets rather than raw field
// inequality, so a no-op or non-runtime update still succeeds. A proposal that
// touches no runtime input passes even when the current target cannot be
// resolved, which matches the guard's behavior for unrelated config saves.
func (s *Store) RefuseReviewedModRuntimeChange(ctx context.Context, current *Server, currentConfig *ServerConfig, proposed *Server, proposedConfig *ServerConfig) error {
	return refuseReviewedModRuntimeChangeTx(s.db.WithContext(ctx), current, currentConfig, proposed, proposedConfig)
}

func refuseReviewedModRuntimeChangeTx(tx *gorm.DB, current *Server, currentConfig *ServerConfig, proposed *Server, proposedConfig *ServerConfig) error {
	if current == nil || proposed == nil || currentConfig == nil || proposedConfig == nil {
		return nil
	}
	hasStaged, err := hasStagedReviewedModCandidates(tx, current.ID)
	if err != nil || !hasStaged {
		return err
	}
	// The sync in UpdateServer overwrites the system fields from the proposed
	// server row; project them before deciding whether the request can move the
	// target and before resolving it.
	projected := *proposedConfig
	applyServerFieldsToConfig(&projected, proposed)
	if !serverRuntimeInputsChanged(current, proposed) && !configRuntimeInputsChanged(currentConfig, &projected) {
		return nil
	}
	oldLoader, oldGame, oldErr := currentConfig.EffectiveRuntimeTarget(current)
	newLoader, newGame, newErr := projected.EffectiveRuntimeTarget(proposed)
	if oldErr == nil && newErr == nil && oldLoader == newLoader && oldGame == newGame {
		return nil
	}
	return fmt.Errorf("%w: apply or discard the staged reviewed mod update before changing the Docker runtime target, or review and stage the mod update again for the new target", ErrStagedModVersionChange)
}

// serverRuntimeInputsChanged reports whether server fields that Docker reads at
// creation can move the effective runtime target independently of the config.
func serverRuntimeInputsChanged(current, proposed *Server) bool {
	if current.DockerImage != proposed.DockerImage {
		return true
	}
	return !proto.Equal(current.DockerOverrides, proposed.DockerOverrides)
}

// configRuntimeInputsChanged reports whether the proposed config diff touches a
// runtime-relevant field.
func configRuntimeInputsChanged(current, proposed *ServerConfig) bool {
	currentMap, err := ServerConfigToMap(current)
	if err != nil {
		return true
	}
	proposedMap, err := ServerConfigToMap(proposed)
	if err != nil {
		return true
	}
	return configChangesReviewedModRuntime(DiffMaps(currentMap, proposedMap))
}

// ReviewedModRuntimeTarget loads the configuration used at container creation.
func (s *Store) ReviewedModRuntimeTarget(ctx context.Context, server *Server) (ModLoader, string, error) {
	if server == nil {
		return "", "", ErrReviewedModRuntimeUnknown
	}
	cfg, err := s.GetServerConfig(ctx, server.ID)
	if err != nil && !errors.Is(err, ErrServerConfigNotFound) {
		return "", "", err
	}
	if errors.Is(err, ErrServerConfigNotFound) {
		cfg = nil
	}
	return cfg.EffectiveRuntimeTarget(server)
}

// EffectiveRuntimeTarget resolves only concrete targets. Docker omits unset
// config values, applies environment overrides last and may resolve some
// values at launch. The panel's display fields do not establish that target.
func (c *ServerConfig) EffectiveRuntimeTarget(server *Server) (ModLoader, string, error) {
	if server == nil {
		return "", "", ErrReviewedModRuntimeUnknown
	}
	env := map[string]string{}
	entrypoint := []string(nil)
	command := []string(nil)
	if server.DockerOverrides != nil {
		env = server.DockerOverrides.GetEnvironment()
		entrypoint = server.DockerOverrides.GetEntrypoint()
		command = server.DockerOverrides.GetCommand()
	}
	if len(entrypoint) != 0 || len(command) != 0 {
		return "", "", fmt.Errorf("%w: Docker launch command is overridden", ErrReviewedModRuntimeUnknown)
	}
	if strings.Contains(strings.ToLower(server.DockerImage), "itzg/minecraft-bedrock-server") {
		return "", "", fmt.Errorf("%w: Bedrock image has no Java loader", ErrReviewedModRuntimeUnknown)
	}
	if env["LOAD_ENV_FROM_FILE"] != "" || env["LOAD_ENV_FROM_ARCHIVE"] != "" || dockerEnvTrue(env["LOAD_ENV_FROM_GENERIC_PACK"]) {
		return "", "", fmt.Errorf("%w: the Docker image loads runtime environment from external content", ErrReviewedModRuntimeUnknown)
	}
	var loaderType, version, projects, deriveVersion, customJarExec string
	if c != nil {
		loaderType = NormalizeModLoaderForType(ModLoader(strings.TrimSpace(configString(c.Type))))
		version = configString(c.Version)
		projects = configString(c.ModrinthProjects)
		customJarExec = configString(c.CustomJarExec)
		if c.VersionFromModrinthProjects != nil && *c.VersionFromModrinthProjects {
			deriveVersion = "true"
		}
	}
	loaderType = strings.ToUpper(overrideRuntimeEnv(env, "TYPE", loaderType))
	version = overrideRuntimeEnv(env, "VERSION", version)
	projects = overrideRuntimeEnv(env, "MODRINTH_PROJECTS", projects)
	deriveVersion = overrideRuntimeEnv(env, "VERSION_FROM_MODRINTH_PROJECTS", deriveVersion)
	if overrideRuntimeEnv(env, "CUSTOM_JAR_EXEC", customJarExec) != "" || dockerEnvTrue(env["SETUP_ONLY"]) {
		return "", "", fmt.Errorf("%w: Docker does not use the standard server launch command", ErrReviewedModRuntimeUnknown)
	}
	if dockerEnvTrue(deriveVersion) && projects != "" {
		return "", "", fmt.Errorf("%w: VERSION is derived from Modrinth projects", ErrReviewedModRuntimeUnknown)
	}
	if env["MODPACK_PLATFORM"] != "" || env["MOD_PLATFORM"] != "" {
		return "", "", fmt.Errorf("%w: a Docker modpack platform selects the runtime", ErrReviewedModRuntimeUnknown)
	}
	var loader ModLoader
	switch loaderType {
	case "FORGE", "NEOFORGE", "FABRIC", "QUILT", "BUKKIT", "SPIGOT", "PAPER",
		"PURPUR", "FOLIA", "MAGMA", "MAGMA_MAINTAINED", "KETTING",
		"MOHIST", "YOUER", "BANNER", "CATSERVER", "ARCLIGHT", "SPONGEVANILLA",
		"CRUCIBLE", "GLOWSTONE":
		loader = ModLoader(strings.ToLower(loaderType))
	case "NEOFORGED":
		loader = ModLoaderNeoForge
	case "CRAFTBUKKIT":
		loader = ModLoaderBukkit
	case "VANILLA":
		loader = ModLoaderVanilla
	case "AUTO_CURSEFORGE", "CURSEFORGE", "FTB", "FTBA", "MODRINTH", "GTNH":
		return "", "", fmt.Errorf("%w: the selected modpack determines the runtime", ErrReviewedModRuntimeUnknown)
	case "PUFFERFISH":
		return "", "", fmt.Errorf("%w: the selected Pufferfish build determines the Minecraft version", ErrReviewedModRuntimeUnknown)
	default:
		return "", "", fmt.Errorf("%w: TYPE must identify a supported loader", ErrReviewedModRuntimeUnknown)
	}
	if version == "" || version != strings.TrimSpace(version) ||
		strings.EqualFold(version, "latest") || strings.EqualFold(version, "snapshot") {
		return "", "", fmt.Errorf("%w: VERSION must identify a specific Minecraft release", ErrReviewedModRuntimeUnknown)
	}
	if loader == ModLoaderForge && c != nil &&
		(overrideRuntimeEnv(env, "FORGE_INSTALLER", configString(c.ForgeInstaller)) != "" ||
			overrideRuntimeEnv(env, "FORGE_INSTALLER_URL", configString(c.ForgeInstallerURL)) != "") {
		return "", "", fmt.Errorf("%w: a custom Forge installer selects the runtime", ErrReviewedModRuntimeUnknown)
	}
	for _, key := range customLauncherEnvironment[loader] {
		if env[key] != "" {
			return "", "", fmt.Errorf("%w: %s selects a custom launcher", ErrReviewedModRuntimeUnknown, key)
		}
	}
	return loader, version, nil
}

func configString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func overrideRuntimeEnv(env map[string]string, key, fallback string) string {
	if value, ok := env[key]; ok {
		return value
	}
	return fallback
}

func dockerEnvTrue(value string) bool {
	switch strings.ToLower(value) {
	case "true", "yes", "on", "1":
		return true
	default:
		return false
	}
}
