// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

/**
 * Form seam for the node editor's TLS credentials.
 *
 * Node read responses are redacted: `tls_ca_cert`, `tls_cert` and `tls_key`
 * serialize empty even when the server stores real material. An empty value is
 * therefore "not disclosed", never "no credential stored". The edit form must
 * not echo those values back into `UpdateNode`, because a non-empty redaction
 * placeholder would replace real credentials and bump the endpoint generation.
 * See docs/adr/0018-node-response-redaction.md (Track C server side).
 */

export type TlsSecretFields = {
	tlsCaCert: string;
	tlsCert: string;
	tlsKey: string;
};

/** `unchanged` keeps stored credentials; `replace` submits all three fields. */
export type TlsCredentialIntent = 'unchanged' | 'replace';

export type TlsFormValues = TlsSecretFields & {
	tlsEnabled: boolean;
	tlsSkipVerify: boolean;
};

export type TlsUpdateFields = {
	tlsEnabled: boolean;
	tlsSkipVerify: boolean;
	tlsCaCert?: string;
	tlsCert?: string;
	tlsKey?: string;
};

export type TlsReplacementErrors = {
	tlsCaCert: string;
	tlsCert: string;
	tlsKey: string;
};

/**
 * Build the TLS subset of `UpdateNodeRequest`.
 *
 * In `unchanged` mode the secret fields are omitted entirely (not sent as empty
 * strings), so the server keeps the stored bytes and the endpoint generation is
 * untouched. Only an explicit `replace` sends real values, and then all three
 * fields are sent together so a partial replacement cannot leave a stale half.
 */
export function buildTlsUpdate(
	values: TlsFormValues,
	intent: TlsCredentialIntent
): TlsUpdateFields {
	const fields: TlsUpdateFields = {
		tlsEnabled: values.tlsEnabled,
		tlsSkipVerify: values.tlsSkipVerify
	};
	if (intent === 'replace') {
		fields.tlsCaCert = values.tlsCaCert.trim();
		fields.tlsCert = values.tlsCert.trim();
		fields.tlsKey = values.tlsKey.trim();
	}
	return fields;
}

/** Per-field validation shown only when the operator opts into replacement. */
export function validateTlsReplacement(
	intent: TlsCredentialIntent,
	secrets: TlsSecretFields
): TlsReplacementErrors {
	if (intent !== 'replace') {
		return { tlsCaCert: '', tlsCert: '', tlsKey: '' };
	}
	return {
		tlsCaCert: secrets.tlsCaCert.trim() ? '' : 'CA certificate is required to replace credentials',
		tlsCert: secrets.tlsCert.trim() ? '' : 'Client certificate is required to replace credentials',
		tlsKey: secrets.tlsKey.trim() ? '' : 'Client private key is required to replace credentials'
	};
}

export function hasTlsReplacementErrors(errors: TlsReplacementErrors): boolean {
	return Boolean(errors.tlsCaCert || errors.tlsCert || errors.tlsKey);
}
