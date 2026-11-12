// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

export interface FileIdentity {
	readonly serverId: string;
	readonly path: string;
}

export interface FileEditorIdentity extends FileIdentity {
	readonly generation: number;
}

export interface FileWrite extends FileIdentity {
	readonly content: string;
}

export interface FileEditorState {
	readonly identity: FileEditorIdentity | null;
	readonly content: string;
	readonly originalContent: string;
	readonly loading: boolean;
	readonly loaded: boolean;
	readonly saving: boolean;
	readonly dirty: boolean;
}

interface FileEditorActions {
	readFile(identity: FileIdentity): Promise<string>;
	writeFile(file: FileWrite): Promise<unknown>;
	onLoadError?(): void;
	onSaveError?(): void;
	onSaved?(): void;
}

function closedState(): FileEditorState {
	return Object.freeze({
		identity: null,
		content: '',
		originalContent: '',
		loading: false,
		loaded: false,
		saving: false,
		dirty: false
	});
}

/** Owns content identity across asynchronous loads, saves and dialog reuse. */
export class FileEditorSession {
	private generation = 0;
	private current = closedState();

	constructor(
		private readonly actions: FileEditorActions,
		private readonly onChange: (state: FileEditorState) => void = () => {}
	) {}

	get state(): FileEditorState {
		return this.current;
	}

	isFor(identity: FileIdentity): boolean {
		return (
			this.current.identity?.serverId === identity.serverId &&
			this.current.identity?.path === identity.path
		);
	}

	isCurrent(identity: FileEditorIdentity | null): boolean {
		return (
			identity !== null &&
			identity.generation === this.current.identity?.generation &&
			this.isFor(identity)
		);
	}

	async open(file: FileIdentity): Promise<void> {
		if (this.isFor(file)) return;
		const identity = Object.freeze({ ...file, generation: ++this.generation });
		this.publish({ ...closedState(), identity, loading: true });
		try {
			const content = await this.actions.readFile({
				serverId: identity.serverId,
				path: identity.path
			});
			if (!this.isCurrent(identity)) return;
			this.publish({ ...this.current, content, originalContent: content, loaded: true });
		} catch {
			if (this.isCurrent(identity)) this.actions.onLoadError?.();
		} finally {
			if (this.isCurrent(identity)) this.publish({ ...this.current, loading: false });
		}
	}

	close(): void {
		++this.generation;
		this.publish(closedState());
	}

	setContent(content: string): void {
		if (!this.current.loaded || this.current.loading) return;
		this.publish({ ...this.current, content });
	}

	async save(): Promise<void> {
		const { identity, content, loaded, loading, saving, dirty } = this.current;
		if (!identity || !loaded || loading || saving || !dirty) return;
		this.publish({ ...this.current, saving: true });
		try {
			await this.actions.writeFile({ serverId: identity.serverId, path: identity.path, content });
			if (!this.isCurrent(identity)) return;
			// The editor may have changed while saving. Only this revision is persisted.
			this.publish({ ...this.current, originalContent: content });
			this.actions.onSaved?.();
		} catch {
			if (this.isCurrent(identity)) this.actions.onSaveError?.();
		} finally {
			if (this.isCurrent(identity)) this.publish({ ...this.current, saving: false });
		}
	}

	private publish(state: FileEditorState): void {
		this.current = Object.freeze({ ...state, dirty: state.content !== state.originalContent });
		this.onChange(this.current);
	}
}
