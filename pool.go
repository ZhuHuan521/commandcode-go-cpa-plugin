package plugin

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"golang.org/x/net/proxy"
)

type doer interface {
	do(ctx context.Context, method, url string, headers map[string]string, body []byte) (int, http.Header, []byte, error)
	doStream(ctx context.Context, url string, headers map[string]string, body []byte) (int, http.Header, <-chan pluginapi.HTTPStreamChunk, error)
}

type hostDoer struct {
	client pluginapi.HostHTTPClient
}

func (d hostDoer) do(ctx context.Context, method, url string, headers map[string]string, body []byte) (int, http.Header, []byte, error) {
	resp, err := d.client.Do(ctx, pluginapi.HTTPRequest{
		Method:  firstNonEmpty(strings.TrimSpace(method), http.MethodPost),
		URL:     url,
		Headers: stringHeaders(headers),
		Body:    body,
	})
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Headers, resp.Body, nil
}

func (d hostDoer) doStream(ctx context.Context, url string, headers map[string]string, body []byte) (int, http.Header, <-chan pluginapi.HTTPStreamChunk, error) {
	resp, err := d.client.DoStream(ctx, pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     url,
		Headers: stringHeaders(headers),
		Body:    body,
	})
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Headers, resp.Chunks, nil
}

type stdDoer struct {
	client *http.Client
}

func (d stdDoer) do(ctx context.Context, method, url string, headers map[string]string, body []byte) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, firstNonEmpty(strings.TrimSpace(method), http.MethodPost), url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header = stringHeaders(headers)
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, resp.Header, nil, err
	}
	return resp.StatusCode, resp.Header, respBody, nil
}

func (d stdDoer) doStream(ctx context.Context, url string, headers map[string]string, body []byte) (int, http.Header, <-chan pluginapi.HTTPStreamChunk, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header = stringHeaders(headers)
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		ch := make(chan pluginapi.HTTPStreamChunk, 1)
		if len(errBody) > 0 {
			ch <- pluginapi.HTTPStreamChunk{Payload: errBody}
		}
		close(ch)
		return resp.StatusCode, resp.Header, ch, nil
	}
	ch := make(chan pluginapi.HTTPStreamChunk)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		buf := make([]byte, 32*1024)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				select {
				case <-ctx.Done():
					return
				case ch <- pluginapi.HTTPStreamChunk{Payload: bytes.Clone(buf[:n])}:
				}
			}
			if readErr != nil {
				if !strings.Contains(readErr.Error(), "EOF") {
					select {
					case <-ctx.Done():
					case ch <- pluginapi.HTTPStreamChunk{Err: readErr}:
					}
				}
				return
			}
		}
	}()
	return resp.StatusCode, resp.Header, ch, nil
}

func stringHeaders(headers map[string]string) http.Header {
	out := make(http.Header, len(headers))
	for key, value := range headers {
		out.Set(key, value)
	}
	return out
}

// pool implements weighted multi-key selection with per-key proxy transports.
type pool struct {
	mu      sync.Mutex
	rnd     *rand.Rand
	clients map[string]*http.Client
}

func newPool() *pool {
	return &pool{
		rnd:     rand.New(rand.NewSource(time.Now().UnixNano())),
		clients: make(map[string]*http.Client),
	}
}

func poolRequestFromHTTP(req pluginapi.ExecutorHTTPRequest) pluginapi.ExecutorRequest {
	return pluginapi.ExecutorRequest{AuthAttributes: req.Attributes, AuthMetadata: req.Metadata}
}

func (p *pool) order(members []APIKeyEntry) []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	remaining := make([]int, len(members))
	for i := range remaining {
		remaining[i] = i
	}
	out := make([]int, 0, len(members))
	for len(remaining) > 0 {
		total := 0
		for _, idx := range remaining {
			total += members[idx].normalizedWeight()
		}
		pick := p.rnd.Intn(total)
		at := 0
		for i, idx := range remaining {
			pick -= members[idx].normalizedWeight()
			if pick < 0 {
				at = i
				break
			}
		}
		out = append(out, remaining[at])
		remaining = append(remaining[:at], remaining[at+1:]...)
	}
	return out
}

func (p *pool) clientFor(idx int, member APIKeyEntry, hostClient pluginapi.HostHTTPClient) (doer, error) {
	if strings.TrimSpace(member.ProxyURL) == "" {
		if hostClient == nil {
			return nil, fmt.Errorf("commandcode executor: host HTTP client is required")
		}
		return hostDoer{client: hostClient}, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	cacheKey := strconv.Itoa(idx) + "\x00" + strings.TrimSpace(member.ProxyURL)
	if client, ok := p.clients[cacheKey]; ok {
		return stdDoer{client: client}, nil
	}
	transport, err := proxyTransport(strings.TrimSpace(member.ProxyURL))
	if err != nil {
		return nil, err
	}
	client := &http.Client{Transport: transport, Timeout: 0}
	p.clients[cacheKey] = client
	return stdDoer{client: client}, nil
}

func proxyTransport(proxyURL string) (http.RoundTripper, error) {
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("commandcode executor: invalid proxy_url %q: %w", proxyURL, err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return &http.Transport{
			Proxy:           http.ProxyURL(parsed),
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			DialContext:     (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		}, nil
	case "socks5", "socks5h":
		dialer, err := proxy.FromURL(parsed, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("commandcode executor: invalid socks proxy %q: %w", proxyURL, err)
		}
		return &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		}, nil
	case "":
		return nil, fmt.Errorf("commandcode executor: proxy_url %q missing scheme", proxyURL)
	default:
		return nil, fmt.Errorf("commandcode executor: unsupported proxy scheme %q", parsed.Scheme)
	}
}

func retryable(status int, err error) bool {
	if err != nil {
		return true
	}
	return status == 401 || status == 429 || (status >= 500 && status <= 599)
}
