// SPDX-License-Identifier: MPL-2.0
// Command acceptance-evidence validates the OSS acceptance-evidence contract
// offline and audits its GitHub references. Exit code 1 means closure is blocked.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/athNdev/carbon-panel/internal/acceptanceevidence"
)

func loadContract(path string) (acceptanceevidence.Contract, error) {
	var c acceptanceevidence.Contract
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(raw, &c)
	return c, err
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "validate":
		validate(os.Args[2:])
	case "verify-github":
		verifyGitHub(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: acceptance-evidence validate|verify-github [flags]")
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

func parseNow(fs *flag.FlagSet) *string {
	return fs.String("now", "", "RFC3339 timestamp to use as 'now' (tests only)")
}

func resolveNow(value string) time.Time {
	if value != "" {
		if t, err := time.Parse(time.RFC3339, value); err == nil {
			return t
		}
	}
	return time.Now().UTC()
}

func validate(args []string) {
	fs := newFlags("validate")
	contractPath := fs.String("contract", "docs/acceptance/oss-acceptance-evidence.json", "contract path")
	root := fs.String("root", ".", "repository root")
	nowFlag := parseNow(fs)
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	c, err := loadContract(*contractPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "contract error:", err)
		os.Exit(2)
	}
	report := acceptanceevidence.ValidateContract(c, *root, resolveNow(*nowFlag))
	out, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(out))
	if !report.Passed {
		os.Exit(1)
	}
}

func verifyGitHub(args []string) {
	fs := newFlags("verify-github")
	contractPath := fs.String("contract", "docs/acceptance/oss-acceptance-evidence.json", "contract path")
	apiBase := fs.String("api-base", "https://api.github.com", "GitHub API base URL")
	repository := fs.String("repository", "athNdev/carbonpanel", "default repository")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	c, err := loadContract(*contractPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "contract error:", err)
		os.Exit(2)
	}
	client := acceptanceevidence.GitHubClient{
		BaseURL:    *apiBase,
		Token:      os.Getenv("GITHUB_TOKEN"),
		Repository: *repository,
	}
	failures := client.VerifyGitHub(context.Background(), c)
	out, _ := json.MarshalIndent(map[string]interface{}{"passed": len(failures) == 0, "failures": failures}, "", "  ")
	fmt.Println(string(out))
	if len(failures) > 0 {
		os.Exit(1)
	}
}
