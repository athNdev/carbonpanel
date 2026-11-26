import { authStore } from '$lib/stores/auth';

export function triggerStreamDownload(sessionId: string, filename: string) {
	authStore.refreshBrowserSessionCookie();
	const url = `/api/v1/download/${sessionId}`;
	const anchor = document.createElement('a');
	anchor.href = url;
	anchor.download = filename;
	anchor.click();
}
