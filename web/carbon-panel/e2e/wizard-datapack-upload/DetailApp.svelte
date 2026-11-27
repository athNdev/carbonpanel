<!-- SPDX-License-Identifier: MPL-2.0 -->
<script>
	import { Toaster } from 'svelte-sonner';
	import * as Tooltip from '$lib/components/ui/tooltip';
	import ServerDetailPage from '../../src/routes/servers/[id]/+page.svelte';
</script>

<Tooltip.Provider>
	<Toaster position="top-right" />
	<ServerDetailPage />
</Tooltip.Provider>
