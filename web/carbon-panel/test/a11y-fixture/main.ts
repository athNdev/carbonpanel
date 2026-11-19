// SPDX-License-Identifier: MPL-2.0
import '../../src/app.css';
import { mount } from 'svelte';
import Fixture from './Fixture.svelte';

const target = document.getElementById('app');
if (!target) throw new Error('Fixture mount point missing');
mount(Fixture, { target });
