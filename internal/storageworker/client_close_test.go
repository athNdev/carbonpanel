// SPDX-License-Identifier: MPL-2.0
package storageworker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type signalledWriter struct {
	io.Writer
	writes chan struct{}
}

func TestRejectedHandshakeClosesTransport(t *testing.T) {
	closed := 0
	_, err := NewSession(io.Discard, strings.NewReader(`{"ok":false,"error":"refused"}`), func() error {
		closed++
		return nil
	})
	if err == nil || closed != 1 {
		t.Fatalf("rejected handshake: error=%v, close calls=%d", err, closed)
	}
}

func TestCancelInterruptsBlockedHandshake(t *testing.T) {
	workerInput, clientOutput := io.Pipe()
	clientInput, workerOutput := io.Pipe()
	defer func() { _ = workerOutput.Close() }()
	handshakeRead := make(chan struct{})
	go func() {
		var request Request
		if json.NewDecoder(workerInput).Decode(&request) == nil {
			close(handshakeRead)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := NewSessionContext(ctx, clientOutput, clientInput, func() error {
			_ = workerInput.Close()
			_ = clientOutput.Close()
			return clientInput.Close()
		})
		result <- err
	}()
	select {
	case <-handshakeRead:
	case <-time.After(3 * time.Second):
		t.Fatal("handshake did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled handshake remained blocked")
	}
}

func (w signalledWriter) Write(p []byte) (int, error) {
	w.writes <- struct{}{}
	return w.Writer.Write(p)
}

func TestCloseInterruptsBlockedRequest(t *testing.T) {
	for _, blocked := range []string{"read", "write"} {
		t.Run(blocked, func(t *testing.T) {
			workerInput, clientOutput := io.Pipe()
			clientInput, workerOutput := io.Pipe()
			writes := make(chan struct{}, 2)
			requestRead := make(chan struct{})
			go func() {
				decoder := json.NewDecoder(workerInput)
				var request Request
				if decoder.Decode(&request) != nil {
					return
				}
				if json.NewEncoder(workerOutput).Encode(Response{OK: true, Root: "root"}) != nil {
					return
				}
				if blocked == "read" && decoder.Decode(&request) == nil {
					close(requestRead)
				}
			}()
			session, err := NewSession(signalledWriter{clientOutput, writes}, clientInput, func() error {
				_ = workerInput.Close()
				_ = clientOutput.Close()
				_ = clientInput.Close()
				return workerOutput.Close()
			})
			if err != nil {
				t.Fatal(err)
			}
			<-writes // handshake
			requestDone := make(chan error, 1)
			go func() { _, err := session.call(Request{Op: "stat"}); requestDone <- err }()
			select {
			case <-writes:
			case <-time.After(3 * time.Second):
				t.Fatal("request did not start writing")
			}
			if blocked == "read" {
				select {
				case <-requestRead:
				case <-time.After(3 * time.Second):
					t.Fatal("worker did not receive request")
				}
			}
			closeDone := make(chan error, 1)
			go func() { closeDone <- session.Close() }()
			select {
			case err := <-closeDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("close blocked behind request")
			}
			select {
			case err := <-requestDone:
				if err == nil {
					t.Fatal("interrupted request succeeded")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("request remained blocked")
			}
		})
	}
}
