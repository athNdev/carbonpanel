// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package blueprint

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
)

// Protocol is the player-facing edition grammar a seed would boot.
type Protocol string

const (
	ProtocolJava    Protocol = "java"
	ProtocolBedrock Protocol = "bedrock"

	// DefaultJavaImageTag is the itzg/minecraft-server tag used when a
	// qualified seed does not pin a full image. It is a tag, not a live
	// registry lookup: create still resolves the daemon reference from it.
	DefaultJavaImageTag = "java21"

	// NativeBedrockLimitation is the supported-mode label for the builtin
	// Bedrock full-image seed. A seed row is not a native edition path.
	NativeBedrockLimitation = "Native Bedrock edition is not a qualified create path. This seed is a full-image reference only; Geyser remains a separate module."
)

// Spec is the create-path resolution of one catalog seed: image, env,
// protocol and whether that combination is a qualified Java/tag workflow.
type Spec struct {
	ID          string
	Qualified   bool
	Limitation  string
	ModLoader   storage.ModLoader
	MCVersion   string
	StoredImage string
	Image       string
	Env         map[string]string
	Protocol    Protocol
	GamePort    int
	MemoryMB    int
}

// Resolve turns a catalog seed into the image, env and protocol create would
// use. Qualification is about the create path, not the seed's display name:
// a Bedrock repository reference is unlabeled native edition support.
func Resolve(bp storage.ServerBlueprint) (Spec, error) {
	env := map[string]string{}
	if strings.TrimSpace(bp.DefaultEnv) != "" {
		if err := json.Unmarshal([]byte(bp.DefaultEnv), &env); err != nil {
			return Spec{}, fmt.Errorf("blueprint %s default env: %w", bp.ID, err)
		}
	}

	spec := Spec{
		ID:        bp.ID,
		ModLoader: bp.ModLoader,
		MCVersion: bp.MCVersion,
		Env:       env,
		MemoryMB:  parseMemoryMB(env["MEMORY"]),
	}

	image := strings.TrimSpace(bp.DockerImage)
	if image == "" {
		image = DefaultJavaImageTag
	}
	stored, err := docker.CanonicalServerImage(image)
	if err != nil {
		return Spec{}, fmt.Errorf("blueprint %s image: %w", bp.ID, err)
	}
	resolved, err := docker.ResolveServerImage(stored)
	if err != nil {
		return Spec{}, fmt.Errorf("blueprint %s resolved image: %w", bp.ID, err)
	}
	spec.StoredImage = stored
	spec.Image = resolved

	if isBedrockImage(resolved) {
		spec.Protocol = ProtocolBedrock
		spec.GamePort = 19132
		spec.Qualified = false
		spec.Limitation = NativeBedrockLimitation
		return spec, nil
	}

	spec.Protocol = ProtocolJava
	spec.GamePort = 25565
	spec.Qualified = true
	if spec.Env == nil {
		spec.Env = map[string]string{}
	}
	if spec.Env["TYPE"] == "" {
		spec.Env["TYPE"] = storage.NormalizeModLoaderForType(bp.ModLoader)
	} else {
		spec.Env["TYPE"] = storage.NormalizeModLoaderForType(storage.ModLoader(strings.ToLower(spec.Env["TYPE"])))
	}
	if spec.Env["EULA"] == "" {
		spec.Env["EULA"] = "TRUE"
	}
	return spec, nil
}

func isBedrockImage(resolved string) bool {
	return strings.HasPrefix(strings.ToLower(resolved), "itzg/minecraft-bedrock-server")
}

func parseMemoryMB(raw string) int {
	s := strings.TrimSpace(strings.ToUpper(raw))
	if s == "" {
		return 0
	}
	mult := 1
	switch {
	case strings.HasSuffix(s, "G"):
		mult = 1024
		s = strings.TrimSuffix(s, "G")
	case strings.HasSuffix(s, "M"):
		s = strings.TrimSuffix(s, "M")
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0
	}
	return n * mult
}
