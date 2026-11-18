// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package native

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

const fixtureTimeValue = "2026-11-18T12:00:00Z"

func fixtureTime() time.Time {
	t, _ := time.Parse(time.RFC3339, fixtureTimeValue)
	return t
}

// testAuthority is an owned, disposable enrollment fixture. Its private
// runtime directory is a temp dir and all material is synthetic/ephemeral.
type testAuthority struct {
	authority    *EnrollmentAuthority
	bundle       *NodeBundle
	enrollmentID string
	nodeID       string
}

func newTestAuthority(t *testing.T, topology TLSTopology) *testAuthority {
	t.Helper()
	a, err := NewEnrollmentAuthority(topology)
	if err != nil {
		t.Fatal(err)
	}
	a.WithClock(func() time.Time { return fixtureTime() })
	dir := t.TempDir()
	bundle, req, err := GenerateNodeBundle(dir, "node-fixture-native", "fixture-node")
	if err != nil {
		t.Fatal(err)
	}
	operator := Credential{Kind: CredentialOperator}
	resp, err := a.CreateEnrollment(operator, req)
	if err != nil {
		t.Fatal(err)
	}
	return &testAuthority{
		authority:    a,
		bundle:       bundle,
		enrollmentID: resp.GetEnrollment().GetEnrollmentId(),
		nodeID:       bundle.NodeID,
	}
}

func (ta *testAuthority) validClaim(t *testing.T) *v1.ClaimEnrollmentRequest {
	t.Helper()
	challenge, err := ta.authority.BeginEnrollmentChallenge(&v1.BeginEnrollmentChallengeRequest{EnrollmentId: ta.enrollmentID})
	if err != nil {
		t.Fatal(err)
	}
	caps := []v1.NodeCapability{
		v1.NodeCapability_NODE_CAPABILITY_PRIVATE_SOCKET,
		v1.NodeCapability_NODE_CAPABILITY_DEDICATED_ROOTS,
	}
	claim, err := ta.bundle.BuildClaim(ta.enrollmentID, challenge, caps)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func otherFixtureKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}
