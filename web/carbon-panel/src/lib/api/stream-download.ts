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
		credentials: 'same-origin'
	});
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
