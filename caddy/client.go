package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type Client struct {
	addr   string
	domain string
	hc     *http.Client
}

func NewClient(addr, domain string) *Client {
	return &Client{
		addr:   addr,
		domain: domain,
		hc:     &http.Client{},
	}
}

func (c *Client) DiscoverServer(ctx context.Context) (string, error) {
	endpoint := c.addr + "/config/apps/http/servers"
	servers, err := c.getMap(ctx, endpoint)
	if err != nil {
		return "", err
	}
	for name := range servers {
		routesURL := fmt.Sprintf("%s/config/apps/http/servers/%s/routes", c.addr, name)
		routes, err := c.getSlice(ctx, routesURL)
		if err != nil {
			continue
		}
		for _, r := range routes {
			route, ok := r.(map[string]interface{})
			if !ok {
				continue
			}
			matchers, ok := route["match"].([]interface{})
			if !ok {
				continue
			}
			for _, m := range matchers {
				matcher, ok := m.(map[string]interface{})
				if !ok {
					continue
				}
				hosts, ok := matcher["host"].([]interface{})
				if !ok {
					continue
				}
				for _, h := range hosts {
					host, ok := h.(string)
					if !ok {
						continue
					}
					if strings.HasSuffix(host, "."+c.domain) {
						return name, nil
					}
				}
			}
		}
	}
	return "", fmt.Errorf("no server with .%s host matcher found", c.domain)
}

func (c *Client) AddRoute(ctx context.Context, srvName string, port int) error {
	host := fmt.Sprintf("%d.%s", port, c.domain)
	route := map[string]interface{}{
		"match": []interface{}{
			map[string]interface{}{
				"host": []interface{}{host},
			},
		},
		"handle": []interface{}{
			map[string]interface{}{
				"handler":   "reverse_proxy",
				"upstreams": []interface{}{
					map[string]interface{}{"dial": fmt.Sprintf("localhost:%d", port)},
				},
			},
		},
		"@id": fmt.Sprintf("cadport-%d", port),
	}
	body, err := json.Marshal(route)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/config/apps/http/servers/%s/routes/0", c.addr, srvName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("caddy add route: %s: %s", resp.Status, string(b))
	}
	return nil
}

func (c *Client) RemoveRoute(ctx context.Context, srvName string, port int) error {
	return c.deleteByID(ctx, fmt.Sprintf("cadport-%d", port))
}

func (c *Client) RemoveAllRoutes(ctx context.Context, srvName string) ([]string, error) {
	endpoint := fmt.Sprintf("%s/config/apps/http/servers/%s/routes", c.addr, srvName)
	routes, err := c.getSlice(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, r := range routes {
		route, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		id, ok := route["@id"].(string)
		if !ok || !strings.HasPrefix(id, "cadport-") {
			continue
		}
		if err := c.deleteByID(ctx, id); err != nil {
			return removed, err
		}
		removed = append(removed, id)
	}
	return removed, nil
}

func (c *Client) deleteByID(ctx context.Context, id string) error {
	delURL := fmt.Sprintf("%s/id/%s", c.addr, url.PathEscape(id))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, delURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != 404 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("caddy delete %s: %s: %s", id, resp.Status, string(b))
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *Client) getMap(ctx context.Context, url string) (map[string]interface{}, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: %s", resp.Status, string(b))
	}
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) getSlice(ctx context.Context, url string) ([]interface{}, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: %s", resp.Status, string(b))
	}
	var result []interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}