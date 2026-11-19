// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// ModuleRuntimeMount is the EFFECTIVE bind observed on the node, not the
// template's intent. Discovery must compare real source/target/RW before it
// claims a renderer serves a server's world.
type ModuleRuntimeMount struct {
	Type     string
	Source   string
	Target   string
	ReadOnly bool
}

// ModuleRuntimeIdentity is the node-scoped, read-only container metadata for
// one module. It is the PRIVATE backend transport's identity proof: labels,
// image, state, owned-network membership and effective mounts. No host-port
// binding is trusted or required.
type ModuleRuntimeIdentity struct {
	ModuleID       string
	ServerID       string
	NodeID         string
	TemplateID     string
	Image          string
	State          string
	OnOwnedNetwork bool
	// RendererIP is the container's address on the owned network. The panel
	// dials this IP, not a Docker DNS name: in host-network and native-binary
	// modes the controller is not attached to the bridge and cannot resolve
	// container names.
	RendererIP string
	Privileged bool
	Mounts     []ModuleRuntimeMount
	// ContainerPorts are the template-declared ports exposed inside the
	// container. Discovery uses these, never a published host port.
	ContainerPorts []int
}

// InspectModuleRuntime resolves the exact module container and inspects it on
// its owning node. It never modifies the container or broadens privileges.
func (c *Client) InspectModuleRuntime(ctx context.Context, moduleID, recordedID, networkName string) (*ModuleRuntimeIdentity, error) {
	if c == nil || moduleID == "" {
		return nil, fmt.Errorf("inspect module runtime: empty module id")
	}
	summary, err := c.ResolveModuleContainerPreferring(ctx, moduleID, recordedID)
	if err != nil || summary == nil {
		return nil, fmt.Errorf("inspect module runtime: cannot resolve module %s: %w", moduleID, err)
	}
	inspectResult, err := c.docker.ContainerInspect(ctx, summary.ID, client.ContainerInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("inspect module runtime: %w", err)
	}
	cfg := inspectResult.Container.Config
	hostCfg := inspectResult.Container.HostConfig
	state := ""
	if inspectResult.Container.State != nil {
		state = string(inspectResult.Container.State.Status)
	}

	identity := &ModuleRuntimeIdentity{
		ModuleID:   labelValue(cfg.Labels, "carbon-panel.module.id"),
		ServerID:   labelValue(cfg.Labels, "carbon-panel.module.server_id"),
		NodeID:     labelValue(cfg.Labels, "carbon-panel.node.id"),
		TemplateID: labelValue(cfg.Labels, "carbon-panel.module.template_id"),
		Image:      cfg.Image,
		State:      strings.ToLower(state),
		Privileged: hostCfg != nil && hostCfg.Privileged,
	}
	if cfg.ExposedPorts != nil {
		for port := range cfg.ExposedPorts {
			num, convErr := strconv.Atoi(port.Port())
			if convErr == nil && num > 0 {
				identity.ContainerPorts = append(identity.ContainerPorts, num)
			}
		}
	}
	if networkName != "" && inspectResult.Container.NetworkSettings != nil {
		endpoint, ok := inspectResult.Container.NetworkSettings.Networks[networkName]
		identity.OnOwnedNetwork = ok
		if ok && endpoint != nil {
			identity.RendererIP = endpoint.IPAddress.String()
		}
	}
	for _, m := range inspectResult.Container.Mounts {
		identity.Mounts = append(identity.Mounts, ModuleRuntimeMount{
			Type: strings.ToLower(string(m.Type)), Source: m.Source,
			Target: m.Destination, ReadOnly: !m.RW,
		})
	}
	return identity, nil
}

func labelValue(labels map[string]string, key string) string {
	if labels == nil {
		return ""
	}
	return strings.TrimSpace(labels[key])
}

// ModuleWorldMount reports the effective bind for the container's /app/world
// target, checking both bind mounts and Docker-managed mounts. It is used to
// verify that the real mounted level matches the requested canonical world.
func (i *ModuleRuntimeIdentity) ModuleWorldMount() (ModuleRuntimeMount, bool) {
	if i == nil {
		return ModuleRuntimeMount{}, false
	}
	for _, m := range i.Mounts {
		if strings.TrimRight(m.Target, "/") == "/app/world" && m.Type == strings.ToLower(string(mount.TypeBind)) {
			return m, true
		}
	}
	return ModuleRuntimeMount{}, false
}
