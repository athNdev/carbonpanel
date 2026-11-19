// SPDX-License-Identifier: MIT AND MPL-2.0
// This file includes DiscoPanel material under the MIT License,
// Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
// and still applies to that inherited material.
// Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
// If a copy of the MPL was not distributed with this file, You can obtain
// one at https://mozilla.org/MPL/2.0/.
import type {
	ModulePort,
	ModuleDependency,
	ModuleEventHook
} from '../../proto/carbonpanel/v1/module_pb';

// Blank rows are dropped, keys/paths trimmed, values preserved, duplicate keys
// use the last row. Persisted malformed JSON retains the existing empty-row
// fallback; the *Detailed decoders report it explicitly so a caller can say so.
// That is lossy tolerant decoding, not validation or rejection.
export interface EnvVar {
	key: string;
	value: string;
}
export interface VolumeMount {
	hostPath: string;
	containerPath: string;
	readOnly: boolean;
	createDir: boolean;
}
export interface MetadataEntry {
	key: string;
	value: string;
}

export function envVarsToJson(envVars: EnvVar[]): string {
	const obj: Record<string, string> = {};
	for (const env of envVars) {
		if (env.key.trim()) obj[env.key.trim()] = env.value;
	}
	return JSON.stringify(obj);
}

export function volumesToJson(volumes: VolumeMount[]): string {
	return JSON.stringify(
		volumes
			.filter((v) => v.hostPath.trim() && v.containerPath.trim())
			.map((v) => ({
				source: v.hostPath.trim(),
				target: v.containerPath.trim(),
				read_only: v.readOnly,
				create_dir: v.createDir
			}))
	);
}

/** Rows plus an explicit flag for persisted JSON that could not be parsed. */
export interface RowParseResult<T> {
	rows: T[];
	malformed: boolean;
}

/**
 * Decode persisted environment JSON. A blank field is treated as `{}`; a
 * non-blank parse failure yields the historical empty rows AND reports
 * `malformed: true`. It is still not validation.
 */
export function parseEnvVarsDetailed(json: string): RowParseResult<EnvVar> {
	const raw = json || '{}';
	try {
		return {
			rows: Object.entries(JSON.parse(raw)).map(([key, value]) => ({
				key,
				value: String(value)
			})),
			malformed: false
		};
	} catch {
		return { rows: [], malformed: true };
	}
}

/** Historical wrapper: returns only the rows, unchanged from before. */
export function parseEnvVars(json: string): EnvVar[] {
	return parseEnvVarsDetailed(json).rows;
}

/**
 * Decode persisted volume JSON into hostPath/containerPath rows. The
 * source/target and snake-case booleans are preserved; a non-blank parse
 * failure yields empty rows AND reports `malformed: true`.
 */
export function parseVolumesDetailed(json: string): RowParseResult<VolumeMount> {
	const raw = json || '[]';
	try {
		return {
			rows: JSON.parse(raw).map((v: Record<string, unknown>) => ({
				hostPath: v.source || '',
				containerPath: v.target || '',
				readOnly: v.read_only || false,
				createDir: v.create_dir || false
			})),
			malformed: false
		};
	} catch {
		return { rows: [], malformed: true };
	}
}

/** Historical wrapper: returns only the rows, unchanged from before. */
export function parseVolumes(json: string): VolumeMount[] {
	return parseVolumesDetailed(json).rows;
}

export function parsePorts(p: ModulePort[] | undefined): ModulePort[] {
	return (
		p?.map(
			(x) =>
				({
					name: x.name,
					containerPort: x.containerPort,
					hostPort: x.hostPort,
					protocol: x.protocol || 'tcp',
					proxyEnabled: x.proxyEnabled
				}) as ModulePort
		) || []
	);
}

export function parseDependencies(d: ModuleDependency[] | undefined): ModuleDependency[] {
	return (
		d?.map(
			(x) =>
				({
					moduleId: x.moduleId,
					waitForHealthy: x.waitForHealthy,
					timeoutSeconds: x.timeoutSeconds
				}) as ModuleDependency
		) || []
	);
}

export function parseEventHooks(h: ModuleEventHook[] | undefined): ModuleEventHook[] {
	return (
		h?.map(
			(x) =>
				({
					event: x.event,
					action: x.action,
					command: x.command,
					delaySeconds: x.delaySeconds,
					condition: x.condition
				}) as ModuleEventHook
		) || []
	);
}

export function parseMetadata(m: { [key: string]: string } | undefined): MetadataEntry[] {
	return m ? Object.entries(m).map(([key, value]) => ({ key, value })) : [];
}

export function metadataToMap(metadata: MetadataEntry[]): { [key: string]: string } {
	const map: { [key: string]: string } = {};
	for (const e of metadata) {
		if (e.key.trim()) map[e.key.trim()] = e.value;
	}
	return map;
}

// Zero-valued persisted health settings retain the existing instance defaults.
export function instanceHealthDefaults(value: {
	healthCheckInterval: number;
	healthCheckTimeout: number;
	healthCheckRetries: number;
}) {
	return {
		healthCheckInterval: value.healthCheckInterval || 30,
		healthCheckTimeout: value.healthCheckTimeout || 5,
		healthCheckRetries: value.healthCheckRetries || 3
	};
}
