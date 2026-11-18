// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Command carbon-node-installer is the portable, mode-agnostic guest installer
// used by every managed Carbon Panel runtime mode. It mutates only the
// directories named on its command line. All JSON it prints is secret-free;
// the one-time enrollment secret is written only to a 0600 file inside the
// state directory and is emitted only inside a single claim request.
package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/athNdev/carbon-panel/internal/nodeinstall"
)

type options struct {
	stateDir     string
	dataDir      string
	artifactDir  string
	backupDir    string
	challenge    string
	publicKey    string
	targetOS     string
	targetArch   string
	label        string
	capabilities string
	protocol     int
	ttl          time.Duration
	now          string
	daemonSpec   string
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: carbon-node-installer <verb> [flags]

Flags:
  -state-dir DIR    guest-owned state directory (required for most verbs)
  -data-dir DIR     persistent data/world directory (volume verbs only)
  -artifact DIR     versioned artifact bundle directory
  -backup-dir DIR   new backup directory (backup) or existing one (recover)
  -challenge FILE   BeginEnrollmentChallenge response JSON (claim)
  -public-key FILE  Ed25519 PKIX public key that signs artifacts
  -target-os OS     artifact OS override (offline fixtures only)
  -target-arch ARCH artifact architecture override (offline fixtures only)
  -daemon-spec FILE disposable daemon spec JSON (render-daemon; read-only)

Verbs:
  init       create/verify the unique first-boot identity (idempotent)
  bundle     build the scoped, secret-free enrollment bundle
  claim      build the one-time, challenge-bound claim request
  verify     verify an artifact manifest, checksums and optional signature
  import     bind an existing persistent data directory
  backup     snapshot the persistent data directory into a new backup dir
  recover    verify a backup and copy files back (never deletes)
  upgrade    stage and promote a verified artifact into the inactive slot
  rollback   verify the undo slot and flip back to the previous version
  status     print redacted install state; finish an interrupted promotion
  render-daemon render a disposable daemon unit/config from a spec (read-only)`)
}

func loadPublicKey(path string) (*ed25519.PublicKey, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("%w: public key is not PEM", nodeinstall.ErrInvalidInput)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%w: public key is not Ed25519", nodeinstall.ErrInvalidInput)
	}
	return &pub, nil
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var o options
	global := flag.NewFlagSet("global", flag.ContinueOnError)
	global.StringVar(&o.stateDir, "state-dir", "", "guest-owned state directory")
	global.StringVar(&o.dataDir, "data-dir", "", "persistent data directory")
	global.StringVar(&o.artifactDir, "artifact", "", "artifact bundle directory")
	global.StringVar(&o.backupDir, "backup-dir", "", "backup directory")
	global.StringVar(&o.challenge, "challenge", "", "challenge response JSON")
	global.StringVar(&o.publicKey, "public-key", "", "Ed25519 public key PEM")
	global.StringVar(&o.targetOS, "target-os", "", "artifact OS override")
	global.StringVar(&o.targetArch, "target-arch", "", "artifact architecture override")
	global.StringVar(&o.label, "label", "", "node label")
	global.StringVar(&o.capabilities, "capabilities", "ROOTLESS,PRIVATE_SOCKET,DEDICATED_ROOTS,CGROUP_V2", "required capabilities")
	global.IntVar(&o.protocol, "protocol-version", 1, "protocol version")
	global.DurationVar(&o.ttl, "ttl", 10*time.Minute, "enrollment TTL")
	global.StringVar(&o.now, "now", "", "RFC3339 now override for offline fixtures")
	global.StringVar(&o.daemonSpec, "daemon-spec", "", "disposable daemon spec JSON")
	global.SetOutput(os.Stderr)
	verb := os.Args[1]
	verbArgs := os.Args[2:]
	if verb == "-h" || verb == "--help" || verb == "help" {
		usage()
		return
	}
	if err := global.Parse(verbArgs); err != nil {
		os.Exit(2)
	}
	args := global.Args()
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "unexpected arguments: %s\n", strings.Join(args, " "))
		os.Exit(2)
	}
	if err := run(verb, o); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(verb string, o options) error {
	now := time.Now().UTC()
	if o.now != "" {
		parsed, err := time.Parse(time.RFC3339, o.now)
		if err != nil {
			return fmt.Errorf("%w: bad -now: %v", nodeinstall.ErrInvalidInput, err)
		}
		now = parsed
	}
	caps := strings.Split(o.capabilities, ",")
	for i := range caps {
		caps[i] = strings.TrimSpace(caps[i])
	}

	switch verb {
	case "init":
		if o.stateDir == "" {
			return fmt.Errorf("%w: -state-dir is required", nodeinstall.ErrInvalidInput)
		}
		id, err := nodeinstall.InitIdentity(o.stateDir)
		if err != nil {
			return err
		}
		return printJSON(id)

	case "bundle":
		if o.stateDir == "" || o.label == "" {
			return fmt.Errorf("%w: -state-dir and -label are required", nodeinstall.ErrInvalidInput)
		}
		b, err := nodeinstall.BuildEnrollmentBundle(o.stateDir, o.label, caps, int32(o.protocol), o.ttl)
		if err != nil {
			return err
		}
		return printJSON(b)

	case "claim":
		if o.stateDir == "" || o.challenge == "" {
			return fmt.Errorf("%w: -state-dir and -challenge are required", nodeinstall.ErrInvalidInput)
		}
		raw, err := os.ReadFile(o.challenge)
		if err != nil {
			return err
		}
		var ch nodeinstall.ClaimChallenge
		if err := json.Unmarshal(raw, &ch); err != nil {
			return fmt.Errorf("%w: challenge JSON: %v", nodeinstall.ErrInvalidInput, err)
		}
		out, err := nodeinstall.BuildClaim(o.stateDir, ch, now)
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil

	case "verify":
		pub, err := loadPublicKey(o.publicKey)
		if err != nil {
			return err
		}
		m, err := nodeinstall.VerifyArtifact(o.artifactDir, o.targetOS, o.targetArch, pub)
		if err != nil {
			return err
		}
		return printJSON(m)

	case "import":
		b, err := nodeinstall.ImportVolume(o.stateDir, o.dataDir)
		if err != nil {
			return err
		}
		return printJSON(b)

	case "backup":
		m, err := nodeinstall.BackupVolume(o.dataDir, o.backupDir)
		if err != nil {
			return err
		}
		return printJSON(m)

	case "recover":
		if err := nodeinstall.RecoverVolume(o.backupDir, o.dataDir); err != nil {
			return err
		}
		return printJSON(map[string]string{"status": "recovered", "data_dir": o.dataDir})

	case "upgrade":
		pub, err := loadPublicKey(o.publicKey)
		if err != nil {
			return err
		}
		st, err := nodeinstall.Upgrade(o.stateDir, o.artifactDir, o.targetOS, o.targetArch, pub)
		if err != nil {
			return err
		}
		return printJSON(st)

	case "rollback":
		st, err := nodeinstall.Rollback(o.stateDir)
		if err != nil {
			return err
		}
		return printJSON(st)

	case "status":
		st, err := nodeinstall.Status(o.stateDir)
		if err != nil {
			return err
		}
		return printJSON(st)

	case "render-daemon":
		if o.daemonSpec == "" {
			return fmt.Errorf("%w: -daemon-spec is required", nodeinstall.ErrInvalidInput)
		}
		raw, err := os.ReadFile(o.daemonSpec)
		if err != nil {
			return err
		}
		var spec nodeinstall.DaemonSpec
		if err := json.Unmarshal(raw, &spec); err != nil {
			return fmt.Errorf("%w: daemon spec JSON: %v", nodeinstall.ErrInvalidInput, err)
		}
		rendered, err := nodeinstall.RenderDaemon(spec)
		if err != nil {
			return err
		}
		return printJSON(rendered)

	default:
		usage()
		return fmt.Errorf("%w: unknown verb %q", nodeinstall.ErrInvalidInput, verb)
	}
}
