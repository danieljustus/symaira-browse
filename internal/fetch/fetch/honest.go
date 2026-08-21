package fetch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const honestUA = "symfetch/0.1 (+https://github.com/danieljustus/symaira-fetch)"

// honestClient uses stdlib net/http with a plain user-agent.
type honestClient struct {
	hcSafe       *http.Client // transport with ControlSSRF dial guard + SSRF CheckRedirect
	hcUnsafe     *http.Client // transport without SSRF dial guard (for AllowPrivate=true)
	opts         *clientOptions
	proxyClients map[string]*http.Client
	proxyOrder   []string
	proxyMu      sync.Mutex
}

func newHonestClient(o *clientOptions) (*honestClient, error) {
	safeTransport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Control: ControlSSRF,
		}).DialContext,
	}
	unsafeTransport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}

	if o.proxy != "" {
		proxyURL, err := url.Parse(o.proxy)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy: %w", err)
		}
		safeTransport.Proxy = http.ProxyURL(proxyURL)
		unsafeTransport.Proxy = http.ProxyURL(proxyURL)
	}

	safeRedirect := func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		if err := CheckSSRF(req.URL.String()); err != nil {
			return err
		}
		return nil
	}
	unsafeRedirect := func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}

	hcSafe := &http.Client{Transport: safeTransport, CheckRedirect: safeRedirect}
	hcUnsafe := &http.Client{Transport: unsafeTransport, CheckRedirect: unsafeRedirect}

	return &honestClient{
		hcSafe:       hcSafe,
		hcUnsafe:     hcUnsafe,
		opts:         o,
		proxyClients: make(map[string]*http.Client),
	}, nil
}

func (c *honestClient) Fetch(ctx context.Context, req Request) (*Response, error) {
	if !req.AllowPrivate {
		if err := CheckSSRF(req.URL); err != nil {
			return nil, err
		}
	}

	method := req.Method
	if method == "" {
		method = http.MethodGet
	}

	timeout := time.Duration(c.opts.timeoutSeconds) * time.Second
	if req.Timeout > 0 {
		timeout = req.Timeout
	}

	maxBody := int64(c.opts.maxBodyMB) * 1024 * 1024
	if req.MaxBody > 0 {
		maxBody = req.MaxBody
	}

	var bodyReader io.Reader
	if len(req.Body) > 0 {
		bodyReader = bytes.NewReader(req.Body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	httpReq.Header.Set("User-Agent", honestUA)
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("Accept-Language", "en-US,en;q=0.5")

	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpReq = httpReq.WithContext(timeoutCtx)

	hc := c.hcUnsafe
	if req.Proxy != "" {
		hc = c.getProxyClient(req.Proxy, req.AllowPrivate)
	} else if !req.AllowPrivate {
		hc = c.hcSafe
	}

	return c.doFetchWithRetry(ctx, req, hc, httpReq, maxBody)
}

func (c *honestClient) getProxyClient(proxyURL string, allowPrivate bool) *http.Client {
	c.proxyMu.Lock()
	defer c.proxyMu.Unlock()

	key := proxyURL
	if allowPrivate {
		key = proxyURL + "?allow-private"
	}
	if client, ok := c.proxyClients[key]; ok {
		return client
	}

	if len(c.proxyClients) >= maxProxySessions {
		oldest := c.proxyOrder[0]
		c.proxyOrder = c.proxyOrder[1:]
		if client, ok := c.proxyClients[oldest]; ok {
			client.CloseIdleConnections()
			delete(c.proxyClients, oldest)
		}
	}

	parsed, err := url.Parse(proxyURL)
	if err != nil {
		if allowPrivate {
			return c.hcUnsafe
		}
		return c.hcSafe
	}

	transport := &http.Transport{Proxy: http.ProxyURL(parsed)}
	var redirectFn func(req *http.Request, via []*http.Request) error
	if allowPrivate {
		redirectFn = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		}
	} else {
		transport.DialContext = (&net.Dialer{
			Control: ControlSSRF,
		}).DialContext
		redirectFn = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if err := CheckSSRF(req.URL.String()); err != nil {
				return err
			}
			return nil
		}
	}
	client := &http.Client{Transport: transport, CheckRedirect: redirectFn}
	c.proxyClients[key] = client
	c.proxyOrder = append(c.proxyOrder, key)
	return client
}

func (c *honestClient) Close() error {
	c.hcSafe.CloseIdleConnections()
	c.hcUnsafe.CloseIdleConnections()
	c.proxyMu.Lock()
	for _, client := range c.proxyClients {
		client.CloseIdleConnections()
	}
	c.proxyClients = make(map[string]*http.Client)
	c.proxyOrder = nil
	c.proxyMu.Unlock()
	return nil
}

func (c *honestClient) doFetchWithRetry(ctx context.Context, req Request, hc *http.Client, httpReq *http.Request, maxBody int64) (*Response, error) {
	var lastErr error
	maxRetries := 0
	if c.opts.enableRetry {
		maxRetries = c.opts.backoffConfig.MaxRetries
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if c.opts.rateLimiter != nil && !c.opts.rateLimiter.Allow(req.URL) {
			return nil, fmt.Errorf("circuit breaker open for %s", extractHost(req.URL))
		}

		start := c.opts.clock.Now()
		resp, err := hc.Do(httpReq)
		elapsed := c.opts.clock.Now().Sub(start)

		if err == nil {
			if IsTransientError(resp.StatusCode, nil) && c.opts.enableRetry && attempt < maxRetries {
				retryAfter := ParseRetryAfter(resp.Header.Get("Retry-After"))
				delay := c.opts.backoffConfig.BackoffDelay(attempt)
				if retryAfter > delay {
					delay = retryAfter
				}
				if c.opts.rateLimiter != nil {
					c.opts.rateLimiter.RecordFailure(req.URL)
				}
				resp.Body.Close()
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-c.opts.clock.After(delay):
				}
				continue
			}

			defer resp.Body.Close()
			limited := io.LimitReader(resp.Body, maxBody+1)
			body, err := io.ReadAll(limited)
			if err != nil {
				return nil, fmt.Errorf("read body: %w", err)
			}
			if int64(len(body)) > maxBody {
				return nil, &ErrTooLarge{URL: req.URL, Limit: maxBody}
			}

			ct := resp.Header.Get("Content-Type")
			body, err = normaliseCharset(body, ct)
			if err != nil {
				body = bytes.ToValidUTF8(body, []byte("?"))
			}

			proto := "HTTP/1.1"
			if strings.HasPrefix(resp.Proto, "HTTP/2") {
				proto = "HTTP/2.0"
			}

			headers := make(map[string][]string)
			for k, v := range resp.Header {
				headers[k] = v
			}

			if c.opts.rateLimiter != nil {
				c.opts.rateLimiter.RecordSuccess(req.URL)
			}

			return &Response{
				FinalURL:    resp.Request.URL.String(),
				StatusCode:  resp.StatusCode,
				Headers:     headers,
				Body:        body,
				Protocol:    proto,
				ContentType: ct,
				Elapsed:     elapsed,
			}, nil
		}

		lastErr = err
		if c.opts.rateLimiter != nil {
			c.opts.rateLimiter.RecordFailure(req.URL)
		}

		if !c.opts.enableRetry || attempt >= maxRetries {
			break
		}

		delay := c.opts.backoffConfig.BackoffDelay(attempt)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.opts.clock.After(delay):
		}
	}

	return nil, fmt.Errorf("fetch %s: %w", req.URL, lastErr)
}
