// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"fmt"

	"github.com/athNdev/carbon-panel/internal/blueprint"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// applyResolvedBlueprint copies a qualified catalog seed onto a create
// request: image, loader, protocol port and memory. An unqualified seed
// (native Bedrock edition is not a create path) is refused here so success
// cannot be inferred from the seed name.
func applyResolvedBlueprint(msg *v1.CreateServerRequest, spec blueprint.Spec) error {
	if msg == nil {
		return fmt.Errorf("create request is required")
	}
	if !spec.Qualified {
		if spec.Limitation != "" {
			return fmt.Errorf("%s", spec.Limitation)
		}
		return fmt.Errorf("blueprint %s is not a qualified create path", spec.ID)
	}
	msg.ModLoader = dbModLoaderToProto(spec.ModLoader)
	if msg.DockerImage == "" {
		msg.DockerImage = spec.StoredImage
	}
	if msg.Memory <= 0 && spec.MemoryMB > 0 {
		msg.Memory = int32(spec.MemoryMB)
	}
	if msg.Port <= 0 && spec.GamePort > 0 {
		msg.Port = int32(spec.GamePort)
	}
	if spec.Protocol != blueprint.ProtocolJava {
		return fmt.Errorf("blueprint %s protocol %s is not a qualified create path", spec.ID, spec.Protocol)
	}
	return nil
}
