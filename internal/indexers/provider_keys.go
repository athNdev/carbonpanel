// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package indexers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ProviderKeyValidation is the soft result of a provider credential check.
// A false Valid with a Message is a normal outcome, not an error: callers
// present it to the operator verbatim.
type ProviderKeyValidation struct {
	Valid   bool
	Message string
}

// Provider credential endpoints are variables so tests can aim the validator
// at a local fixture without touching the real upstream APIs.
var (
	curseforgeAPIBase = "https://api.curseforge.com"
	modrinthAPIBase   = "https://api.modrinth.com"
)

// newProviderKeyClient builds the validator's HTTP client. Credentials travel
// in headers, so redirects must never carry them across scheme/host/port
// boundaries (see ProtectCredentialsOnRedirect).
func newProviderKeyClient() *http.Client {
	return ProtectCredentialsOnRedirect(&http.Client{Timeout: 10 * time.Second})
}

// ValidateProviderKey checks a CurseForge or Modrinth credential against its
// upstream API. It is the single implementation shared by the generated
// ConfigService.ValidateProviderKey RPC and the legacy REST adapter; both must
// keep reporting provider-side outcomes as soft results.
func ValidateProviderKey(ctx context.Context, provider, apiKey, userAgent string) ProviderKeyValidation {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "curseforge", "fuego":
		return validateCurseForgeKey(ctx, apiKey)
	case "modrinth":
		return validateModrinthKey(ctx, apiKey, userAgent)
	default:
		return ProviderKeyValidation{Message: fmt.Sprintf("Unsupported provider: %s", provider)}
	}
}

func validateCurseForgeKey(ctx context.Context, apiKey string) ProviderKeyValidation {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return ProviderKeyValidation{Message: "CurseForge API key is empty"}
	}

	// Validate with CurseForge API: get Minecraft mod categories
	reqCF, err := http.NewRequestWithContext(ctx, http.MethodGet, curseforgeAPIBase+"/v1/categories?gameId=432", nil)
	if err != nil {
		return ProviderKeyValidation{Message: fmt.Sprintf("Failed to build request: %v", err)}
	}
	reqCF.Header.Set("x-api-key", key)
	reqCF.Header.Set("Accept", "application/json")

	httpClient := newProviderKeyClient()
	res, err := httpClient.Do(reqCF)
	if err != nil {
		return ProviderKeyValidation{Message: fmt.Sprintf("Connection to CurseForge API failed: %v", err)}
	}
	defer func() { _ = res.Body.Close() }()

	switch res.StatusCode {
	case http.StatusOK:
		// Also check if mods/search endpoint allows this key
		reqSearch, sErr := http.NewRequestWithContext(ctx, http.MethodGet, curseforgeAPIBase+"/v1/mods/search?gameId=432&pageSize=1", nil)
		if sErr == nil {
			reqSearch.Header.Set("x-api-key", key)
			reqSearch.Header.Set("Accept", "application/json")
			resSearch, doErr := httpClient.Do(reqSearch)
			if doErr == nil {
				defer func() { _ = resSearch.Body.Close() }()
				switch resSearch.StatusCode {
				case http.StatusOK:
					return ProviderKeyValidation{Valid: true, Message: "Connected successfully! CurseForge API key is valid and approved for mod searches."}
				case http.StatusForbidden, http.StatusUnauthorized:
					return ProviderKeyValidation{Valid: true, Message: "CurseForge API key accepted, but Overwolf restricts direct mod search (403). Carbon Panel will automatically use the keyless community proxy for mod searches!"}
				default:
					return ProviderKeyValidation{Valid: true, Message: "Connected successfully! CurseForge API key is valid."}
				}
			}
			// On a Do error the response body is already closed by net/http;
			// there is nothing to clean up here.
		}
		return ProviderKeyValidation{Valid: true, Message: "Connected successfully! CurseForge API key is valid."}
	case http.StatusForbidden, http.StatusUnauthorized:
		return ProviderKeyValidation{Message: "CurseForge rejected API key (403 Forbidden). Please verify your key at https://console.curseforge.com/#/api-keys."}
	default:
		// Do not reflect upstream response text to the console. Provider
		// errors can contain echoed credentials, internal URLs or other
		// operator-sensitive detail; return a typed status instead.
		return ProviderKeyValidation{Message: fmt.Sprintf("CurseForge returned unexpected status %d", res.StatusCode)}
	}
}

func validateModrinthKey(ctx context.Context, apiKey, userAgent string) ProviderKeyValidation {
	token := strings.TrimSpace(apiKey)
	ua := strings.TrimSpace(userAgent)
	if ua == "" {
		ua = "CarbonPanel/1.0 (CarbonPanel-admin)"
	}

	httpClient := newProviderKeyClient()
	if token != "" {
		// Authenticated check with Modrinth user endpoint
		reqMR, err := http.NewRequestWithContext(ctx, http.MethodGet, modrinthAPIBase+"/v2/user", nil)
		if err != nil {
			return ProviderKeyValidation{Message: fmt.Sprintf("Failed to build request: %v", err)}
		}
		reqMR.Header.Set("Authorization", token)
		reqMR.Header.Set("User-Agent", ua)

		res, err := httpClient.Do(reqMR)
		if err != nil {
			return ProviderKeyValidation{Message: fmt.Sprintf("Connection to Modrinth failed: %v", err)}
		}
		defer func() { _ = res.Body.Close() }()

		switch res.StatusCode {
		case http.StatusOK:
			var user struct {
				Username string `json:"username"`
			}
			_ = json.NewDecoder(res.Body).Decode(&user)
			if user.Username != "" {
				return ProviderKeyValidation{Valid: true, Message: fmt.Sprintf("Authenticated successfully as %s on Modrinth.", user.Username)}
			}
			return ProviderKeyValidation{Valid: true, Message: "Authenticated successfully with Modrinth API."}
		case http.StatusUnauthorized:
			return ProviderKeyValidation{Message: "Modrinth rejected Personal Access Token (401 Unauthorized)."}
		default:
			return ProviderKeyValidation{Message: fmt.Sprintf("Modrinth returned status %d", res.StatusCode)}
		}
	}

	// Anonymous connectivity check
	reqMR, err := http.NewRequestWithContext(ctx, http.MethodGet, modrinthAPIBase+"/v2/search?limit=1", nil)
	if err != nil {
		return ProviderKeyValidation{Message: fmt.Sprintf("Failed to build request: %v", err)}
	}
	reqMR.Header.Set("User-Agent", ua)

	res, err := httpClient.Do(reqMR)
	if err != nil {
		return ProviderKeyValidation{Message: fmt.Sprintf("Connection to Modrinth failed: %v", err)}
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode == http.StatusOK {
		return ProviderKeyValidation{Valid: true, Message: "Modrinth API connected successfully (public access)."}
	}
	return ProviderKeyValidation{Message: fmt.Sprintf("Modrinth returned status %d", res.StatusCode)}
}
