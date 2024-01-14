package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/autobrr/distribrr/pkg/version"
	"github.com/rs/xid"
	"net/http"
	"net/url"
	"time"

	"github.com/autobrr/distribrr/pkg/stats"
	"github.com/autobrr/distribrr/pkg/task"

	"github.com/pkg/errors"
)

const DefaultTimeout = 15 * time.Second

type Client struct {
	name  string
	addr  string
	token string

	http *http.Client
}

func NewClient(addr, name, token string) *Client {
	return &Client{
		addr:  addr,
		name:  name,
		token: token,
		http: &http.Client{
			Timeout: DefaultTimeout,
		},
	}
}

func (c *Client) HealthCheck(ctx context.Context) error {
	// TODO ping clients
	return nil
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
		return nil, fmt.Errorf("node: %s unexpected status: %d", c.name, resp.StatusCode)
	}

	var s stats.Stats
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, err
	}

	return &s, nil
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

	resp, err := c.http.Do(req)
	if err != nil {
		return errors.Wrapf(err, "error during request for node: %s", c.name)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("node: %s unexpected status: %d", c.name, resp.StatusCode)
	}

	return nil
}

func (c *Client) setHeaders(ctx context.Context, req *http.Request) {
	req.Header.Add("Authorization", c.token)
	req.Header.Add("User-Agent", "distribrr-server-"+version.Version)

	if ctx != nil {
		if id := ctx.Value("correlation_id"); id != nil {
			if id == "" {
				id = xid.New().String()
			}
			req.Header.Add("X-Correlation-ID", id.(string))
		}
	}
}

type DownloadRequest struct {
	DownloadUrl string            `json:"download_url"`
	Filename    string            `json:"filename"`
	InfoHash    string            `json:"info_hash"`
	Category    string            `json:"category"`
	Tags        string            `json:"tags"`
	Mode        string            `json:"mode,omitempty"`
	Opts        map[string]string `json:"opts"`
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
