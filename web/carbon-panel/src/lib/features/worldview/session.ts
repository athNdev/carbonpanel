// SPDX-License-Identifier: MPL-2.0
/**
 * The map iframe is same-origin only. The backend supplies an opaque proxy
 * path prefix; no renderer origin, host:port, scheme or token is ever accepted
 * from the response and placed in an iframe src.
 */

/** True only for a same-origin absolute path that cannot escape the origin. */
export function isSameOriginProxyPath(value: string | null | undefined): boolean {
	if (!value || typeof value !== 'string') return false;
	if (!value.startsWith('/')) return false;
	// Protocol-relative URLs ('//host') and absolute URLs both leave the origin.
	if (value.startsWith('//')) return false;
	if (value.includes('://') || value.includes('\\')) return false;
	// Control characters (including CR/LF) must never reach an iframe src.
	// eslint-disable-next-line no-control-regex
	if (/[\u0000-\u001f\u007f]/.test(value)) return false;
	return true;
}

/** The iframe src, or null when the session cannot safely be mounted. */
export function resolveEmbedSrc(proxyPath: string | null | undefined): string | null {
	return isSameOriginProxyPath(proxyPath) ? (proxyPath as string) : null;
}

export interface SessionVerdict {
	src: string | null;
	reason: string;
}

/**
 * Combine renderer state and the session response into one embed decision.
 * A non-empty reason is shown to the user instead of a frame.
 */
export function evaluateSession(
	stateAllowsEmbed: boolean,
	session: { proxyPath: string; state: number; reason: string } | undefined
): SessionVerdict {
	if (!session) return { src: null, reason: 'Map session is not available yet.' };
	if (!stateAllowsEmbed) {
		return { src: null, reason: session.reason || 'The map is not ready to display.' };
	}
	const src = resolveEmbedSrc(session.proxyPath);
	if (!src) {
		return {
			src: null,
			reason: 'The renderer returned no safe same-origin map path.'
		};
	}
	return { src, reason: '' };
}
