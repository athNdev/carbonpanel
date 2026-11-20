<!-- SPDX-License-Identifier: MPL-2.0 -->
<script>
	import { Toaster } from 'svelte-sonner';
	import * as Tooltip from '$lib/components/ui/tooltip';
	import NewServerPage from '../../src/routes/servers/new/+page.svelte';
</script>

<Tooltip.Provider>
	<Toaster position="top-right" />
	<NewServerPage />
</Tooltip.Provider>
