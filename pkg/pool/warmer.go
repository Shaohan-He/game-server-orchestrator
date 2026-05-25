package pool

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type Warmer struct {
	client         *http.Client
	healthEndpoint string
	retryMax       int
	retryBackoff   time.Duration
}

func NewWarmer(healthPath string, timeout time.Duration) *Warmer {
	return &Warmer{
		client: &http.Client{
			Timeout: timeout,
		},
		healthEndpoint: healthPath,
		retryMax:       30,
		retryBackoff:   2 * time.Second,
	}
}

// WaitUntilReady polls the game server's health check endpoint until it returns 200
// or the retry budget is exhausted.
func (w *Warmer) WaitUntilReady(ctx context.Context, endpoint string) error {
	url := fmt.Sprintf("http://%s%s", endpoint, w.healthEndpoint)

	for i := 0; i < w.retryMax; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("build health check request: %w", err)
		}

		resp, err := w.client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			return nil
		}
		if resp != nil {
			_ = resp.Body.Close()
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(w.retryBackoff):
		}
	}

	return fmt.Errorf("server %s did not become ready within %d retries", endpoint, w.retryMax)
}
