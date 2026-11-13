<script>
  import FileEditor from '$lib/components/files/file-editor-dialog.svelte';
  import CodeEditor from '$lib/components/ui/code-editor.svelte';
  import * as monaco from 'monaco-editor';
  import { tick } from 'svelte';

  let file = $state(null);
  let serverId = $state('server-1');
  let open = $state(false);
  let closes = $state(0);
  let saved = $state([]);
  let codeOpen = $state(false);

  function select(server, path) {
    serverId = server;
    file = { path, name: path, isDir: false };
    open = true;
  }

  // The test controls selection and deferred RPCs; the dialog and Monaco are real.
  globalThis.__editorUi = {
    select,
    code: (show) => { codeOpen = show; },
    close: () => { open = false; },
    flush: async () => { await tick(); await tick(); },
    edit: (text) => monaco.editor.getModels()[0].setValue(text),
    snapshot: () => ({
      open, closes, saved: [...saved],
      model: monaco.editor.getModels()[0]?.getValue(),
      languages: monaco.editor.getModels().map((model) => model.getLanguageId()),
      modelCount: monaco.editor.getModels().length
    })
  };
</script>

<main>
  <h1>File editor browser regression</h1>
  <button onclick={() => select('server-1', 'A.txt')}>Open A</button>
  <button onclick={() => select('server-1', 'B.txt')}>Open B</button>
  <output>Closes {closes} Saves {saved.length}</output>
</main>
{#if codeOpen}<CodeEditor value={'{"key":"{{ .Value }}"}'} language="json-template" />{/if}
<FileEditor {serverId} {file} {open}
  onClose={() => { open = false; closes++; }}
  onSave={() => { saved = [...saved, `${serverId}/${file?.path}`]; }} />
