package panel

import (
	"fmt"
	"github.com/go-resty/resty/v2"
	"net/url"
)

// Debug set the client debug for client
func (c *Client) Debug() {
	c.client.SetDebug(true)
}

func (c *Client) assembleURL(path string) string {
	base, err := url.Parse(c.APIHost)
	if err != nil {
		return c.APIHost + path
	}
	ref, err := url.Parse(path)
	if err != nil {
		return c.APIHost + path
	}
	return base.ResolveReference(ref).String()
}
func (c *Client) checkResponse(res *resty.Response, path string, err error) error {
	if err != nil {
		return fmt.Errorf("request %s failed: %s", c.assembleURL(path), err)
	}
	if res.StatusCode() >= 400 {
		body := res.Body()
		return fmt.Errorf("request %s failed: %s", c.assembleURL(path), string(body))
	}
	return nil
}
