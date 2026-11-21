// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import "strings"

// ActualLevelName resolves the configured save folder. An explicit level-name
// in custom server.properties wins over the panel's Level field; Minecraft
// otherwise uses "world". Invalid or unknown configuration has no level name.
func (cfg *ServerConfig) ActualLevelName() string {
	if cfg == nil {
		return ""
	}
	if cfg.CustomServerProperties != nil {
		for _, line := range strings.Split(*cfg.CustomServerProperties, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if ok && strings.EqualFold(strings.TrimSpace(key), "level-name") {
				return plainLevelName(strings.TrimSpace(value))
			}
		}
	}
	if cfg.Level != nil && strings.TrimSpace(*cfg.Level) != "" {
		return plainLevelName(strings.TrimSpace(*cfg.Level))
	}
	return "world"
}

// The local save folder is a single path component.
func plainLevelName(name string) string {
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, "-") ||
		strings.ContainsAny(name, "/\\:\x00\r\n") {
		return ""
	}
	return name
}
