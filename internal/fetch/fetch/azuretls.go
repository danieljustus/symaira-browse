package fetch

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	azuretls "github.com/Noooste/azuretls-client"
)

const maxProxySessions = 10

// azureClient uses azuretls for browser-impersonating TLS+HTTP/2.
type azureClient struct {
	session       *azuretls.Session
	opts          *clientOptions
	profile       Profile
	proxySessions map[string]*azuretls.Session
	proxyOrder    []string
	proxyMu       sync.Mutex
}

// azuretlsBrowser maps a Profile to an azuretls browser constant.
// For ProfileRandom, a verified preset is randomly selected.
func azuretlsBrowser(p Profile) string {
	// Resolve random once — the caller stores the resolved profile.
	switch p {
	case ProfileFirefox:
		return azuretls.Firefox
	case ProfileOpera:
		return azuretls.Opera
	case ProfileSafari:
		return azuretls.Safari
	case ProfileEdge:
		return azuretls.Edge
	case ProfileIos:
		return azuretls.Ios
	case ProfileChrome:
		return azuretls.Chrome
	default:
		return azuretls.Chrome
	}
}

func newAzureClient(p Profile, o *clientOptions) (*azureClient, error) {
	// Resolve random at construction time so the same client uses a
	// consistent preset for its lifetime ("per session").
	if p == ProfileRandom {
		p = randomProfile()
	}

	browser := azuretlsBrowser(p)
	sess := azuretls.NewSession()
	sess.Browser = browser

	if o.proxy != "" {
		if err := sess.SetProxy(o.proxy); err != nil {
			return nil, fmt.Errorf("invalid proxy: %w", err)
		}
	}

	return &azureClient{
		session:       sess,
		opts:          o,
		profile:       p,
		proxySessions: make(map[string]*azuretls.Session),
	}, nil
}

func (c *azureClient) Fetch(ctx context.Context, req Request) (*Response, error) {
	if err := CheckAllowlist(req.URL, req.Allowlist); err != nil {
		return nil, err
	}
	if !req.AllowPrivate {
		if err := CheckSSRF(req.URL); err != nil {
			return nil, err
		}
	}

	method := req.Method
	if method == "" {
		method = "GET"
	}

	timeout := time.Duration(c.opts.timeoutSeconds) * time.Second
	if req.Timeout > 0 {
		timeout = req.Timeout
	}

	maxBody := int64(c.opts.maxBodyMB) * 1024 * 1024
	if req.MaxBody > 0 {
		maxBody = req.MaxBody
	}

	azReq := &azuretls.Request{
		Method:  method,
		Url:     req.URL,
		TimeOut: timeout,
	}
	if len(req.Body) > 0 {
		azReq.Body = req.Body
	}

	if len(req.Headers) > 0 {
		oh := make(azuretls.OrderedHeaders, 0, len(req.Headers))
		for k, v := range req.Headers {
			oh = append(oh, []string{k, v})
		}
		azReq.OrderedHeaders = oh
	}

	if req.Proxy != "" {
		return c.fetchWithProxy(ctx, req, method, timeout, maxBody)
	}

	return c.doFetchWithRetry(ctx, req, azReq, maxBody)
}

func (c *azureClient) fetchWithProxy(ctx context.Context, req Request, method string, timeout time.Duration, maxBody int64) (*Response, error) {
	sess := c.getProxySession(req.Proxy)

	azReq := &azuretls.Request{
		Method:  method,
		Url:     req.URL,
		TimeOut: timeout,
	}
	if len(req.Body) > 0 {
		azReq.Body = req.Body
	}

	start := time.Now()
	azResp, err := sess.Do(azReq)
	elapsed := time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", req.URL, err)
	}

	if int64(len(azResp.Body)) > maxBody {
		return nil, &ErrTooLarge{URL: req.URL, Limit: maxBody}
	}

	resp := processResponse(req.URL, azResp, elapsed, "HTTP/2.0")
	if err := CheckAllowlist(resp.FinalURL, req.Allowlist); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *azureClient) getProxySession(proxyURL string) *azuretls.Session {
	c.proxyMu.Lock()
	defer c.proxyMu.Unlock()

	if sess, ok := c.proxySessions[proxyURL]; ok {
		return sess
	}

	if len(c.proxySessions) >= maxProxySessions {
		oldest := c.proxyOrder[0]
		c.proxyOrder = c.proxyOrder[1:]
		if sess, ok := c.proxySessions[oldest]; ok {
			sess.Close()
			delete(c.proxySessions, oldest)
		}
	}

	browser := azuretlsBrowser(c.profile)
	sess := azuretls.NewSession()
	sess.Browser = browser
	if err := sess.SetProxy(proxyURL); err != nil {
		return nil
	}
	c.proxySessions[proxyURL] = sess
	c.proxyOrder = append(c.proxyOrder, proxyURL)
	return sess
}

func (c *azureClient) Close() error {
	c.session.Close()
	c.proxyMu.Lock()
	for _, sess := range c.proxySessions {
		sess.Close()
	}
	c.proxySessions = make(map[string]*azuretls.Session)
	c.proxyOrder = nil
	c.proxyMu.Unlock()
	return nil
}

func (c *azureClient) doFetchWithRetry(ctx context.Context, req Request, azReq *azuretls.Request, maxBody int64) (*Response, error) {
	var lastErr error
	maxRetries := 0
	if c.opts.enableRetry {
		maxRetries = c.opts.backoffConfig.MaxRetries
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if c.opts.rateLimiter != nil && !c.opts.rateLimiter.Allow(req.URL) {
			return nil, fmt.Errorf("circuit breaker open for %s", extractHost(req.URL))
		}

		start := time.Now()
		azResp, err := c.session.Do(azReq)
		elapsed := time.Since(start)

		if err == nil && int64(len(azResp.Body)) > maxBody {
			err = &ErrTooLarge{URL: req.URL, Limit: maxBody}
		}

		if err == nil {
			proto := detectProto(azResp)
			resp := processResponse(req.URL, azResp, elapsed, proto)

			if err := CheckAllowlist(resp.FinalURL, req.Allowlist); err != nil {
				return nil, err
			}
			if !req.AllowPrivate && resp.FinalURL != req.URL {
				if ssrfErr := CheckSSRF(resp.FinalURL); ssrfErr != nil {
					return nil, ssrfErr
				}
			}

			if c.opts.rateLimiter != nil {
				c.opts.rateLimiter.RecordSuccess(req.URL)
			}
			return resp, nil
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
		case <-time.After(delay):
		}
	}

	return nil, lastErr
}

func detectProto(azResp *azuretls.Response) string {
	if azResp.HttpResponse != nil {
		if strings.HasPrefix(azResp.HttpResponse.Proto, "HTTP/2") {
			return "HTTP/2.0"
		} else if strings.HasPrefix(azResp.HttpResponse.Proto, "HTTP/3") {
			return "HTTP/3.0"
		}
	}
	return "HTTP/1.1"
}

func processResponse(rawURL string, azResp *azuretls.Response, elapsed time.Duration, proto string) *Response {
	body := azResp.Body

	ct := azResp.Header.Get("Content-Type")
	body, _ = normaliseCharset(body, ct)

	headers := make(map[string][]string)
	for k, v := range azResp.Header {
		headers[k] = v
	}

	finalURL := rawURL
	if azResp.Request != nil {
		finalURL = azResp.Request.Url
	}

	return &Response{
		FinalURL:    finalURL,
		StatusCode:  azResp.StatusCode,
		Headers:     headers,
		Body:        body,
		Protocol:    proto,
		ContentType: ct,
		Elapsed:     elapsed,
	}
}
