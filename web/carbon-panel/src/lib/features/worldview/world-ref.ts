// SPDX-License-Identifier: MPL-2.0
/**
 * The URL carries an opaque world key rather than splitting a folder name into
 * an identity. A WorldRef is tagged (library binding, server-local level or
 * static snapshot), so the key round-trips all four discriminating fields.
 * The key is an internal encoding for deep links, not an authority: the RPCs
 * still receive a real WorldRef and the backend re-authorizes every request.
 */

import { create } from '@bufbuild/protobuf';
import {
	WorldRefSchema,
	WorldRef_Kind,
	type WorldRef
} from '$lib/proto/carbonpanel/v1/worldview_pb';

const PREFIX = 'w1.';

type Packed = [number, string, string, string, string];

function toBase64Url(bytes: Uint8Array): string {
	let binary = '';
	for (const byte of bytes) binary += String.fromCharCode(byte);
	return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '');
}

function fromBase64Url(value: string): Uint8Array {
	const base64 = value.replace(/-/g, '+').replace(/_/g, '/');
	const padded = base64 + '='.repeat((4 - (base64.length % 4)) % 4);
	const binary = atob(padded);
	const bytes = new Uint8Array(binary.length);
	for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
	return bytes;
}

/** Encode a WorldRef as an opaque, URL-safe key used by the server World tab. */
export function encodeWorldKey(ref: WorldRef | undefined): string {
	if (!ref) return '';
	const packed: Packed = [
		ref.kind,
		ref.serverId ?? '',
		ref.levelName ?? '',
		ref.bindingId ?? '',
		ref.snapshotId ?? ''
	];
	const json = JSON.stringify(packed);
	return PREFIX + toBase64Url(new TextEncoder().encode(json));
}

/**
 * Decode an opaque key back into a WorldRef. Returns null for malformed or
 * unsupported input so a bad deep link can degrade to an honest error state
 * instead of producing a plausible-looking but wrong world.
 */
export function decodeWorldKey(key: string | undefined): WorldRef | null {
	if (!key || typeof key !== 'string' || !key.startsWith(PREFIX)) return null;
	try {
		const json = new TextDecoder().decode(fromBase64Url(key.slice(PREFIX.length)));
		const packed: unknown = JSON.parse(json);
		if (!Array.isArray(packed) || packed.length !== 5) return null;
		if (!packed.every((part) => typeof part === 'string' || typeof part === 'number')) return null;
		const [kind, serverId, levelName, bindingId, snapshotId] = packed as Packed;
		if (!Number.isInteger(kind) || kind < 0 || kind > 3) return null;
		if ([serverId, levelName, bindingId, snapshotId].some((part) => typeof part !== 'string'))
			return null;
		return create(WorldRefSchema, {
			kind: kind as WorldRef_Kind,
			serverId,
			levelName,
			bindingId,
			snapshotId
		});
	} catch {
		return null;
	}
}

/** A stable identity string for grouping targets that point at the same world. */
export function worldIdentity(ref: WorldRef | undefined): string {
	if (!ref) return '';
	switch (ref.kind) {
		case WorldRef_Kind.LIBRARY_BINDING:
			return `library:${ref.bindingId}`;
		case WorldRef_Kind.STATIC_IMPORT:
			return `snapshot:${ref.snapshotId}`;
		case WorldRef_Kind.SERVER_LOCAL:
			return `server:${ref.serverId}:${ref.levelName}`;
		default:
			return `unknown:${ref.serverId}:${ref.levelName}:${ref.bindingId}:${ref.snapshotId}`;
	}
}
