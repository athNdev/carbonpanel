// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playerskin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// lookupTimeout bounds every identity-provider call independently of the
// caller. A timeout is an outage (UNAVAILABLE), never a negative result.
const lookupTimeout = 5 * time.Second

// LookupStatus is the honest outcome of an exact-identity lookup. It mirrors
// the PlayerLookupStatus proto enum but stays local to the provider port so the
// RPC layer is not coupled to this package.
type LookupStatus string

const (
	LookupFound       LookupStatus = "found"
	LookupNotFound    LookupStatus = "not_found"
	LookupAmbiguous   LookupStatus = "ambiguous"
	LookupUnavailable LookupStatus = "unavailable"
)

// LookupResult resolves an exact query to one online identity. Name is the
// provider's current name, never a name-history entry. Note explains every
// non-found status (and may accompany a found result as a disclosure).
type LookupResult struct {
	Status LookupStatus
	UUID   string
	Name   string
	Note   string
	Source string
}

var (
	namePattern       = regexp.MustCompile(`^[A-Za-z0-9_]{1,16}$`)
	undashedUUID      = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	errMalformedName  = errors.New("identity service returned a malformed name profile")
	errMalformedUUID  = errors.New("identity service returned an invalid UUID")
	errNameDisagree   = errors.New("identity service resolved a different name than requested")
	errUnexpectedResp = errors.New("identity service returned an unexpected response")
)

// NormalizeQuery classifies a trimmed operator query without any network I/O.
// It returns "uuid" with a canonical dashed UUID, "name" with the exact
// username, or "invalid". UUIDs (32 hex or 36 dashed hex) and usernames
// (1-16 ASCII name characters) are disjoint, so a value is never guessed to
// be one or the other.
func NormalizeQuery(query string) (kind, value string) {
	q := strings.TrimSpace(query)
	if q == "" {
		return "invalid", ""
	}
	compact := strings.ReplaceAll(q, "-", "")
	if (len(q) == 36 && uuidPattern.MatchString(q)) || (len(q) == 32 && undashedUUID.MatchString(q)) {
		if !undashedUUID.MatchString(compact) {
			return "invalid", ""
		}
		compact = strings.ToLower(compact)
		return "uuid", compact[0:8] + "-" + compact[8:12] + "-" + compact[12:16] + "-" + compact[16:20] + "-" + compact[20:32]
	}
	if namePattern.MatchString(q) {
		return "name", q
	}
	return "invalid", ""
}

// Lookup resolves an exact UUID or current username through Mojang's
// first-party identity endpoints. It never queries name history and never
// invents an identity: 404 is NOT_FOUND, malformed/conflicting replies are
// AMBIGUOUS, and transport/rate-limit/5xx replies are UNAVAILABLE.
func (c *Cache) Lookup(ctx context.Context, query string) (LookupResult, error) {
	bounded, cancel := context.WithTimeout(withContext(ctx), lookupTimeout)
	defer cancel()
	ctx = bounded

	kind, value := NormalizeQuery(query)
	if kind == "invalid" {
		return LookupResult{
			Status: LookupAmbiguous,
			Note:   "Enter an exact UUID (32 or 36 characters) or a username of 1-16 letters, digits or underscores.",
			Source: SourcePanelDB,
		}, nil
	}
	if kind == "uuid" {
		return c.lookupUUID(ctx, value)
	}
	return c.lookupName(ctx, value)
}

func (c *Cache) lookupUUID(ctx context.Context, uuid string) (LookupResult, error) {
	profile, status, err := c.getProfile(ctx, uuid)
	if err != nil {
		return unavailableLookup(errUpstream.Error() + ": " + err.Error()), nil
	}
	switch status {
	case http.StatusOK:
	case http.StatusNoContent, http.StatusNotFound:
		return LookupResult{Status: LookupNotFound, Note: "No Mojang profile exists for that UUID.", Source: SourceIdentityService}, nil
	default:
		return unavailableLookup(errUnexpectedResp.Error()), nil
	}
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		// An authoritative 200 with no profile fields is a definitive negative,
		// not an outage and not a guessed identity.
		return LookupResult{Status: LookupNotFound, Note: "The identity service returned an empty profile for that UUID.", Source: SourceIdentityService}, nil
	}
	// Defensive: the session server is keyed by UUID, but never pair the
	// requested UUID with a name from a malformed reply for a different id.
	if returned := strings.TrimSpace(profile.ID); returned != "" {
		kind, canonical := NormalizeQuery(returned)
		if kind != "uuid" || canonical != uuid {
			return LookupResult{
				Status: LookupAmbiguous,
				Note:   "The identity profile did not match the requested UUID; refusing to guess an identity.",
				Source: SourceIdentityService,
			}, nil
		}
	}
	return LookupResult{Status: LookupFound, UUID: uuid, Name: name, Source: SourceIdentityService}, nil
}

func (c *Cache) lookupName(ctx context.Context, name string) (LookupResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nameBaseURL+url.PathEscape(name), nil)
	if err != nil {
		return unavailableLookup(errUpstream.Error() + ": " + err.Error()), nil
	}
	resp, err := c.clientFor(expectedHost(nameBaseURL)).Do(req)
	if err != nil {
		return unavailableLookup(errUpstream.Error() + ": " + err.Error()), nil
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNoContent, http.StatusNotFound:
		return LookupResult{Status: LookupNotFound, Note: "No Mojang profile exists for that exact username.", Source: SourceIdentityService}, nil
	default:
		// 429 (rate limit), 5xx and unexpected codes are explicitly unknown,
		// never a definitive absence.
		return unavailableLookup(errUnexpectedResp.Error()), nil
	}
	body, err := readBounded(resp.Body, maxProfileBytes)
	if err != nil {
		return unavailableLookup(errUpstream.Error() + ": " + err.Error()), nil
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		// Authoritative 200 with an empty body is a definitive negative.
		return LookupResult{Status: LookupNotFound, Note: "The identity service returned no profile for that exact username.", Source: SourceIdentityService}, nil
	}
	var payload struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return LookupResult{Status: LookupAmbiguous, Note: errMalformedName.Error() + "; refusing to guess an identity.", Source: SourceIdentityService}, nil
	}
	kind, canonical := NormalizeQuery(payload.ID)
	if kind != "uuid" {
		return LookupResult{Status: LookupAmbiguous, Note: errMalformedUUID.Error() + "; refusing to guess an identity.", Source: SourceIdentityService}, nil
	}
	providerName := strings.TrimSpace(payload.Name)
	if providerName == "" {
		return LookupResult{Status: LookupAmbiguous, Note: errMalformedName.Error() + "; refusing to guess an identity.", Source: SourceIdentityService}, nil
	}
	if !strings.EqualFold(providerName, name) {
		return LookupResult{Status: LookupAmbiguous, Note: errNameDisagree.Error() + "; use the exact UUID instead.", Source: SourceIdentityService}, nil
	}
	return LookupResult{Status: LookupFound, UUID: canonical, Name: providerName, Source: SourceIdentityService}, nil
}

func unavailableLookup(note string) LookupResult {
	return LookupResult{Status: LookupUnavailable, Note: "Identity service unavailable; try again later. " + note, Source: SourceIdentityService}
}
