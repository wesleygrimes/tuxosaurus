// Package rexclient is a pure-Go client for Rex-compatible terminal servers.
// It works against both the official Rex server and Tuxosaurus, so it relies
// only on methods both advertise. It must stay free of cgo so rexctl can be
// built as a static binary.
package rexclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Endpoint describes how to reach a server: a WebSocket base URL and the HTTP
// client used to dial it. Unix sockets dial a fixed placeholder host because
// the server ignores the Host header on local connections.
type Endpoint struct {
	Raw    string
	WSBase string
	HTTP   *http.Client
}

// ParseEndpoint accepts https://host, http://host:port and unix:///path.
// A bare host name is treated as https, which matches how Rex servers are
// published on a tailnet.
func ParseEndpoint(raw string) (*Endpoint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("no server given")
	}
	if strings.HasPrefix(raw, "unix://") {
		path := strings.TrimPrefix(raw, "unix://")
		if path == "" {
			return nil, fmt.Errorf("unix endpoint %q has no socket path", raw)
		}
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}}
		return &Endpoint{Raw: raw, WSBase: "ws://rex", HTTP: &http.Client{Transport: transport}}, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("server URL %q: %w", raw, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("server URL %q has no host", raw)
	}
	var scheme string
	switch u.Scheme {
	case "https", "wss":
		scheme = "wss"
	case "http", "ws":
		scheme = "ws"
	default:
		return nil, fmt.Errorf("server URL %q: unsupported scheme %q", raw, u.Scheme)
	}
	base := scheme + "://" + u.Host + strings.TrimRight(u.Path, "/")
	return &Endpoint{Raw: raw, WSBase: base, HTTP: http.DefaultClient}, nil
}
