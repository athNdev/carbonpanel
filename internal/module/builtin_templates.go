// SPDX-License-Identifier: MIT AND MPL-2.0
// This file includes DiscoPanel material under the MIT License,
// Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
// and still applies to that inherited material.
// Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
// If a copy of the MPL was not distributed with this file, You can obtain
// one at https://mozilla.org/MPL/2.0/.

package module

import (
	"context"

	storage "github.com/athNdev/carbon-panel/internal/db"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// InitBuiltinTemplates creates/updates built-in module templates
// Only includes templates with real, working Docker images
func InitBuiltinTemplates(store *storage.Store) error {
	ctx := context.Background()

	templates := []storage.ModuleTemplate{
		{
			ID:                "builtin-geyser",
			Name:              "Geyser",
			Description:       "Allows Bedrock Edition players to join Java Edition servers. Auth defaults to Floodgate. The shared plugins/floodgate/key.pem is the key both this sidecar and the Floodgate plugin read.",
			Type:              storage.ModuleTemplateTypeBuiltin,
			DockerImage:       "atharvnaphade/carbonpanel-geyser:latest",
			Category:          "proxy",
			SupportsProxy:     true,
			RequiresServer:    true,
			Icon:              "users",
			Ports:             []*v1.ModulePort{geyserBedrockPort()},
			DefaultAccessUrls: []string{"http://{{host.hostname}}:{{module.ports.Bedrock.host_port}}"},
			DefaultEnv: `{
				"PUID": "{{host.uid}}",
				"PGID": "{{host.gid}}",
				"OVERWRITE_CONFIG": "false",
				"BEDROCK_ADDRESS": "0.0.0.0",
				"BEDROCK_PORT": "{{module.ports.Bedrock.container_port}}",
				"BEDROCK_MOTD1": "GeyserMC",
				"BEDROCK_MOTD2": "Minecraft Server",
				"BEDROCK_SERVERNAME": "Geyser",
				"REMOTE_ADDRESS": "carbon-panel-server-{{server.id}}",
				"REMOTE_PORT": "25565",
				"REMOTE_AUTH_TYPE": "floodgate",
				"JAVA_PLUGINS_DIR": "/java-plugins",
				"GEYSER_FLOODGATE_KEY_FILE": "/java-plugins/floodgate/key.pem"
			}`,
			DefaultVolumes: `[
				{"source": "{{server.data_path}}/modules/geyser", "target": "/data", "read_only": false},
				{"source": "{{server.data_path}}/plugins", "target": "/java-plugins", "read_only": false, "create_dir": true}
			]`,
			Documentation:   "Geyser translates Bedrock packets to Java packets. Default auth is floodgate, so a Bedrock client cannot join as an arbitrary Java name. The entrypoint installs the pinned Floodgate Spigot jar into plugins/ when that directory is mounted, and both sides share plugins/floodgate/key.pem. Offline auth is refused unless GEYSER_ALLOW_OFFLINE_AUTH=1, which is only appropriate when the Java server itself is offline-mode behind Velocity. This module sets BEDROCK_ADDRESS=0.0.0.0 explicitly to listen on all interfaces inside its container so the panel's UDP port proxy can reach it; a standalone Geyser entrypoint without that setting binds loopback. Restrict the published UDP port to trusted networks.",
			HealthCheckPort: 19132,
			DefaultMemory:   1024,
		},
		{
			ID:             "builtin-rcon-web",
			Name:           "RCON Web Admin",
			Description:    "Web-based RCON client for remote server management with command history and multi-server support",
			Type:           storage.ModuleTemplateTypeBuiltin,
			DockerImage:    "itzg/rcon:latest",
			Category:       "management",
			SupportsProxy:  true,
			RequiresServer: true,
			Icon:           "terminal",
			Ports: []*v1.ModulePort{
				{Name: "Web", ContainerPort: 4326, HostPort: 0, Protocol: "http", ProxyEnabled: true},
				{Name: "WS", ContainerPort: 4327, HostPort: 0, Protocol: "http", ProxyEnabled: true},
			},
			DefaultAccessUrls: []string{"http://{{host.hostname}}:{{module.ports.Web.host_port}}"},
			DefaultEnv: `{
				"RWA_ADMIN": "true",
				"RWA_PASSWORD": "admin",
				"RWA_RCON_HOST": "carbon-panel-server-{{server.id}}",
				"RWA_RCON_PORT": "{{server.config.rconPort}}",
				"RWA_RCON_PASSWORD": "{{server.config.rconPassword}}",
				"RWA_WEBSOCKET_URL": "ws://{{server.proxy_hostname}}:{{module.ports.WS.host_port}}"
			}`,
			DefaultVolumes:  `[]`,
			HealthCheckPath: "/",
			HealthCheckPort: 4326,
			Documentation:   "Provides a web interface for RCON commands. RCON settings are pulled from server config. Web UI on port 4326, WebSocket on port 4327 - both need to be accessible.",
			DefaultMemory:   256,
		},
		{
			ID:             "builtin-minecraft-exporter",
			Name:           "Prometheus Exporter",
			Description:    "Exports Minecraft server metrics to Prometheus for monitoring dashboards",
			Type:           storage.ModuleTemplateTypeBuiltin,
			DockerImage:    "itzg/mc-monitor:latest",
			Category:       "monitoring",
			SupportsProxy:  true,
			RequiresServer: true,
			Icon:           "chart-bar",
			DefaultCmd:     "export-for-prometheus",
			Ports: []*v1.ModulePort{
				{Name: "Metrics", ContainerPort: 9225, HostPort: 0, Protocol: "http", ProxyEnabled: true},
			},
			DefaultAccessUrls: []string{"http://{{host.hostname}}:{{module.ports.Metrics.host_port}}/metrics"},
			DefaultEnv: `{
				"EXPORT_SERVERS": "carbon-panel-server-{{server.id}}:25565",
				"EXPORT_PORT": "{{module.ports.Metrics.container_port}}"
			}`,
			DefaultVolumes:  `[]`,
			HealthCheckPath: "/metrics",
			HealthCheckPort: 9225,
			Documentation:   "Exports server status, player count, TPS, and other metrics in Prometheus format. Connect to /metrics endpoint to scrape metrics.",
			DefaultMemory:   512,
		},
		{
			ID:          "builtin-bluemap",
			Name:        "BlueMap",
			Description: "Interactive 3D map renderer for Minecraft Java Edition worlds. The same-origin World View proxy is the only browser path; no renderer origin or port is exposed.",
			Type:        storage.ModuleTemplateTypeBuiltin,
			// Upstream mandates pinning a specific release for production
			// (minor upgrades can require manual migration). ADR 0015 verified
			// this adapter against v5.29; do not ship :latest.
			DockerImage:    "ghcr.io/bluemap-minecraft/bluemap:v5.29",
			Category:       "monitoring",
			SupportsProxy:  true,
			RequiresServer: true,
			Icon:           "map",
			DefaultCmd:     "-r -u -w",
			Ports: []*v1.ModulePort{
				{Name: "Web", ContainerPort: 8100, HostPort: 0, Protocol: "http", ProxyEnabled: true},
			},
			// No DefaultAccessUrls: ADR 0015 forbids giving the browser a
			// renderer origin/port. World View is the only supported path.
			DefaultEnv: `{}`,
			DefaultVolumes: `[
				{"source": "{{server.data_path}}/modules/bluemap/config", "target": "/app/config", "read_only": false, "create_dir": true},
				{"source": "{{server.data_path}}/{{server.config.level}}", "target": "/app/world", "read_only": true},
				{"source": "{{server.data_path}}/modules/bluemap/data", "target": "/app/data", "read_only": false, "create_dir": true},
				{"source": "{{server.data_path}}/modules/bluemap/web", "target": "/app/web", "read_only": false, "create_dir": true}
				]`,
			HealthCheckPath: "/",
			HealthCheckPort: 8100,
			Documentation:   "Renders 3D maps of Minecraft Java Edition worlds. Pinned to v5.29 (upstream requires a specific release for production). The world folder comes from the server's actual level name and is mounted read-only; non-default level folders and dimensions need matching map configs. Config, data and web assets live in the bluemap module directory. BlueMap's web server has no authentication, so it is reachable only through the authenticated same-origin World View proxy and is never published to the host. Accepting the Mojang resource download is an explicit operator consent step for NEW installs; adopted modules keep their existing config untouched.",
			DefaultMemory:   2048, // conservative default, not an upstream minimum (512 MB is documented as workable)
			DefaultUID:      "{{host.uid}}",
			DefaultGID:      "{{host.gid}}",
			// No DefaultInitCommand / restart: silently setting accept-download
			// accepted the Minecraft EULA and a Mojang download on the user's
			// behalf (OSS-388). New installs obtain explicit recorded consent
			// through ModuleService; adoption never runs an init command.
		},
		{
			ID:             "builtin-resource-pack-host",
			Name:           "Resource Pack Host",
			Description:    "Serves resource pack ZIPs from a dedicated server folder so Minecraft players can download them.",
			Type:           storage.ModuleTemplateTypeBuiltin,
			DockerImage:    "ghcr.io/static-web-server/static-web-server:2.44.0@sha256:2c1a7c3e0feaea5859307403b74e1c575f3ec1499094fc077344173d11abaae2",
			Category:       "utility",
			RequiresServer: true,
			Icon:           "package",
			Ports: []*v1.ModulePort{
				{Name: "Web", ContainerPort: 8080, Protocol: "http"},
			},
			DefaultAccessUrls: []string{"http://{{host.hostname}}:{{module.ports.Web.host_port}}"},
			DefaultEnv: `{
				"SERVER_HOST": "0.0.0.0",
				"SERVER_PORT": "{{module.ports.Web.container_port}}",
				"SERVER_ROOT": "/packs",
				"SERVER_DIRECTORY_LISTING": "false",
				"SERVER_DISABLE_SYMLINKS": "true",
				"SERVER_IGNORE_HIDDEN_FILES": "true",
				"SERVER_HEALTH": "true"
			}`,
			DefaultVolumes: `[
				{"source": "{{server.data_path}}/modules/resource-pack-host/packs", "target": "/packs", "read_only": true, "create_dir": true}
			]`,
			HealthCheckPath: "/health",
			HealthCheckPort: 8080,
			Documentation:   "Upload public resource pack ZIPs to modules/resource-pack-host/packs using Files. Start the module manually, append /your-pack.zip to the displayed access URL, and use that URL in the server's resource-pack setting. The listener is unauthenticated HTTP, so keep only files you intend to share in that folder. The module mounts it read-only and disallows hidden files, directory listings, and symbolic links.",
			DefaultMemory:   128,
			DefaultUID:      "65534",
			DefaultGID:      "65534",
		},
		{
			ID:             "builtin-status-panel",
			Name:           "Status Panel",
			Description:    "Real-time server status dashboard showing player count, TPS, memory usage, and server info via the Carbon Panel API.",
			Type:           storage.ModuleTemplateTypeBuiltin,
			DockerImage:    "atharvnaphade/carbonpanel-status:latest",
			Category:       "monitoring",
			SupportsProxy:  true,
			RequiresServer: true,
			Icon:           "monitor",
			Ports: []*v1.ModulePort{
				{Name: "Web", ContainerPort: 8181, HostPort: 0, Protocol: "http", ProxyEnabled: true},
			},
			DefaultAccessUrls: []string{"http://{{host.hostname}}:{{module.ports.Web.host_port}}"},
			DefaultEnv: `{
				"CARBONPANEL_URL": "http://host.docker.internal:{{config.server.port}}",
				"POLL_INTERVAL": "10s",
				"PORT": "{{module.ports.Web.container_port}}",
				"HOST": "0.0.0.0"
			}`,
			DefaultVolumes:  `[]`,
			HealthCheckPath: "/health",
			HealthCheckPort: 8181,
			Documentation:   "Displays a real-time status dashboard for the attached Minecraft server. Fetches status via the Carbon Panel API including player count, TPS, CPU/memory usage, and server configuration. Automatically refreshes every 10 seconds. The published web endpoint has no request authentication; it binds 0.0.0.0 inside the container so the panel's port proxy can reach it. Restrict exposure to trusted networks.",
			DefaultMemory:   512,
		},
	}

	// Upsert each template
	for _, template := range templates {
		if err := store.UpsertModuleTemplate(ctx, &template); err != nil {
			return err
		}
	}

	// Retire builtin-mc-backup: backups are a native feature (Backups tab,
	// scheduled tasks and the CreateBackup RPC run the same journaled
	// pipeline), not a sidecar. Existing installs keep working until their
	// owners remove them, so the template row is deleted only when nothing
	// references it; DeleteModuleTemplate refuses otherwise.
	if mods, err := store.ListModulesByTemplate(ctx, "builtin-mc-backup"); err != nil {
		return err
	} else if len(mods) == 0 {
		if err := store.DeleteModuleTemplate(ctx, "builtin-mc-backup"); err != nil {
			return err
		}
	}

	return nil
}

// geyserBedrockPort is the UDP port Bedrock clients dial. It stays public
// when the Java server is behind Gate, because the TCP proxy cannot carry it.
func geyserBedrockPort() *v1.ModulePort {
	return &v1.ModulePort{
		Name:          "Bedrock",
		ContainerPort: 19132,
		HostPort:      0,
		Protocol:      "udp",
		ProxyEnabled:  true,
		ExposePublic:  true,
	}
}
