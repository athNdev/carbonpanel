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
