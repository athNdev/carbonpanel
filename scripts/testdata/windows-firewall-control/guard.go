// SPDX-License-Identifier: MPL-2.0
package main

// This control deliberately opens a public socket. It is never part of the
// ordinary suite and must have every disposable-runner opt-in before binding.
func controlAllowed(args []string, getenv func(string) string) bool {
	return len(args) == 1 && args[0] == "--allow-disposable-public-listener" &&
		getenv("GITHUB_ACTIONS") == "true" &&
		getenv("RUNNER_ENVIRONMENT") == "github-hosted" &&
		getenv("CARBON_WINDOWS_POSITIVE_CONTROL") == "1"
}
