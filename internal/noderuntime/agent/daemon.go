// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, you can obtain one at https://mozilla.org/MPL/2.0/.

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/noderuntime/native"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/moby/moby/client"
	"google.golang.org/protobuf/proto"
)

const daemonProbeTimeout = 4 * time.Second

type daemonObservation struct {
	id      string
	version string
	cgroup  bool
}

func observeDaemon(ctx context.Context, facts native.HostFacts, spec native.DaemonSpec) (daemonObservation, error) {
	if !filepath.IsAbs(spec.SocketPath) || !filepath.IsAbs(facts.XDG_RUNTIME_DIR) || !filepath.IsAbs(spec.DataRoot) {
		return daemonObservation{}, errors.New("private Docker socket and roots must use absolute paths")
	}
	runtimeDir, err := filepath.EvalSymlinks(facts.XDG_RUNTIME_DIR)
	if err != nil {
		return daemonObservation{}, err
	}
	socket, err := filepath.EvalSymlinks(spec.SocketPath)
	if err != nil {
		return daemonObservation{}, err
	}
	rel, err := filepath.Rel(runtimeDir, socket)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
		socket == "/run/docker.sock" || socket == "/var/run/docker.sock" {
		return daemonObservation{}, errors.New("Docker socket is outside the private runtime directory")
	}
	if facts.HostSocketPath != "" {
		hostSocket, err := filepath.EvalSymlinks(facts.HostSocketPath)
		if err == nil && hostSocket == socket {
			return daemonObservation{}, errors.New("Docker socket resolves to the host engine")
		}
	}
	file, err := os.Stat(socket)
	if err != nil {
		return daemonObservation{}, err
	}
	if file.Mode()&os.ModeSocket == 0 {
		return daemonObservation{}, errors.New("private Docker endpoint is not a Unix socket")
	}
	dataRoot, err := filepath.EvalSymlinks(spec.DataRoot)
	if err != nil {
		return daemonObservation{}, err
	}
	if dataRoot == "/var/lib/docker" || strings.HasPrefix(dataRoot, "/var/lib/docker/") {
		return daemonObservation{}, errors.New("Docker data root resolves under the shared engine")
	}

	probe, cancel := context.WithTimeout(ctx, daemonProbeTimeout)
	defer cancel()
	api, err := docker.NewAPIClientContext(probe, client.WithHost("unix://"+socket))
	if err != nil {
		return daemonObservation{}, err
	}
	defer api.Close()
	response, err := api.Info(probe, client.InfoOptions{})
	if err != nil {
		return daemonObservation{}, err
	}
	info := response.Info
	daemonRoot, err := filepath.EvalSymlinks(info.DockerRootDir)
	if err != nil || !filepath.IsAbs(info.DockerRootDir) || daemonRoot != dataRoot || strings.TrimSpace(info.ID) == "" {
		return daemonObservation{}, errors.New("Docker daemon identity or private data root is unverifiable")
	}
	if slices.Contains(info.SecurityOptions, "name=rootless") != (spec.Mode == native.ModeRootless) {
		return daemonObservation{}, errors.New("Docker daemon running mode disagrees with the approved plan")
	}
	return daemonObservation{
		id: info.ID, version: info.ServerVersion,
		cgroup: info.CgroupVersion == "2" && info.MemoryLimit && info.PidsLimit,
	}, nil
}

func activeCapabilities(plan *v1.NodeCapabilities, observation daemonObservation) *v1.NodeCapabilities {
	caps := proto.CloneOf(plan)
	caps.EngineVersion = observation.version
	caps.Capabilities = nil
	if observation.id == "" {
		return caps
	}
	for _, capability := range plan.GetCapabilities() {
		switch capability {
		case v1.NodeCapability_NODE_CAPABILITY_ROOTLESS,
			v1.NodeCapability_NODE_CAPABILITY_PRIVATE_SOCKET,
			v1.NodeCapability_NODE_CAPABILITY_DEDICATED_ROOTS,
			v1.NodeCapability_NODE_CAPABILITY_CONTAINERD_NAMESPACE:
			caps.Capabilities = append(caps.Capabilities, capability)
		case v1.NodeCapability_NODE_CAPABILITY_CGROUP_V2:
			if observation.cgroup {
				caps.Capabilities = append(caps.Capabilities, capability)
			}
		}
	}
	return caps
}
