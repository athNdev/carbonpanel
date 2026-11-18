// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package native

// The host-native lane consumes the SINGLE mode-agnostic enrollment boundary
// in internal/noderuntime/enroll. This file is only a compatibility shim: it
// re-exports the shared types so existing native callers and mode-lane entry
// points keep compiling without a second proof-of-possession, claim-context or
// certificate-issuance implementation. No enrollment logic lives here.

import "github.com/athNdev/carbon-panel/internal/noderuntime/enroll"

// Shared enrollment types, re-exported for mode-lane compatibility.
type (
	EnrollmentAuthority = enroll.EnrollmentAuthority
	Enrollment          = enroll.Enrollment
	TLSTopology         = enroll.TLSTopology
	CredentialKind      = enroll.CredentialKind
	Credential          = enroll.Credential
	NodeBundle          = enroll.NodeBundle
	Boundary            = enroll.Boundary
)

// Shared topology and credential constants.
const (
	TopologyUnset        = enroll.TopologyUnset
	TopologyDirectTLS    = enroll.TopologyDirectTLS
	TopologyL4PassThrough = enroll.TopologyL4PassThrough
	TopologyTrustedProxy = enroll.TopologyTrustedProxy
	TopologyUnsupported  = enroll.TopologyUnsupported

	CredentialBootstrap = enroll.CredentialBootstrap
	CredentialNode      = enroll.CredentialNode
	CredentialOperator  = enroll.CredentialOperator

	SigningDomain = enroll.SigningDomain
)

// Shared refusal errors, re-exported so callers can use errors.Is uniformly.
var (
	ErrRefused       = enroll.ErrRefused
	ErrMismatchedKey = enroll.ErrMismatchedKey
	ErrStaleNonce    = enroll.ErrStaleNonce
	ErrReplay        = enroll.ErrReplay
	ErrRevoked       = enroll.ErrRevoked
	ErrScope         = enroll.ErrScope
	ErrTopology      = enroll.ErrTopology
	ErrMissingProof  = enroll.ErrMissingProof
	ErrRateLimited   = enroll.ErrRateLimited
)

// Shared constructors and context helpers.
var (
	NewEnrollmentAuthority = enroll.NewEnrollmentAuthority
	RestoreAuthority       = enroll.RestoreAuthority
	NewBoundary            = enroll.NewBoundary
	GenerateNodeBundle     = enroll.GenerateNodeBundle
	ClaimContext           = enroll.ClaimContext
	ClaimContextRaw        = enroll.ClaimContextRaw
)
