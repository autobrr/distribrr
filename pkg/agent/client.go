package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/autobrr/distribrr/pkg/middleware"
	"github.com/autobrr/distribrr/pkg/stats"
	"github.com/autobrr/distribrr/pkg/task"
	"github.com/autobrr/distribrr/pkg/version"

	"github.com/pkg/errors"
)

const DefaultTimeout = 15 * time.Second

type Client struct {
	name  string
	addr  string
	token string

	http *http.Client
	// taskHTTP has no timeout of its own: an agent can take much longer than
	// DefaultTimeout to fetch the .torrent and add it, so the caller's ctx
	// bounds StartTask instead.
	taskHTTP *http.Client
}

func NewClient(addr, name, token string) *Client {
	return &Client{
		addr:  addr,
		name:  name,
		token: token,
		http: &http.Client{
			Timeout: DefaultTimeout,
		},
		taskHTTP: &http.Client{},
	}
}

func (c *Client) HealthCheck(ctx context.Context) error {
	reqUrl, err := c.buildUrl(c.addr, "healthz/readiness", nil)
	if err != nil {
		return errors.Wrapf(err, "could not build URL: %s", c.name)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqUrl.String(), nil)
	if err != nil {
		return errors.Wrapf(err, "could not create request for node: %s", c.name)
	}

	c.setHeaders(ctx, req)

	resp, err := c.http.Do(req)
	if err != nil {
		return errors.Wrapf(err, "error during request for node: %s", c.name)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.statusError(resp)
	}

	return nil
}

func (c *Client) GetLabels(ctx context.Context) (map[string]string, error) {
	return c.getLabels(ctx)
}

func (c *Client) getLabels(ctx context.Context) (map[string]string, error) {
	reqUrl, err := c.buildUrl(c.addr, "labels", nil)
	if err != nil {
		return nil, errors.Wrapf(err, "could not build URL: %s", c.name)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqUrl.String(), nil)
	if err != nil {
		return nil, errors.Wrapf(err, "could not create request for node: %s", c.name)
	}

	c.setHeaders(ctx, req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errors.Wrapf(err, "error during request for node: %s", c.name)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.statusError(resp)
	}

	var data map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	return data, nil
}

func (c *Client) GetStats(ctx context.Context) (*stats.Stats, error) {
	return c.getStats(ctx)
}

func (c *Client) getStats(ctx context.Context) (*stats.Stats, error) {
	reqUrl, err := c.buildUrl(c.addr, "stats", nil)
	if err != nil {
		return nil, errors.Wrapf(err, "could not build URL: %s", c.name)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqUrl.String(), nil)
	if err != nil {
		return nil, errors.Wrapf(err, "could not create request for node: %s", c.name)
	}

	c.setHeaders(ctx, req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errors.Wrapf(err, "error during request for node: %s", c.name)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.statusError(resp)
	}

	var s stats.Stats
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, err
	}

	return &s, nil
}

func (c *Client) VerifyToken(ctx context.Context) error {
	return c.verifyToken(ctx)
}

func (c *Client) verifyToken(ctx context.Context) error {
	reqUrl, err := c.buildUrl(c.addr, "verify", nil)
	if err != nil {
		return errors.Wrapf(err, "could not build URL: %s", c.name)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqUrl.String(), nil)
	if err != nil {
		return errors.Wrapf(err, "could not create request for node: %s", c.name)
	}

	c.setHeaders(ctx, req)

	resp, err := c.http.Do(req)
	if err != nil {
		return errors.Wrapf(err, "error during request for node: %s", c.name)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.statusError(resp)
	}

	return nil
}

func (c *Client) StartTask(ctx context.Context, te *task.Event) error {
	return c.startTask(ctx, te)
}

func (c *Client) startTask(ctx context.Context, te *task.Event) error {
	reqUrl, err := c.buildUrl(c.addr, "tasks", nil)
	if err != nil {
		return errors.Wrapf(err, "could not build URL: %s", c.name)
	}

	body, err := json.Marshal(te)
	if err != nil {
		return errors.Wrapf(err, "could not marshal request for node: %s", c.name)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqUrl.String(), bytes.NewBuffer(body))
	if err != nil {
		return errors.Wrapf(err, "could not create request for node: %s", c.name)
	}

	c.setHeaders(ctx, req)

	resp, err := c.taskHTTP.Do(req)
	if err != nil {
		return errors.Wrapf(err, "error during request for node: %s", c.name)
	}

	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.statusError(resp)
	}

	return nil
}

// statusError reads the error the agent sent with an unexpected status.
func (c *Client) statusError(resp *http.Response) error {
	if msg := errorBody(resp); msg != "" {
		return fmt.Errorf("node: %s unexpected status: %d: %s", c.name, resp.StatusCode, msg)
	}

	return fmt.Errorf("node: %s unexpected status: %d", c.name, resp.StatusCode)
}

// errorBody returns the message of an error response: the "error" field of a
// JSON body, or else the body itself, capped at 4 KiB.
func errorBody(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Error != "" {
		return e.Error
	}

	return string(bytes.TrimSpace(body))
}

func (c *Client) setHeaders(ctx context.Context, req *http.Request) {
	req.Header.Add("Authorization", c.token)
	req.Header.Add("User-Agent", "distribrr-server-"+version.Version)

	if id := middleware.CorrelationIDFromContext(ctx); id != "" {
		req.Header.Add(middleware.CorrelationIDHeader, id)
	}
}

func (c *Client) buildUrl(addr string, endpoint string, params map[string]string) (*url.URL, error) {
	apiBase := "/api/v1/"

	// add query params
	queryParams := url.Values{}
	for key, value := range params {
		queryParams.Add(key, value)
	}

	joinedUrl, err := url.JoinPath(addr, apiBase, endpoint)
	if err != nil {
		return nil, err
	}

	parsedUrl, err := url.Parse(joinedUrl)
	if err != nil {
		return nil, err
	}

	parsedUrl.RawQuery = queryParams.Encode()

	return parsedUrl, nil
}
