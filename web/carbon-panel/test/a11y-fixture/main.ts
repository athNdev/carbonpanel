// SPDX-License-Identifier: MPL-2.0
import './fixture.css';
import { mount } from 'svelte';
import Fixture from './Fixture.svelte';
import NativeMapFixture from './NativeMapFixture.svelte';

const target = document.getElementById('app');
if (!target) throw new Error('Fixture mount point missing');
const native = new URLSearchParams(location.search).has('native-map');
mount(native ? NativeMapFixture : Fixture, { target });
