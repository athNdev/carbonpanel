package indexers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTPClient wraps http.Client with common indexer request logic.
type HTTPClient struct {
	client       *http.Client
	userAgent    string
	indexer      string
	extraHeaders map[string]string
}

// NewHTTPClient creates a shared HTTP client for an indexer.
func NewHTTPClient(indexer string, userAgent string, extraHeaders map[string]string) *HTTPClient {
	return NewHTTPClientWithClient(indexer, userAgent, extraHeaders, nil)
}

// NewHTTPClientWithClient permits a caller to supply an HTTP transport and
// timeout. A nil client retains the standard indexer timeout.
func NewHTTPClientWithClient(indexer string, userAgent string, extraHeaders map[string]string, client *http.Client) *HTTPClient {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	headers := make(map[string]string, len(extraHeaders))
	for key, value := range extraHeaders {
		headers[key] = value
	}
	return &HTTPClient{
		client:       ProtectCredentialsOnRedirect(client),
		userAgent:    userAgent,
		indexer:      indexer,
		extraHeaders: headers,
	}
}

// ProtectCredentialsOnRedirect copies the client and prevents credentials from
// crossing scheme, host or port boundaries. It preserves a caller's redirect
// policy and the standard ten-redirect limit when no policy was supplied.
func ProtectCredentialsOnRedirect(client *http.Client) *http.Client {
	copyClient := *client
	previous := copyClient.CheckRedirect
	copyClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if previous != nil {
			if err := previous(req, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if len(via) > 0 && (!strings.EqualFold(req.URL.Scheme, via[0].URL.Scheme) || !strings.EqualFold(req.URL.Host, via[0].URL.Host)) {
			for _, name := range []string{"X-Api-Key", "Authorization", "Proxy-Authorization", "Cookie"} {
				req.Header.Del(name)
			}
		}
		return nil
	}
	return &copyClient
}

// DoJSON performs a GET request, checks the status, and JSON-decodes into dest.
// It returns structured IndexerErrors for all failure modes.
func (h *HTTPClient) DoJSON(ctx context.Context, url string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return &IndexerError{Kind: ErrNetwork, Indexer: h.indexer, URL: url, Err: err}
	}

	req.Header.Set("Accept", "application/json")
	if h.userAgent != "" {
		req.Header.Set("User-Agent", h.userAgent)
	}
	for k, v := range h.extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return NewNetworkError(h.indexer, url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return NewAPIError(h.indexer, resp.StatusCode, url, string(bodyBytes))
	}

	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return NewDecodeError(h.indexer, url, err)
	}

	return nil
}
