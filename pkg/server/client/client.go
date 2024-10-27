package serverclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/autobrr/distribrr/pkg/version"

	"github.com/rs/xid"
)

const DefaultClientTimeout = 15 * time.Second

type Client struct {
	http *http.Client

	baseUrl string
	token   string
}

func NewClient(addr, token string) *Client {
	return &Client{
		baseUrl: addr,
		token:   token,
		http: &http.Client{
			Timeout: DefaultClientTimeout,
		},
	}
}

type JoinRequest struct {
	NodeName   string            `json:"node_name"`
	ClientAddr string            `json:"client_addr"`
	Labels     map[string]string `yaml:"labels"`
}

type JoinResponse struct {
	NodeName string `json:"node_name"`
}

func (c *Client) JoinRequest(ctx context.Context, joinReq JoinRequest) error {
	reqUrl, err := c.buildUrl("/node/register", nil)
	if err != nil {
		return err
	}

	body, err := json.Marshal(joinReq)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqUrl.String(), bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	c.setHeaders(ctx, req)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("bad status: %d", resp.StatusCode)
	}

	return nil
}

type DeregisterRequest struct {
	NodeName   string `json:"node_name"`
	ClientAddr string `json:"client_addr"`
}

func (c *Client) DeregisterRequest(ctx context.Context, data DeregisterRequest) error {
	reqUrl, err := c.buildUrl("/node/deregister", nil)
	if err != nil {
		return err
	}

	body, err := json.Marshal(data)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqUrl.String(), bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	c.setHeaders(ctx, req)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	return nil
}

func (c *Client) setHeaders(ctx context.Context, req *http.Request) {
	req.Header.Add("Authorization", c.token)
	req.Header.Add("User-Agent", "distribrr-client-"+version.Version)

	if ctx != nil {
		if id := ctx.Value("correlation_id"); id != nil {
			if id == "" {
				id = xid.New().String()
			}
			req.Header.Add("X-Correlation-ID", id.(string))
		}
	}
}

func (c *Client) buildUrl(endpoint string, params map[string]string) (*url.URL, error) {
	apiBase := "/api/v1/"

	// add query params
	queryParams := url.Values{}
	for key, value := range params {
		queryParams.Add(key, value)
	}

	joinedUrl, err := url.JoinPath(c.baseUrl, apiBase, endpoint)
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
