// SPDX-License-Identifier: MPL-2.0
package main

import (
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func main() {
	if !controlAllowed(os.Args[1:], os.Getenv) {
		fmt.Fprintln(os.Stderr, "public listener control requires explicit disposable hosted-runner opt-in")
		os.Exit(2)
	}
	// A mistaken administrator launch is not a standard-profile control.
	token := windows.GetCurrentProcessToken()
	if token.IsElevated() {
		fmt.Fprintln(os.Stderr, "public listener control refuses an elevated token")
		os.Exit(2)
	}
	user, err := token.GetTokenUser()
	if err != nil || user.User.Sid.String() != os.Getenv("CARBON_WINDOWS_CONTROL_USER_SID") {
		fmt.Fprintln(os.Stderr, "public listener control requires the disposable user's token")
		os.Exit(2)
	}
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer func() { _ = listener.Close() }()
	fmt.Println("Disposable public listener control started")
	time.Sleep(10 * time.Second)
}
