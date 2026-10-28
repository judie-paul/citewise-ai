package httpx

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNetworkErrorDoesNotLeakURL(t *testing.T) {
	c := New("OpenAlex", time.Second)
	c.Retries = 0
	var out map[string]any
	err := c.Do(context.Background(), func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:1/works?api_key=supersecret", nil)
	}, &out)
	if err == nil {
		t.Fatal("expected connection error")
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("error leaks API key: %v", err)
	}
}
