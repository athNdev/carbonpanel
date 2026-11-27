import { authStore } from '$lib/stores/auth';

export function streamDownloadUrl(sessionId: string) {
	return `/api/v1/download/${sessionId}`;
}

// Confirm the scoped download would be accepted before the UI claims success.
// The archive itself is still fetched later as an ordinary browser download, so
// this HEAD is the only point where a refusal (401/403/404/5xx) can be reported
// without reading or buffering any archive bytes.
export async function verifyStreamDownload(sessionId: string): Promise<number> {
	authStore.refreshBrowserSessionCookie();
	const response = await fetch(streamDownloadUrl(sessionId), {
		method: 'HEAD',
		credentials: 'same-origin',
		// A fronting proxy or SSO layer can answer a same-origin request with a
		// 302 to an HTML login page. Following it would return 200 and let the UI
		// claim a download that never happens, so a redirect is a transport
		// refusal: the check could not be completed.
		redirect: 'error'
	});
	// Defence in depth for a transport that still resolves a redirected
	// response: a redirect never proves the scoped archive is downloadable.
	if (response.redirected) {
		throw new Error('Archive download preflight was redirected.');
	}
	// A non-2xx status is returned so the caller reports the exact HTTP status;
	// only a non-redirected 2xx may be treated as success.
	return response.status;
}

export function triggerStreamDownload(sessionId: string, filename: string) {
	authStore.refreshBrowserSessionCookie();
	const url = streamDownloadUrl(sessionId);
	const anchor = document.createElement('a');
	anchor.href = url;
	anchor.download = filename;
	anchor.click();
}
