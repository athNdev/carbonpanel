// SPDX-License-Identifier: MPL-2.0
// govulncheck-policy runs the pinned JSON scanner and applies the repository's
// policy: reachable findings with published fixes fail; unfixed ones are reported.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
)

// The v1.9.0 JSON handler exits zero after a complete scan, including scans with
// findings. Text mode's exit code 3 is not accepted in this JSON-only command.
// See golang.org/x/vuln/internal/{govulncheck/jsonhandler.go,scan/run.go}.
const scannerVersion = "v1.9.0"

type config struct {
	ProtocolVersion string `json:"protocol_version"`
	ScannerName     string `json:"scanner_name"`
	ScannerVersion  string `json:"scanner_version"`
	ScanLevel       string `json:"scan_level"`
	ScanMode        string `json:"scan_mode"`
}

type finding struct {
	OSV          string `json:"osv"`
	FixedVersion string `json:"fixed_version,omitempty"`
	Trace        []struct {
		Module   string `json:"module"`
		Function string `json:"function"`
	} `json:"trace"`
}

type report struct {
	Outcome string   `json:"outcome"`
	Scanner config   `json:"scanner"`
	Fixable []string `json:"fixable"`
	Unfixed []string `json:"unfixed"`
	Error   string   `json:"error,omitempty"`
}

func main() {
	scanner := flag.String("scanner", "govulncheck", "path to govulncheck v1.9.0")
	output := flag.String("output", "govulncheck.json", "raw JSON report path")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "govulncheck-policy accepts no package arguments; it scans ./...")
		os.Exit(1)
	}
	f, err := os.Create(*output)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	r, scanErr := run(exec.Command(*scanner, "-json", "-scan=symbol", "./..."), f, os.Stderr)
	if err := f.Close(); err != nil {
		scanErr = errors.Join(scanErr, err)
	}
	if scanErr != nil {
		r.Outcome, r.Error = "error", scanErr.Error()
	}
	if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if scanErr != nil || len(r.Fixable) != 0 {
		os.Exit(1)
	}
}

func run(cmd *exec.Cmd, output *os.File, stderr io.Writer) (report, error) {
	cmd.Stdout, cmd.Stderr = output, stderr
	if err := cmd.Run(); err != nil {
		return report{}, fmt.Errorf("scanner did not complete: %w", err)
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		return report{}, err
	}
	return parse(output)
}

func parse(input io.Reader) (report, error) {
	r := report{Fixable: []string{}, Unfixed: []string{}}
	dec := json.NewDecoder(input)
	seenConfig, seenSBOM := false, false
	fixable, unfixed := map[string]bool{}, map[string]bool{}
	for {
		var msg map[string]json.RawMessage
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return r, fmt.Errorf("invalid scanner JSON: %w", err)
		}
		if len(msg) != 1 || (!seenConfig && msg["config"] == nil) {
			return r, errors.New("expected one message field, with config first")
		}
		for kind, payload := range msg {
			if string(payload) == "null" {
				return r, fmt.Errorf("empty %s message", kind)
			}
			switch kind {
			case "config":
				if seenConfig {
					return r, errors.New("duplicate scanner config")
				}
				if err := json.Unmarshal(payload, &r.Scanner); err != nil {
					return r, err
				}
				c := r.Scanner
				if c.ProtocolVersion != "v1.0.0" || c.ScannerName != "govulncheck" || c.ScannerVersion != scannerVersion || c.ScanMode != "source" || c.ScanLevel != "symbol" {
					return r, fmt.Errorf("unsupported scanner configuration: %+v", c)
				}
				seenConfig = true
			case "SBOM":
				var sbom struct {
					Roots []string `json:"roots"`
				}
				if err := json.Unmarshal(payload, &sbom); err != nil {
					return r, err
				}
				if seenSBOM || len(sbom.Roots) == 0 {
					return r, errors.New("missing scan roots or duplicate SBOM")
				}
				seenSBOM = true
			case "finding":
				var f finding
				if err := json.Unmarshal(payload, &f); err != nil {
					return r, err
				}
				if f.OSV == "" || len(f.Trace) == 0 || f.Trace[0].Module == "" {
					return r, errors.New("incomplete scanner finding")
				}
				// Module/package findings do not establish a reachable symbol.
				if f.Trace[0].Function == "" {
					continue
				}
				if f.FixedVersion == "" {
					unfixed[f.OSV] = true
				} else {
					fixable[f.OSV+" ("+f.Trace[0].Module+" fixed in "+f.FixedVersion+")"] = true
				}
			case "progress", "osv":
				var object map[string]json.RawMessage
				if err := json.Unmarshal(payload, &object); err != nil || len(object) == 0 {
					return r, fmt.Errorf("invalid %s message", kind)
				}
			default:
				return r, fmt.Errorf("unknown scanner message %q", kind)
			}
		}
	}
	if !seenConfig || !seenSBOM {
		return r, errors.New("incomplete scanner stream: config and populated SBOM required")
	}
	for id := range fixable {
		r.Fixable = append(r.Fixable, id)
	}
	for id := range unfixed {
		r.Unfixed = append(r.Unfixed, id)
	}
	sort.Strings(r.Fixable)
	sort.Strings(r.Unfixed)
	r.Outcome = "clean"
	if len(r.Unfixed) != 0 {
		r.Outcome = "unfixed"
	}
	if len(r.Fixable) != 0 {
		r.Outcome = "fixable"
	}
	return r, nil
}
