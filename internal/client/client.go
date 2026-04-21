package client

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/berryp/scpcli/internal/auth"
	"github.com/berryp/scpcli/internal/config"
)

type Client struct {
	cfg  config.Config
	http *http.Client
}

func (c *Client) SecretKey() string { return c.cfg.SecretKey }

func New(cfg config.Config) *Client {
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// BuildRequest constructs and signs a request without executing it.
func (c *Client) BuildRequest(method, endpoint string, pathParams, queryParams map[string]string, body []byte) (*http.Request, error) {
	path, err := substitutePathParams(endpoint, pathParams)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("projectId", c.cfg.ProjectID)
	for k, v := range queryParams {
		if v != "" {
			q.Set(k, v)
		}
	}
	fullURL := c.cfg.Host + path + "?" + q.Encode()

	method = strings.ToUpper(method)
	ts := time.Now().UnixMilli()
	sig := auth.Sign(method, fullURL, c.cfg.AccessKey, c.cfg.SecretKey, c.cfg.ProjectID, ts)

	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, fullURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-Cmp-AccessKey", c.cfg.AccessKey)
	req.Header.Set("X-Cmp-Signature", sig)
	req.Header.Set("X-Cmp-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Cmp-ClientType", auth.ClientType)
	req.Header.Set("X-Cmp-ProjectId", c.cfg.ProjectID)
	req.Header.Set("X-Cmp-Language", auth.Language)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// Do issues a signed HTTP request. method is uppercased automatically.
// pathParams values substitute {name} placeholders; queryParams become ?k=v.
// body may be nil for GET/DELETE.
func (c *Client) Do(method, endpoint string, pathParams, queryParams map[string]string, body []byte) ([]byte, error) {
	req, err := c.BuildRequest(method, endpoint, pathParams, queryParams, body)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request %s: %w", req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, req.URL.Path, string(respBody))
	}
	return respBody, nil
}

func (c *Client) Get(endpoint string, pathParams, queryParams map[string]string) ([]byte, error) {
	return c.Do("GET", endpoint, pathParams, queryParams, nil)
}

func substitutePathParams(path string, params map[string]string) (string, error) {
	for name, value := range params {
		placeholder := "{" + name + "}"
		if !strings.Contains(path, placeholder) {
			return "", fmt.Errorf("path %q has no placeholder for %s", path, name)
		}
		if value == "" {
			return "", fmt.Errorf("required path param %s is empty", name)
		}
		path = strings.ReplaceAll(path, placeholder, url.PathEscape(value))
	}
	return path, nil
}
