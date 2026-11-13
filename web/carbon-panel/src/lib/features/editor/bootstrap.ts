// SPDX-License-Identifier: MIT AND MPL-2.0
// This file includes DiscoPanel material under the MIT License,
// Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
// and still applies to that inherited material.
// Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
// If a copy of the MPL was not distributed with this file, You can obtain
// one at https://mozilla.org/MPL/2.0/.
import * as monaco from 'monaco-editor';
import editorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker';
import jsonWorker from 'monaco-editor/esm/vs/language/json/json.worker?worker';
import cssWorker from 'monaco-editor/esm/vs/language/css/css.worker?worker';
import htmlWorker from 'monaco-editor/esm/vs/language/html/html.worker?worker';
import tsWorker from 'monaco-editor/esm/vs/language/typescript/ts.worker?worker';

// Configure the Monaco worker environment
interface MonacoGlobal {
	MonacoEnvironment?: unknown;
	__jsonTemplateLang?: boolean;
}
const monacoGlobal = self as unknown as MonacoGlobal;
if (!monacoGlobal.MonacoEnvironment) {
	monacoGlobal.MonacoEnvironment = {
		getWorker(_: unknown, label: string) {
			if (label === 'json') return new jsonWorker();
			if (label === 'css' || label === 'scss' || label === 'less') return new cssWorker();
			if (label === 'html' || label === 'handlebars' || label === 'razor') return new htmlWorker();
			if (label === 'typescript' || label === 'javascript') return new tsWorker();
			return new editorWorker();
		}
	};
}

// MONACO PSEUDO JSON-LIKE TEMPLATE AND LINTER, NEEDED FOR GO TEMPLATING
if (!monacoGlobal.__jsonTemplateLang) {
	monacoGlobal.__jsonTemplateLang = true;
	monaco.languages.register({ id: 'json-template' });
	monaco.languages.setLanguageConfiguration('json-template', {
		brackets: [
			['{', '}'],
			['[', ']']
		],
		autoClosingPairs: [
			{ open: '{', close: '}' },
			{ open: '[', close: ']' },
			{ open: '"', close: '"' }
		],
		surroundingPairs: [
			{ open: '{', close: '}' },
			{ open: '[', close: ']' },
			{ open: '"', close: '"' }
		]
	});
	monaco.languages.setMonarchTokensProvider('json-template', {
		defaultToken: '',
		tokenizer: {
			root: [
				[/\{\{/, { token: 'variable.template', next: '@template' }],
				[/"(?:[^"\\]|\\.)*"(?=\s*:)/, 'type'],
				[/"/, { token: 'string', next: '@string' }],
				[/-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/, 'number'],
				[/\b(?:true|false|null)\b/, 'keyword'],
				[/[{}[\]]/, 'delimiter.bracket'],
				[/[,:]/, 'delimiter']
			],
			template: [
				[/\}\}/, { token: 'variable.template', next: '@pop' }],
				[/[^}]+/, 'variable.template'],
				[/\}/, 'variable.template']
			],
			string: [
				[/\{\{/, { token: 'variable.template', next: '@templateInString' }],
				[/[^"\\{]+/, 'string'],
				[/\\./, 'string.escape'],
				[/"/, { token: 'string', next: '@pop' }],
				[/\{/, 'string']
			],
			templateInString: [
				[/\}\}/, { token: 'variable.template', next: '@pop' }],
				[/[^}]+/, 'variable.template'],
				[/\}/, 'variable.template']
			]
		}
	} as monaco.languages.IMonarchLanguage);
}

export { monaco };
