package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 35 * time.Second}
}

func (c Client) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		value, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("relay returned %s: %s", response.Status, strings.TrimSpace(string(value)))
	}
	if output != nil {
		return json.NewDecoder(response.Body).Decode(output)
	}
	return nil
}

func (c Client) Verify(ctx context.Context) (Status, error) {
	var value Status
	err := c.request(ctx, http.MethodGet, "/api/v1/status", nil, &value)
	return value, err
}
func (c Client) CreateHook(ctx context.Context, name string) (Hook, error) {
	var value Hook
	err := c.request(ctx, http.MethodPost, "/api/v1/hooks", hookRequest{Name: name}, &value)
	return value, err
}
func (c Client) DeleteHook(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodDelete, "/api/v1/hooks/"+url.PathEscape(id), nil, nil)
}

func (c Client) Next(ctx context.Context) (*Delivery, error) {
	var value Delivery
	err := c.request(ctx, http.MethodGet, "/api/v1/deliveries/next", nil, &value)
	if err != nil {
		return nil, err
	}
	if value.ID == "" {
		return nil, nil
	}
	return &value, nil
}

func (c Client) Acknowledge(ctx context.Context, delivery Delivery, disposition, detail string) error {
	if delivery.ID == "" || delivery.LeaseToken == "" {
		return errors.New("delivery and lease token are required")
	}
	return c.request(ctx, http.MethodPost, "/api/v1/deliveries/"+url.PathEscape(delivery.ID)+"/ack", ackRequest{LeaseToken: delivery.LeaseToken, Disposition: disposition, Error: detail}, nil)
}
