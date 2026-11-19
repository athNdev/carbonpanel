// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"net/http"
	"strings"

	carbonpanelv1connect "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
)

// RegisteredConnectServices is the authoritative set of carbonpanel.v1
// Connect services whose handlers are mounted by Server.registerServices.
//
// It is a single source of truth shared by:
//
//   - the gRPC reflection service (setupHandler), and
//   - the RBAC fail-closed enumeration gate in internal/rbac/mapping_test.go.
//
// Both consumers enumerate every RPC declared on these services through the
// protobuf descriptors. A service that is mounted but absent from this list
// silently escapes the RBAC gate: new RPCs added to it would receive no CI
// signal that they still need a permission mapping (OSS-403). Registering a
// handler in registerServices and adding its name here must therefore be one
// change. TestRegisteredConnectServicesMatchesMountedHandlers fails fast when
// the list and the handlers actually mounted drift apart.
var RegisteredConnectServices = []string{
	carbonpanelv1connect.AuthServiceName,
	carbonpanelv1connect.ConfigServiceName,
	carbonpanelv1connect.FileServiceName,
	carbonpanelv1connect.MinecraftServiceName,
	carbonpanelv1connect.ModServiceName,
	carbonpanelv1connect.ModBrowseServiceName,
	carbonpanelv1connect.StagedConfigServiceName,
	carbonpanelv1connect.PackwizServiceName,
	carbonpanelv1connect.ModpackServiceName,
	carbonpanelv1connect.DatapackServiceName,
	carbonpanelv1connect.ProxyServiceName,
	carbonpanelv1connect.ServerServiceName,
	carbonpanelv1connect.NodeServiceName,
	carbonpanelv1connect.SupportServiceName,
	carbonpanelv1connect.TaskServiceName,
	carbonpanelv1connect.ActivityServiceName,
	carbonpanelv1connect.OperationServiceName,
	carbonpanelv1connect.SubuserServiceName,
	carbonpanelv1connect.BlueprintServiceName,
	carbonpanelv1connect.BackupServiceName,
	carbonpanelv1connect.UserServiceName,
	carbonpanelv1connect.RoleServiceName,
	carbonpanelv1connect.ModuleServiceName,
	carbonpanelv1connect.UploadServiceName,
	carbonpanelv1connect.PlayerServiceName,
	carbonpanelv1connect.WorldViewServiceName,
	carbonpanelv1connect.WorldServiceName,
}

// connectHandlerMux is the subset of http.ServeMux that registerServices uses.
// Keeping it narrow lets a test record every mounted path and assert the
// mounted services match RegisteredConnectServices (OSS-403).
type connectHandlerMux interface {
	Handle(pattern string, handler http.Handler)
}

// connectServiceNameFromPath extracts a carbonpanel.v1 service name from a
// Connect handler mount path of the form "/carbonpanel.v1.<Service>/". Paths
// belonging to the legacy JSON API or the frontend return ok=false.
func connectServiceNameFromPath(path string) (string, bool) {
	const prefix = "/carbonpanel.v1."
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/") {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/"), true
}
