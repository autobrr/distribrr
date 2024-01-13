package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

type Client struct {
}

func NewClient() *Client {
	return &Client{}
}

func (c *Client) RegisterWorker() error {
	return nil
}

type JoinRequest struct {
	NodeName   string `json:"node_name"`
	ClientAddr string `json:"client_addr"`
}

type JoinResponse struct {
	NodeName string `json:"node_name"`
}

func (c *Client) joinRequest(ctx context.Context, addr string, token string, joinReq JoinRequest) error {
	reqUrl := c.buildUrl(addr, "register", nil)

	fmt.Printf("url: %s\n", reqUrl)

	body, err := json.Marshal(joinReq)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqUrl, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	req.Header.Add("Authorization", token)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("bad status: %d", resp.StatusCode)
	}

	return nil
}

func (c *Client) buildUrl(addr string, endpoint string, params map[string]string) string {
	apiBase := "/api/v1/"

	// add query params
	queryParams := url.Values{}
	for key, value := range params {
		queryParams.Add(key, value)
	}

	joinedUrl, _ := url.JoinPath(addr, apiBase, endpoint)
	parsedUrl, _ := url.Parse(joinedUrl)
	parsedUrl.RawQuery = queryParams.Encode()

	// make into new string and return
	return parsedUrl.String()
}
