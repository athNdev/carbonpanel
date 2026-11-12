import { mount } from 'svelte';
import * as monaco from 'monaco-editor';
import App from './App.svelte';
import '$lib/../app.css';

// Capture registered callbacks without replacing Monaco. Invoke them after
// disposal to simulate queued events from the previous editor instance.
globalThis.__editorCallbacks = [];
const createEditor = monaco.editor.create;
monaco.editor.create = (...args) => {
  const instance = createEditor(...args);
  const callbacks = {};
  globalThis.__editorCallbacks.push(callbacks);
  const addCommand = instance.addCommand.bind(instance);
  instance.addCommand = (binding, callback, context) => {
    callbacks.save = callback;
    return addCommand(binding, callback, context);
  };
  const onChange = instance.onDidChangeModelContent.bind(instance);
  instance.onDidChangeModelContent = (callback, ...rest) => {
    callbacks.change = callback;
    return onChange(callback, ...rest);
  };
  return instance;
};
mount(App, { target: document.getElementById('app') });
