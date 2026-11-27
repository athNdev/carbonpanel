<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { Archive } from '@lucide/svelte';
	import { onDestroy } from 'svelte';
	import { toast } from 'svelte-sonner';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { triggerStreamDownload, verifyStreamDownload } from '$lib/api/stream-download';
	import { CarbonButton, CarbonModal } from '$lib/components/carbon';
	import { ServerStatus, type Server } from '$lib/proto/carbonpanel/v1/common_pb';
	import {
		ServerArchiveConsistency,
		ServerArchiveFormat
	} from '$lib/proto/carbonpanel/v1/server_pb';
	import { formatFileSize, parseFileSize } from '$lib/utils/format-file-size';
	import ArchiveExportPanel from './ArchiveExportPanel.svelte';
	import {
		ARCHIVE_CREDENTIAL_DISCLOSURE,
		ARCHIVE_CREDENTIAL_DISCLOSURE_DETAIL,
		ARCHIVE_FORMATS,
		ARCHIVE_RPC_OPTIONS,
		STOPPED_CONSISTENCY,
		archiveConsistencyLabel,
		isSelectableArchiveFormat
	} from './archive-presentation';

	interface Props {
		server: Server;
		disabled?: boolean;
	}

	let { server, disabled = false }: Props = $props();

	type Phase = 'idle' | 'preparing' | 'ready' | 'error';

	let open = $state(false);
	let format = $state<number>(ServerArchiveFormat.ZIP);
	let phase = $state<Phase>('idle');
	let sessionId = $state('');
	let filename = $state('');
	let totalSize = $state(0);
	let error = $state<string | null>(null);
	let acknowledged = $state(false);
	let downloading = $state(false);

	// Export requires a stopped server. A live server is refused here and again
	// on the controller; this control never issues a stop.
	const running = $derived(server.status !== ServerStatus.STOPPED);

	// A preparation result is only allowed to touch the modal while it still
	// belongs to the current open attempt for the same server. Closing,
	// reopening for another format, switching servers and unmounting all bump
	// this generation, so a late response cannot overwrite the active session
	// or surface a stale error.
	let attempt = 0;

	function invalidate() {
		attempt += 1;
	}

	function reset() {
		invalidate();
		phase = 'idle';
		sessionId = '';
		filename = '';
		totalSize = 0;
		error = null;
		acknowledged = false;
		downloading = false;
	}

	function show() {
		reset();
		open = true;
	}

	let previousServerKey = `${server.id}:${server.status}`;
	$effect(() => {
		const key = `${server.id}:${server.status}`;
		if (key === previousServerKey) return;
		previousServerKey = key;
		reset();
	});

	onDestroy(invalidate);

	async function prepare() {
		if (running) {
			toast.error('Stop the server before exporting. It will not be stopped automatically.');
			return;
		}
		if (!isSelectableArchiveFormat(format as ServerArchiveFormat)) {
			toast.error('Select ZIP or TAR.GZ as the archive format.');
			return;
		}
		const generation = ++attempt;
		const requestedServerId = server.id;
		phase = 'preparing';
		error = null;
		acknowledged = false;
		try {
			const result = await rpcClient.server.exportServerArchive(
				{
					serverId: requestedServerId,
					format: format as ServerArchiveFormat,
					consistency: STOPPED_CONSISTENCY
				},
				{ ...ARCHIVE_RPC_OPTIONS, ...silentCallOptions }
			);
			if (generation !== attempt || !open || server.id !== requestedServerId) return;
			sessionId = result.sessionId;
			filename = result.filename;
			totalSize = parseFileSize(result.totalSize);
			phase = 'ready';
		} catch (cause) {
			if (generation !== attempt || !open || server.id !== requestedServerId) return;
			error = cause instanceof Error ? cause.message : 'Archive preparation failed.';
			phase = 'error';
			// The request is silent so the shared interceptor cannot toast for a
			// stale attempt. Only the current, fenced attempt reports its failure.
			toast.error(error);
		}
	}

	async function download() {
		if (!acknowledged || !sessionId || !filename || phase !== 'ready') return;
		if (downloading) return;
		const generation = attempt;
		const requestedServerId = server.id;
		downloading = true;
		let status: number;
		try {
			status = await verifyStreamDownload(sessionId);
		} catch {
			// A transport failure cannot prove the download is authorised, so it is
			// reported as a refusal rather than a started transfer.
			if (generation !== attempt || !open || server.id !== requestedServerId) return;
			downloading = false;
			toast.error('Archive download was refused: the download check could not be completed.');
			return;
		}
		if (generation !== attempt || !open || server.id !== requestedServerId) return;
		if (status < 200 || status >= 300) {
			downloading = false;
			toast.error(`Archive download was refused (HTTP ${status}).`);
			return;
		}
		downloading = false;
		invalidate();
		triggerStreamDownload(sessionId, filename);
		toast.success('Archive download started.');
		open = false;
	}
</script>

<CarbonButton
	kind="tertiary"
	size="md"
	{disabled}
	onclick={show}
	aria-label="Export server archive"
	data-testid="server-export-archive"
>
	<Archive class="h-4 w-4" aria-hidden="true" />
	Export archive
</CarbonButton>

<CarbonModal
	bind:open
	title="Export server archive"
	description="Portable ZIP or tar.gz with stopped-server consistency"
	size="lg"
	hasFooter={false}
	onclose={reset}
>
	<ArchiveExportPanel
		serverName={server.name}
		{running}
		formats={ARCHIVE_FORMATS}
		{format}
		onformat={(value) => (format = value)}
		{phase}
		{filename}
		totalSizeLabel={formatFileSize(totalSize)}
		{error}
		disclosure={ARCHIVE_CREDENTIAL_DISCLOSURE}
		disclosureDetail={ARCHIVE_CREDENTIAL_DISCLOSURE_DETAIL}
		consistencyText={archiveConsistencyLabel(ServerArchiveConsistency.STOPPED)}
		{acknowledged}
		{downloading}
		onacknowledge={(value) => (acknowledged = value)}
		onprepare={prepare}
		ondownload={download}
	/>
</CarbonModal>
