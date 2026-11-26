// SPDX-License-Identifier: MIT AND MPL-2.0
// This file includes DiscoPanel material under the MIT License,
// Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
// and still applies to that inherited material.
// Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
// If a copy of the MPL was not distributed with this file, You can obtain
// one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	shellparse "github.com/arkady-emelyanov/go-shellparse"
	"github.com/athNdev/carbon-panel/internal/alias"
	"github.com/athNdev/carbon-panel/internal/config"
	models "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/pkg/files"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// Module sidecars inherit the game-container confinement baseline. A template
// may opt into exactly one named exception profile; that exception is granted
// only after the RPC layer checks servers/manage_docker_privileged (see
// ModuleService.CreateModule). Unknown profiles always fail closed.
const (
	ModuleSecurityProfileMetadataKey      = "security_profile"
	ModuleSecurityCapAddMetadataKey       = "security_cap_add"
	ModuleSecurityWritableRootMetadataKey = "security_writable_root"
	ModuleProfileHardened                 = "hardened"
	ModuleProfileHostExtension            = "host-extension"
)

// allowedModuleExceptionCaps is the small, named set a host-extension sidecar
// may request. SYS_ADMIN, NET_ADMIN and a writable Docker socket are not here:
// those remain operator-only broad grants, not a builtin module profile.
var allowedModuleExceptionCaps = map[string]bool{
	"CHOWN":            true,
	"DAC_OVERRIDE":     true,
	"SETGID":           true,
	"SETUID":           true,
	"NET_BIND_SERVICE": true,
	"NET_RAW":          true,
	"SYS_PTRACE":       true,
	"SYS_RESOURCE":     true,
}

// ModuleSecurityProfile returns the named confinement profile of a template.
// Absent, blank and unrecognised values all mean the hardened baseline.
func ModuleSecurityProfile(template *models.ModuleTemplate) string {
	if template == nil || template.Metadata == nil {
		return ModuleProfileHardened
	}
	profile := strings.ToLower(strings.TrimSpace(template.Metadata[ModuleSecurityProfileMetadataKey]))
	if profile == "" {
		return ModuleProfileHardened
	}
	if strings.EqualFold(profile, ModuleProfileHostExtension) {
		return ModuleProfileHostExtension
	}
	return ModuleProfileHardened
}

// publishModuleHostPort reports whether Docker should publish the port on
// 0.0.0.0. Proxy-only ports stay unpublished. Public ports, including Geyser
// Bedrock UDP, are published even when proxy routing is also enabled.
func publishModuleHostPort(port *v1.ModulePort) bool {
	return port != nil && port.HostPort > 0 && (!port.ProxyEnabled || port.ExposePublic)
}

// ModuleVolumeMount represents a volume mount from module configuration
type ModuleVolumeMount struct {
	Source    string `json:"source"`
	Target    string `json:"target"`
	ReadOnly  bool   `json:"read_only,omitempty"`
	Type      string `json:"type,omitempty"`       // "bind" or "volume"
	CreateDir bool   `json:"create_dir,omitempty"` // Pre-create source dirs
}

// Create container for a module w/ optional map of sibling modules by name for inter-module references
func (c *Client) CreateModuleContainer(ctx context.Context, module *models.Module, template *models.ModuleTemplate, server *models.Server, serverConfig *models.ServerConfig, cfg *config.Config, siblingModules ...map[string]*models.Module) (string, error) {
	// Determine the Docker image to use
	imageName := template.DockerImage
	if imageName == "" {
		return "", fmt.Errorf("module template has no Docker image configured")
	}

	// A local image skips the registry. A pull that does not leave the image
	// on the daemon is a failure, including an error frame in an HTTP 200.
	if _, err := c.ensureImage(ctx, imageName); err != nil {
		return "", fmt.Errorf("module image %s is not available: %w", imageName, err)
	}

	// Build alias context for substitution (needed for env and volumes)
	aliasCtx := &alias.Context{
		Server:       server,
		ServerConfig: serverConfig,
		Module:       module,
		Config:       cfg,
	}
	// Add sibling modules for inter-module references
	if len(siblingModules) > 0 && siblingModules[0] != nil {
		aliasCtx.Modules = siblingModules[0]
	}

	// Build environment variables
	env := c.buildModuleEnv(module, server, aliasCtx)

	c.log.Debug("Creating container for module %s with image %s", module.ID, imageName)

	// Build exposed ports and port bindings from module.Ports
	exposedPorts := network.PortSet{}
	portBindings := network.PortMap{}

	for _, port := range module.Ports {
		if port == nil || port.ContainerPort == 0 {
			continue
		}

		dockerProto := "tcp"
		if port.Protocol == "udp" {
			dockerProto = "udp"
		}

		modPort, err := network.ParsePort(fmt.Sprintf("%d/%s", port.ContainerPort, dockerProto))
		if err != nil {
			c.log.Warn("Invalid module port %d/%s, skipping: %v", port.ContainerPort, dockerProto, err)
			continue
		}
		exposedPorts[modPort] = struct{}{}

		// Proxy-routed ports are not published. A port marked public (Geyser
		// Bedrock UDP) is published on every interface even when the game
		// server itself is behind Gate. Other extra ports stay unpublished
		// here; the game container binds those on loopback.
		if publishModuleHostPort(port) {
			portBindings[modPort] = []network.PortBinding{
				{HostIP: netip.MustParseAddr("0.0.0.0"), HostPort: fmt.Sprintf("%d", port.HostPort)},
			}
		}

		c.log.Debug("Added port for module %s: %s (%d:%d/%s, proxy=%t)",
			module.ID, port.Name, port.HostPort, port.ContainerPort, port.Protocol, port.ProxyEnabled)
	}

	// Build mounts from module configuration only (frontend sends complete config)
	vols, err := c.parseModuleVolumes(module.VolumeOverrides, aliasCtx)
	if err != nil {
		return "", fmt.Errorf("invalid module volume overrides: %w", err)
	}

	// Confinement roots in the panel's own filesystem view (pre-translation):
	// the owning server's data path and the global backup directory. Any
	// absolute bind source outside these was already rejected by
	// parseModuleVolumes; the MkdirAll gate below re-checks so a host
	// directory is never created as a side effect of a rejected mount.
	mkdirRoots := ModuleSafeRoots(server.DataPath, backupDirOf(cfg))
	// Mount validation runs against host-translated roots because bind
	// sources are translated to host paths in moduleVolumesToMounts.
	mountRoots := expandWithHostTranslation(mkdirRoots)
	if err := ValidateModuleVolumeMounts(vols, mountRoots...); err != nil {
		return "", err
	}

	// Prepare explicitly requested bind sources on the controller's view of
	// storage. A read-only mount stays read-only after its source exists.
	for _, vol := range vols {
		if vol.CreateDir && (vol.Type == "" || vol.Type == "bind") {
			if vol.Source == "" || vol.Target == "" {
				continue
			}
			if err := c.prepareModuleMountSource(server.DataPath, vol.Source, mkdirRoots); err != nil {
				if vol.ReadOnly {
					return "", fmt.Errorf("prepare read-only module bind source %q: %w", vol.Source, err)
				}
				c.log.Warn("Failed to pre-create mount directory %s: %v", vol.Source, err)
			}
		}
	}

	mounts := c.moduleVolumesToMounts(vols, server.DataPath, mountRoots...)

	config := &container.Config{
		Image:        imageName,
		Env:          env,
		Tty:          true,
		AttachStdout: true,
		AttachStderr: true,
		ExposedPorts: exposedPorts,
		Labels: map[string]string{
			"carbon-panel.module.id":          module.ID,
			"carbon-panel.module.name":        module.Name,
			"carbon-panel.module.server_id":   module.ServerID,
			"carbon-panel.module.template_id": module.TemplateID,
			"carbon-panel.managed":            "true",
			"carbon-panel.node.id":            module.NodeID,
			"carbon-panel.schema-version":     "2",
			// Opt out of generic third-party auto-updaters/healers so they
			// don't fight Carbon Panel's own container lifecycle management.
			"com.centurylinklabs.watchtower.enable": "false",
			"io.containrrr.watchtower.enable":       "false",
			"autoheal":                              "false",
			"willfarrell.autoheal":                  "false",
		},
	}
	c.applyInstallationLabel(config.Labels)

	// Set uid + gid
	uid, gid := alias.Substitute(module.UID, aliasCtx), alias.Substitute(module.GID, aliasCtx)
	if uid != "" || gid != "" {
		config.User = fmt.Sprintf("%s:%s", uid, gid)
	}

	// Set container command if specified (module override takes precedence over template default)
	cmd := module.CmdOverride
	if cmd == "" {
		cmd = template.DefaultCmd
	}
	if cmd != "" {
		// Parse command string into args using shell parsing lib
		cmdArgs, err := shellparse.StringToSlice(cmd)
		if err != nil {
			c.log.Warn("Failed to parse module command %q: %v, using as single arg", cmd, err)
			cmdArgs = []string{cmd}
		}
		config.Cmd = cmdArgs
		c.log.Debug("Setting container command for module %s: %v", module.ID, config.Cmd)
	}

	// Configure resources
	memory := int64(module.Memory)
	if memory == 0 {
		memory = 512 // Default 512MB
	}

	hostConfig := &container.HostConfig{
		PortBindings:  portBindings,
		Mounts:        mounts,
		RestartPolicy: DefaultRestartPolicy,
		Resources: container.Resources{
			Memory:     memory * 1024 * 1024,
			MemorySwap: memory * 1024 * 1024,
		},
		LogConfig: container.LogConfig{
			Type:   "json-file",
			Config: map[string]string{"max-size": "10m", "max-file": "3"},
		},
		ExtraHosts: []string{"host.docker.internal:host-gateway"},
	}
	applyModuleConfinement(hostConfig, template)

	// Apply CPU limit if specified
	if module.CPULimit > 0 {
		hostConfig.NanoCPUs = int64(module.CPULimit * 1e9)
	}

	// Network configuration - same network as server for communication
	networkConfig := &network.NetworkingConfig{}
	if c.config.NetworkName != "" {
		networkConfig.EndpointsConfig = map[string]*network.EndpointSettings{
			c.config.NetworkName: {},
		}
	}

	containerName := fmt.Sprintf("carbon-panel-module-%s", module.ID)

	// Idempotent create (Wings pattern, MINE-104): adopt an already-alive module container with
	// this identity instead of duplicating it, or clear away a stale leftover before creating.
	if adoptedID, ok, err := c.preflightResolveForCreate(ctx, "module", module.ID, c.ResolveModuleContainer); err != nil {
		return "", err
	} else if ok {
		return adoptedID, nil
	}

	// Create the container
	containerID, err := c.createContainerWithConflictRetry(ctx, config, hostConfig, networkConfig, containerName, func() (*container.Summary, error) {
		return c.ResolveModuleContainer(ctx, module.ID)
	})
	if err != nil {
		return "", fmt.Errorf("failed to create module container: %w", err)
	}

	return containerID, nil
}

// buildModuleEnv builds environment variables for a module container
func (c *Client) buildModuleEnv(module *models.Module, server *models.Server, aliasCtx *alias.Context) []string {
	env := make([]string, 0)

	// Add Carbon Panel context variables
	env = append(env,
		fmt.Sprintf("CARBONPANEL_SERVER_ID=%s", server.ID),
		fmt.Sprintf("CARBONPANEL_SERVER_NAME=%s", server.Name),
		fmt.Sprintf("CARBONPANEL_SERVER_HOST=carbon-panel-server-%s", server.ID),
		fmt.Sprintf("CARBONPANEL_SERVER_PORT=%d", DefaultMinecraftPort),
		fmt.Sprintf("CARBONPANEL_MODULE_ID=%s", module.ID),
		fmt.Sprintf("CARBONPANEL_MODULE_NAME=%s", module.Name),
	)

	// Add module API token if available
	if module.TokenPlaintext != "" {
		env = append(env, fmt.Sprintf("CARBONPANEL_API_TOKEN=%s", module.TokenPlaintext))
	}

	// Add module environment variables (frontend sends complete config with alias substitution)
	if module.EnvOverrides != "" {
		var envOverrides map[string]string
		if err := json.Unmarshal([]byte(module.EnvOverrides), &envOverrides); err == nil {
			for key, value := range envOverrides {
				resolvedValue := alias.Substitute(value, aliasCtx)
				if alias.HasUnresolvedVariables(resolvedValue) {
					c.log.Warn("Module %s env override %q contains unresolved template variable: %s", module.ID, key, resolvedValue)
				}
				env = append(env, fmt.Sprintf("%s=%s", key, resolvedValue))
			}
		}
	}

	return env
}

// backupDirOf extracts the global backup directory, tolerating a nil config.
func backupDirOf(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.Storage.BackupDir
}

// JSON volume configuration and substitutes. Alias substitution runs first
// (attackers cannot hide a host path behind a {{...}} placeholder), then the
// result is validated against the owning server's safe roots derived from
// the alias context; an escape is returned as an error so the caller aborts
// container creation instead of mounting a host path.
func (c *Client) parseModuleVolumes(volumeJSON string, aliasCtx *alias.Context) ([]ModuleVolumeMount, error) {
	if volumeJSON == "" || volumeJSON == "[]" {
		return nil, nil
	}

	var volumes []ModuleVolumeMount
	if err := json.Unmarshal([]byte(volumeJSON), &volumes); err != nil {
		c.log.Warn("Failed to parse volume configuration: %v", err)
		return nil, nil
	}
	return resolveModuleVolumeMounts(volumes, aliasCtx, c.log)
}

// resolveModuleVolumeMounts substitutes aliases and validates the resolved
// mounts. Container creation and the stopped-export writer check share it so
// both reason about the same effective bind set.
func resolveModuleVolumeMounts(volumes []ModuleVolumeMount, aliasCtx *alias.Context, log *logger.Logger) ([]ModuleVolumeMount, error) {
	// Sub aliases in paths
	for i := range volumes {
		// Older Modules forms persist this exact template source. Resolve it
		// to the actual level so an unset Level cannot mount the data root.
		// A source explicitly chosen by the operator follows normal alias rules.
		if aliasCtx != nil && aliasCtx.Module != nil && aliasCtx.Module.TemplateID == "builtin-bluemap" &&
			volumes[i].Source == "{{server.data_path}}/{{server.config.level}}" && volumes[i].Target == "/app/world" {
			level := ""
			if aliasCtx.ServerConfig != nil {
				level = aliasCtx.ServerConfig.ActualLevelName()
			}
			if aliasCtx.Server == nil || level == "" {
				return nil, fmt.Errorf("cannot resolve BlueMap world volume without a valid configured level")
			}
			volumes[i].Source = filepath.Join(aliasCtx.Server.DataPath, level)
		} else {
			volumes[i].Source = alias.Substitute(volumes[i].Source, aliasCtx)
		}
		volumes[i].Target = alias.Substitute(volumes[i].Target, aliasCtx)
		if log != nil && alias.HasUnresolvedVariables(volumes[i].Source) {
			log.Warn("Module volume source path contains unresolved template variable: %s", volumes[i].Source)
		}
		if log != nil && alias.HasUnresolvedVariables(volumes[i].Target) {
			log.Warn("Module volume target path contains unresolved template variable: %s", volumes[i].Target)
		}
	}

	var serverDataPath, backupDir string
	if aliasCtx != nil {
		if aliasCtx.Server != nil {
			serverDataPath = aliasCtx.Server.DataPath
		}
		if aliasCtx.Config != nil {
			backupDir = aliasCtx.Config.Storage.BackupDir
		}
	}
	if err := ValidateModuleVolumeMounts(volumes, ModuleSafeRoots(serverDataPath, backupDir)...); err != nil {
		return nil, err
	}

	return volumes, nil
}

// WritableModuleDataSources resolves a module's bind mounts and reports the
// writable sources inside the owning server's data directory. Stopped export
// refuses those modules: a stopped container is not writer process-death
// qualification, and the archive read must not overlap a writable mount.
func WritableModuleDataSources(module *models.Module, server *models.Server, serverConfig *models.ServerConfig, cfg *config.Config) ([]string, error) {
	if module == nil || server == nil || server.DataPath == "" ||
		module.VolumeOverrides == "" || module.VolumeOverrides == "[]" {
		return nil, nil
	}
	var volumes []ModuleVolumeMount
	if err := json.Unmarshal([]byte(module.VolumeOverrides), &volumes); err != nil {
		return nil, fmt.Errorf("invalid module volume overrides: %w", err)
	}
	resolved, err := resolveModuleVolumeMounts(volumes, &alias.Context{
		Server: server, ServerConfig: serverConfig, Module: module, Config: cfg,
	}, nil)
	if err != nil {
		return nil, err
	}
	var sources []string
	for _, volume := range resolved {
		if volume.ReadOnly || volume.Source == "" || (volume.Type != "" && volume.Type != "bind") {
			continue
		}
		if files.WithinReal(server.DataPath, volume.Source) {
			sources = append(sources, volume.Source)
		}
	}
	return sources, nil
}

func (c *Client) prepareModuleMountSource(serverDataPath, source string, safeRoots []string) error {
	for _, root := range safeRoots {
		if !files.Within(root, source) {
			continue
		}
		if !c.nodeStorage.IsLocal && c.nodeStorage.StorageRoot != "" {
			rel, ok := relativeBindSource(serverDataPath, source)
			if !ok {
				return fmt.Errorf("source is outside the server's node storage mapping")
			}
			// A shared mount may have the same path on both hosts. MapDataPath
			// checks storage availability; NewLocal opens the existing shared
			// parent and confines directory creation to the mapped server root.
			mapped, err := c.sharedModuleBindRoot(serverDataPath, root, source)
			if err != nil {
				return err
			}
			storage, err := nodestorage.NewLocal(mapped)
			if err != nil {
				return err
			}
			defer func() { _ = storage.Close() }()
			return storage.MkdirAll(rel)
		}
		if files.WithinReal(root, source) {
			return os.MkdirAll(source, 0755)
		}
	}
	return fmt.Errorf("source resolves outside the server data and backup directories")
}

// Verify the controller's view before either creating a directory or handing
// its node-side path to Docker, which can itself create writable bind sources.
func (c *Client) sharedModuleBindRoot(serverDataPath, safeRoot, source string) (string, error) {
	mapped, err := nodestorage.MapDataPath(c.nodeStorage, serverDataPath)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(filepath.Dir(mapped)); err != nil || !info.IsDir() {
		return "", fmt.Errorf("shared storage mount is unavailable")
	}
	info, err := os.Lstat(mapped)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if info != nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("mapped server data directory is a symlink")
	}
	mappedSafeRoot := nodestorage.MapPath(c.nodeStorage, serverDataPath, safeRoot)
	mappedSource := nodestorage.MapPath(c.nodeStorage, serverDataPath, source)
	if !files.WithinReal(mappedSafeRoot, mappedSource) {
		return "", fmt.Errorf("source resolves outside its shared storage root")
	}
	return mapped, nil
}

// moduleVolumeMount converts one resolved module volume into the Docker mount
// that container creation uses. ok=false means the entry is unusable and
// creation skips it; an error is a source that cannot be represented on this
// node. The archive verification path treats both as a refusal.
func (c *Client) moduleVolumeMount(vol ModuleVolumeMount, serverDataPath string, safeRoots ...string) (mount.Mount, bool, error) {
	mountType := mount.TypeBind
	if vol.Type == "volume" {
		mountType = mount.TypeVolume
	}

	if vol.Source == "" || vol.Target == "" {
		return mount.Mount{}, false, nil
	}

	source := vol.Source
	if mountType == mount.TypeBind {
		// Validate in the panel's own filesystem view first; the translation
		// to the daemon's view follows the containment check, so a host path
		// can never turn into a mount.
		if err := ValidateVolumeSource(source, "bind", safeRoots...); err != nil {
			return mount.Mount{}, false, err
		}
		mapped, ok := c.bindSource(serverDataPath, source)
		if !ok {
			return mount.Mount{}, false, fmt.Errorf("bind source %q is outside the node's storage mapping", source)
		}
		// Identical paths qualify as a shared mapping only when the controller
		// can verify the storage it would hand to Docker.
		if !c.nodeStorage.IsLocal && c.nodeStorage.StorageRoot != "" &&
			(filepath.Clean(mapped) == filepath.Clean(source) || !nodestorage.RequiresRemoteTransport(c.nodeStorage)) {
			if _, err := c.sharedModuleBindRoot(serverDataPath, serverDataPath, source); err != nil {
				return mount.Mount{}, false, err
			}
		}
		source = mapped
	}

	return mount.Mount{
		Type:        mountType,
		Source:      source,
		Target:      vol.Target,
		ReadOnly:    vol.ReadOnly,
		BindOptions: &mount.BindOptions{CreateMountpoint: !vol.ReadOnly},
	}, true, nil
}

// Module volumes to Docker mount specs. safeRoots is the already-validated
// confinement set; entries that fail it are skipped defensively so a caller
// that bypassed parseModuleVolumes can never turn a host path into a mount.
func (c *Client) moduleVolumesToMounts(volumes []ModuleVolumeMount, serverDataPath string, safeRoots ...string) []mount.Mount {
	var mounts []mount.Mount

	for _, vol := range volumes {
		mountSpec, ok, err := c.moduleVolumeMount(vol, serverDataPath, safeRoots...)
		if err != nil {
			c.log.Warn("Skipping volume mount that cannot be represented on this node: source=%q, target=%q: %v", vol.Source, vol.Target, err)
			continue
		}
		if !ok {
			c.log.Warn("Skipping volume mount with empty source or target: source=%q, target=%q", vol.Source, vol.Target)
			continue
		}
		mounts = append(mounts, mountSpec)
	}

	return mounts
}

// ModuleBindMount is one bind mount that touches the server data directory in
// the daemon's own path vocabulary.
type ModuleBindMount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// ContainerBindMount is one live bind mount reported by the daemon.
type ContainerBindMount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// ServerDataHostPath reports the daemon-side path of a server data root for
// this node. ok=false means the storage layout cannot be represented there.
func (c *Client) ServerDataHostPath(serverDataPath string) (string, bool) {
	if serverDataPath == "" {
		return "", false
	}
	root, ok := c.bindSource(serverDataPath, serverDataPath)
	return filepath.Clean(root), ok
}

// ModuleDataMounts resolves a module's configured bind mounts exactly as
// container creation does and returns the subset that intersects the owning
// server's data directory on the daemon. It fails closed when a configured
// mount cannot be resolved or mapped, because an unverifiable mount is not a
// safe attachment state for a stopped export.
func (c *Client) ModuleDataMounts(module *models.Module, server *models.Server, serverConfig *models.ServerConfig, cfg *config.Config, siblingModules map[string]*models.Module) ([]ModuleBindMount, error) {
	if module == nil || server == nil || server.DataPath == "" {
		return nil, fmt.Errorf("module and owning server are required to resolve data mounts")
	}
	volumes, err := c.resolvedModuleVolumes(module, server, serverConfig, cfg, siblingModules)
	if err != nil {
		return nil, err
	}
	dataRoot, ok := c.ServerDataHostPath(server.DataPath)
	if !ok {
		return nil, fmt.Errorf("server data directory cannot be mapped onto the module node")
	}
	mountRoots := expandWithHostTranslation(ModuleSafeRoots(server.DataPath, backupDirOf(cfg)))
	var out []ModuleBindMount
	for _, vol := range volumes {
		mountSpec, ok, err := c.moduleVolumeMount(vol, server.DataPath, mountRoots...)
		if err != nil {
			return nil, err
		}
		if !ok || mountSpec.Type != mount.TypeBind {
			continue
		}
		if files.Within(dataRoot, mountSpec.Source) {
			out = append(out, ModuleBindMount{
				Source:   filepath.Clean(mountSpec.Source),
				Target:   mountSpec.Target,
				ReadOnly: mountSpec.ReadOnly,
			})
		}
	}
	return out, nil
}

// resolvedModuleVolumes is the verification-side parser: unlike
// parseModuleVolumes it returns invalid JSON and unresolved aliases as errors
// instead of silently treating them as an empty mount set.
func (c *Client) resolvedModuleVolumes(module *models.Module, server *models.Server, serverConfig *models.ServerConfig, cfg *config.Config, siblingModules map[string]*models.Module) ([]ModuleVolumeMount, error) {
	if module.VolumeOverrides == "" || module.VolumeOverrides == "[]" {
		return nil, nil
	}
	var volumes []ModuleVolumeMount
	if err := json.Unmarshal([]byte(module.VolumeOverrides), &volumes); err != nil {
		return nil, fmt.Errorf("invalid module volume overrides: %w", err)
	}
	aliasCtx := &alias.Context{
		Server: server, ServerConfig: serverConfig, Module: module, Config: cfg,
		Modules: siblingModules,
	}
	resolved, err := resolveModuleVolumeMounts(volumes, aliasCtx, nil)
	if err != nil {
		return nil, err
	}
	for _, vol := range resolved {
		if alias.HasUnresolvedVariables(vol.Source) || alias.HasUnresolvedVariables(vol.Target) {
			return nil, fmt.Errorf("module volume mount contains an unresolved template variable")
		}
	}
	return resolved, nil
}

// ContainerBindMounts returns the live bind mounts of a container. The daemon's
// effective mounts are authoritative even when the stored configuration has
// already moved to a newer, not-yet-applied mount set.
func (c *Client) ContainerBindMounts(ctx context.Context, containerID string) ([]ContainerBindMount, error) {
	if c == nil || c.docker == nil {
		return nil, fmt.Errorf("docker client is not available")
	}
	if containerID == "" {
		return nil, fmt.Errorf("container id is required")
	}
	ctx, cancel := boundedCall(ctx)
	defer cancel()
	inspectResult, err := c.docker.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	var mounts []ContainerBindMount
	for _, m := range inspectResult.Container.Mounts {
		if m.Type != mount.TypeBind {
			continue
		}
		mounts = append(mounts, ContainerBindMount{
			Source:   filepath.Clean(m.Source),
			Target:   m.Destination,
			ReadOnly: !m.RW,
		})
	}
	return mounts, nil
}

// applyModuleConfinement applies the game-container baseline to a sidecar's
// effective HostConfig. It is called after resource setup and before create,
// and deliberately overwrites any broadening fields so a stored or inherited
// value cannot weaken isolation. Only a named, permission-gated profile may
// relax the read-only rootfs and add capabilities from the allowlist; the
// no-new-privileges, CapDrop ALL, pids and zero-swap limits always stay on.
func applyModuleConfinement(hc *container.HostConfig, template *models.ModuleTemplate) {
	if hc == nil {
		return
	}
	hc.Privileged = false
	hc.CapDrop = []string{"ALL"}
	if !hasModuleSecurityOpt(hc, "no-new-privileges:true") {
		hc.SecurityOpt = append(hc.SecurityOpt, "no-new-privileges:true")
	}
	pidsLimit := int64(512)
	hc.PidsLimit = &pidsLimit
	// A memory limit equal to swap means the container cannot use host swap.
	hc.MemorySwap = hc.Memory

	profile := ModuleSecurityProfile(template)
	if profile == ModuleProfileHostExtension {
		hc.CapAdd = allowedModuleCapsFrom(template)
		hc.ReadonlyRootfs = true
		if template != nil && template.Metadata != nil &&
			strings.EqualFold(strings.TrimSpace(template.Metadata[ModuleSecurityWritableRootMetadataKey]), "true") {
			hc.ReadonlyRootfs = false
		}
		ensureModuleTmpfs(hc, "rw,nosuid,size=64m")
		return
	}

	hc.CapAdd = nil
	hc.ReadonlyRootfs = true
	ensureModuleTmpfs(hc, "rw,noexec,nosuid,size=64m")
}

func hasModuleSecurityOpt(hc *container.HostConfig, want string) bool {
	for _, opt := range hc.SecurityOpt {
		if opt == want {
			return true
		}
	}
	return false
}

func ensureModuleTmpfs(hc *container.HostConfig, options string) {
	if hc.Tmpfs == nil {
		hc.Tmpfs = map[string]string{}
	}
	if _, ok := hc.Tmpfs["/tmp"]; !ok {
		hc.Tmpfs["/tmp"] = options
	}
}

func allowedModuleCapsFrom(template *models.ModuleTemplate) []string {
	if template == nil || template.Metadata == nil {
		return nil
	}
	var caps []string
	seen := map[string]bool{}
	for _, name := range strings.Split(template.Metadata[ModuleSecurityCapAddMetadataKey], ",") {
		name = strings.ToUpper(strings.TrimSpace(name))
		if name == "" || seen[name] || !allowedModuleExceptionCaps[name] {
			continue
		}
		seen[name] = true
		caps = append(caps, name)
	}
	return caps
}

// GetModuleContainerIP gets the IP address of a module container on the Carbon Panel network
func (c *Client) GetModuleContainerIP(ctx context.Context, containerID string) (string, error) {
	inspectResult, err := c.docker.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return "", err
	}
	inspect := inspectResult.Container

	// Try to get IP from the configured network
	if c.config.NetworkName != "" {
		if endpoint, ok := inspect.NetworkSettings.Networks[c.config.NetworkName]; ok {
			return endpoint.IPAddress.String(), nil
		}
	}

	// Fallback to any available network
	for _, endpoint := range inspect.NetworkSettings.Networks {
		if endpoint.IPAddress.IsValid() {
			return endpoint.IPAddress.String(), nil
		}
	}

	return "", fmt.Errorf("no IP address found for container")
}
