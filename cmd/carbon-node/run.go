// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, you can obtain one at https://mozilla.org/MPL/2.0/.

package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/athNdev/carbon-panel/internal/noderuntime/agent"
)

func runAgent(args []string) error {
	var config agent.Config
	flags := flag.NewFlagSet("carbon-node run", flag.ContinueOnError)
	factsPath := flags.String("facts", "", "qualified host facts JSON")
	controllerCAPath := flags.String("controller-ca", "", "trusted HTTPS serving CA PEM")
	enrollmentCAPath := flags.String("enrollment-ca", "", "trusted node certificate issuer PEM")
	flags.StringVar(&config.StateDir, "state-dir", "", "portable installer's owned state directory")
	flags.StringVar(&config.ControllerURL, "controller", "", "direct HTTPS controller origin")
	flags.StringVar(&config.EnrollmentID, "enrollment-id", "", "operator-approved enrollment ID on first start")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || config.StateDir == "" || config.ControllerURL == "" ||
		*factsPath == "" || *controllerCAPath == "" || *enrollmentCAPath == "" {
		flags.Usage()
		return errors.New("managed agent configuration is incomplete")
	}
	facts, err := readAgentInput(*factsPath)
	if err != nil {
		return fmt.Errorf("read host facts: %w", err)
	}
	if err := json.Unmarshal(facts, &config.Facts); err != nil {
		return fmt.Errorf("decode host facts: %w", err)
	}
	controllerChain, err := readAgentInput(*controllerCAPath)
	if err != nil {
		return fmt.Errorf("read controller CA: %w", err)
	}
	config.ControllerCAs = x509.NewCertPool()
	if !config.ControllerCAs.AppendCertsFromPEM(controllerChain) {
		return errors.New("controller CA file contains no certificates")
	}
	enrollmentCA, err := readAgentInput(*enrollmentCAPath)
	if err != nil {
		return fmt.Errorf("read enrollment CA: %w", err)
	}
	block, rest := pem.Decode(enrollmentCA)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return errors.New("enrollment CA file must contain one PEM certificate")
	}
	config.EnrollmentCA, err = x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse enrollment CA: %w", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := agent.Run(ctx, config); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func readAgentInput(path string) ([]byte, error) {
	const maxInputBytes = 128 << 10
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	b, err := io.ReadAll(io.LimitReader(file, maxInputBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxInputBytes {
		return nil, errors.New("managed agent input exceeds 128 KiB")
	}
	return b, nil
}
