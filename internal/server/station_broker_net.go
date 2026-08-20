package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"quasar/internal/station"
)

// The capabilities that go out over the network: the two HTTP verbs a station
// may use, and the service address they are usually pointed at.
//
// Dispatched from Do in station_broker.go.

type fetchArgs struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type serviceArgs struct {
	Service string `json:"service"`
	Port    int    `json:"port"`
}

// fetcher is the station's way out, built once per call because resolving the
// containers behind net.internal costs a Docker round trip and most calls make
// no request at all.
func (c *stationCall) fetcher(ctx context.Context) *station.Fetcher {
	if c.net != nil {
		return c.net
	}
	internal := map[string]string{}
	if dock, err := c.containers(); err == nil && c.app != nil {
		for _, service := range c.doc.Permissions.InternalServices() {
			if host, err := dock.ServiceHost(ctx, c.app, service); err == nil {
				internal[service] = host
			}
		}
	}
	c.net = station.NewFetcher(c.doc.Permissions, internal)
	return c.net
}

// fetch performs one request, if the document said it could.
func (c *stationCall) fetch(ctx context.Context, capability string, raw json.RawMessage) (json.RawMessage, error) {
	var a fetchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	method := http.MethodGet
	if capability == "http.post" {
		method = http.MethodPost
	}

	resp, err := c.fetcher(ctx).Do(ctx, method, a.URL, a.Headers, a.Body)
	if err != nil {
		return nil, err
	}
	// Only what left this server is recorded. An internal request never left
	// the machine, and an audit log that cannot be skimmed is one nobody
	// skims — what an operator wants to find in it is the traffic that went
	// somewhere they would have to trust.
	if host := externalHost(a.URL); host != "" && c.doc.Permissions.AllowsHost(host) {
		c.audit("station.http.external", fmt.Sprintf("%s %s → %d", method, a.URL, resp.Status))
	}
	return json.Marshal(resp)
}

// serviceURL hands out an address on the internal network for a service and a
// port the document declared.
func (c *stationCall) serviceURL(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var a serviceArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	url, err := c.fetcher(ctx).ServiceURL(a.Service, a.Port)
	if err != nil {
		return nil, err
	}
	return json.Marshal(url)
}

// externalHost is the host of a URL that was meant for the internet, empty for
// anything else.
func externalHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
