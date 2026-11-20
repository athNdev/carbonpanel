// SPDX-License-Identifier: MPL-2.0
import { mount } from 'svelte';
import App from './App.svelte';
import '$lib/../app.css';

mount(App, { target: document.getElementById('app') });
