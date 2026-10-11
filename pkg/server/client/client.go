package serverclient

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
	"github.com/autobrr/distribrr/pkg/version"

	"github.com/pkg/errors"
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
	Token      string            `json:"token"`
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
		return statusError(resp)
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
		return statusError(resp)
	}

	return nil
}

// ErrNodeNotFound is returned by CheckNode when the server does not know the node.
var ErrNodeNotFound = errors.New("node not found")

// CheckNode asks the server whether it knows the node named nodeName. It
// returns ErrNodeNotFound when it does not; a REMOVED node is still known.
func (c *Client) CheckNode(ctx context.Context, nodeName string) error {
	reqUrl, err := c.buildUrl("/node/"+nodeName, nil)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqUrl.String(), nil)
	if err != nil {
		return err
	}

	c.setHeaders(ctx, req)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return errors.Wrap(ErrNodeNotFound, errorBody(resp))
	default:
		return statusError(resp)
	}
}

// statusError reads the error the server sent with an unexpected status.
func statusError(resp *http.Response) error {
	if msg := errorBody(resp); msg != "" {
		return fmt.Errorf("unexpected status: %d: %s", resp.StatusCode, msg)
	}

	return fmt.Errorf("unexpected status: %d", resp.StatusCode)
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
	req.Header.Add("User-Agent", "distribrr-client-"+version.Version)

	if id := middleware.CorrelationIDFromContext(ctx); id != "" {
		req.Header.Add(middleware.CorrelationIDHeader, id)
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
