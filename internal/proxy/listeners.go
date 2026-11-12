// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package proxy

import "net"

// ListenerFactory supplies a manager's sockets. A zero value uses the operating
// system listeners; tests can reject public binds before reaching the OS.
// Functions are copied into each proxy at construction, never changed globally.
type ListenerFactory struct {
	TCP func(network, address string) (net.Listener, error)
	UDP func(network string, address *net.UDPAddr) (*net.UDPConn, error)
}

func (f ListenerFactory) listenTCP(network, address string) (net.Listener, error) {
	if f.TCP != nil {
		return f.TCP(network, address)
	}
	return net.Listen(network, address)
}

func (f ListenerFactory) listenUDP(network string, address *net.UDPAddr) (*net.UDPConn, error) {
	if f.UDP != nil {
		return f.UDP(network, address)
	}
	return net.ListenUDP(network, address)
}
