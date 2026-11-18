package main

import "net/http"

// maxPanelHeaderBytes pins the panel's request-header bound explicitly. It
// matches net/http's historical default, so documented deployments keep their
// behavior while the limit can no longer be lost by omitting the field.
const maxPanelHeaderBytes = 1 << 20 // 1 MiB

// applyPanelRequestLimits centralizes request-size limits for the panel
// listener so production and loopback fixtures use the same configuration.
func applyPanelRequestLimits(srv *http.Server) *http.Server {
	if srv != nil {
		srv.MaxHeaderBytes = maxPanelHeaderBytes
	}
	return srv
}
