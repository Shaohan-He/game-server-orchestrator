package drainer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type sessionResponse struct {
	ActiveSessions int32 `json:"activeSessions"`
}

type SessionTracker struct {
	client *http.Client
}

func NewSessionTracker(timeout time.Duration) *SessionTracker {
	return &SessionTracker{
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// ActiveSessions queries a game server's session endpoint and returns the active session count.
func (t *SessionTracker) ActiveSessions(ctx context.Context, endpoint string) (int32, error) {
	url := fmt.Sprintf("http://%s/api/v1/sessions", endpoint)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("query sessions: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var sr sessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}

	return sr.ActiveSessions, nil
}

var _ SessionQuerier = (*SessionTracker)(nil)
