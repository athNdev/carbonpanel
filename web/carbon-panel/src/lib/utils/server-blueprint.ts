// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import { ModLoader } from '$lib/proto/carbonpanel/v1/common_pb';

export const nativeBedrockLimitation =
	'Native Bedrock edition is not a qualified create path. This seed is a full-image reference only; Geyser remains a separate module.';

export type CreateProtocol = 'java' | 'bedrock';

export type CreateBlueprintCapability = {
	id: string;
	qualified: boolean;
	limitation: string;
	protocol: CreateProtocol;
	image: string;
	storedImage: string;
	env: Record<string, string>;
	gamePort: number;
	memoryMb: number;
	modLoader: ModLoader;
};

const loaderByName: Record<string, ModLoader> = {
	vanilla: ModLoader.VANILLA,
	forge: ModLoader.FORGE,
	fabric: ModLoader.FABRIC,
	quilt: ModLoader.QUILT,
	paper: ModLoader.PAPER,
	neoforge: ModLoader.NEOFORGE,
	spigot: ModLoader.SPIGOT,
	bukkit: ModLoader.BUKKIT,
	purpur: ModLoader.PURPUR,
	folia: ModLoader.FOLIA
};

export function blueprintModLoader(name: string): ModLoader {
	return loaderByName[name.trim().toLowerCase()] ?? ModLoader.VANILLA;
}

function parseMemoryMb(raw: string | undefined): number {
	const s = (raw ?? '').trim().toUpperCase();
	if (!s) return 0;
	if (s.endsWith('G')) {
		const n = Number.parseInt(s.slice(0, -1), 10);
		return Number.isFinite(n) && n > 0 ? n * 1024 : 0;
	}
	if (s.endsWith('M')) {
		const n = Number.parseInt(s.slice(0, -1), 10);
		return Number.isFinite(n) && n > 0 ? n : 0;
	}
	return 0;
}

function parseEnv(raw: string): Record<string, string> {
	if (!raw.trim()) return {};
	try {
		const parsed = JSON.parse(raw) as unknown;
		if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
		const out: Record<string, string> = {};
		for (const [key, value] of Object.entries(parsed as Record<string, unknown>)) {
			if (typeof value === 'string') out[key] = value;
		}
		return out;
	} catch {
		return {};
	}
}

function isBedrockImage(image: string): boolean {
	const ref = image.trim().toLowerCase().replace(/^docker\.io\//, '');
	return ref.startsWith('itzg/minecraft-bedrock-server');
}

/**
 * Create-path qualification for a catalog seed. Uses the image repository,
 * not the seed display name: a Bedrock full-image row is not native edition
 * support.
 */
export function resolveCreateBlueprint(seed: {
	id: string;
	dockerImage: string;
	defaultEnv: string;
	modLoader: string;
}): CreateBlueprintCapability {
	const env = parseEnv(seed.defaultEnv);
	const dockerImage = seed.dockerImage.trim();
	if (isBedrockImage(dockerImage)) {
		return {
			id: seed.id,
			qualified: false,
			limitation: nativeBedrockLimitation,
			protocol: 'bedrock',
			image: 'itzg/minecraft-bedrock-server:latest',
			storedImage: dockerImage,
			env,
			gamePort: 19132,
			memoryMb: parseMemoryMb(env.MEMORY),
			modLoader: blueprintModLoader(seed.modLoader)
		};
	}
	const storedImage = dockerImage === '' ? 'java21' : dockerImage;
	const image = storedImage.includes('/') ? storedImage : `itzg/minecraft-server:${storedImage}`;
	if (!env.TYPE) {
		env.TYPE = seed.modLoader.trim().toUpperCase();
	}
	if (!env.EULA) env.EULA = 'TRUE';
	return {
		id: seed.id,
		qualified: true,
		limitation: '',
		protocol: 'java',
		image,
		storedImage,
		env,
		gamePort: 25565,
		memoryMb: parseMemoryMb(env.MEMORY),
		modLoader: blueprintModLoader(seed.modLoader)
	};
}
