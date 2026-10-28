// Package httpx provides a small JSON HTTP helper with timeouts and retries
// shared by the external API clients.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// MaxBody caps how much of a response body is read.
const MaxBody = 8 << 20

// StatusError is returned for non-2xx responses.
type StatusError struct {
	Service string
	Code    int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s returned HTTP %d", e.Service, e.Code)
}

// Retryable reports whether a status code is worth retrying.
func Retryable(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// Client performs JSON requests with retries.
type Client struct {
	HTTP     *http.Client
	Service  string
	Retries  int           // additional attempts after the first
	Backoff  time.Duration // base delay, multiplied by attempt number
	MaxBytes int64
}

// New returns a Client with sensible defaults.
func New(service string, timeout time.Duration) *Client {
	return &Client{
		HTTP:     &http.Client{Timeout: timeout},
		Service:  service,
		Retries:  2,
		Backoff:  500 * time.Millisecond,
		MaxBytes: MaxBody,
	}
}

// Do sends the request built by newReq and decodes a JSON response into out.
// newReq is called for every attempt so request bodies can be recreated.
func (c *Client) Do(ctx context.Context, newReq func(context.Context) (*http.Request, error), out any) error {
	var lastErr error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.Backoff * time.Duration(attempt)):
			}
		}
		req, err := newReq(ctx)
		if err != nil {
			return err
		}
		retry, err := c.once(req, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry || ctx.Err() != nil {
			break
		}
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%s request cancelled: %w", c.Service, ctx.Err())
	}
	return lastErr
}

func (c *Client) once(req *http.Request, out any) (retry bool, err error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// Network errors are retryable unless the context ended.
		return !errors.Is(err, context.Canceled), fmt.Errorf("%s request failed: %w", c.Service, err)
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, c.MaxBytes)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		io.Copy(io.Discard, body)
		return Retryable(resp.StatusCode), &StatusError{Service: c.Service, Code: resp.StatusCode}
	}
	if err := json.NewDecoder(body).Decode(out); err != nil {
		return false, fmt.Errorf("%s returned an invalid response: %w", c.Service, err)
	}
	return false, nil
}
