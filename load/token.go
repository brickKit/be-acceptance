package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

// credentials are a user's, for probes whose routes need a token (PROBE_GUARD): the identity member's development
// exchange (A101) gives a short token, refreshed well before it ends.
type credentials struct {
	TokenURL string `json:"token_url"` // e.g. http://127.0.0.1:18200/api/iam/dev/token
	Username string `json:"username"`
	Password string `json:"password"`
}

// RefreshEvery is how often the token is renewed: the identity member's tokens live 5 minutes.
const RefreshEvery = 2 * time.Minute

// bearer is the Authorization header every request carries; empty when the probes are not guarded.
var bearer atomic.Value

func init() { bearer.Store("") }

func fetchToken(ctx context.Context, client *http.Client, c credentials) (string, error) {
	body, _ := json.Marshal(map[string]string{"username": c.Username, "password": c.Password})
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != 200 || out.AccessToken == "" {
		return "", fmt.Errorf("the token exchange answered %d", resp.StatusCode)
	}
	return out.AccessToken, nil
}

// useCredentials reads the file, fetches a first token (failing the run if it cannot) and keeps it fresh.
func useCredentials(ctx context.Context, client *http.Client, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var c credentials
	if err := json.Unmarshal(raw, &c); err != nil {
		return err
	}
	tok, err := fetchToken(ctx, client, c)
	if err != nil {
		return err
	}
	bearer.Store("Bearer " + tok)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(RefreshEvery):
			}
			if tok, err := fetchToken(ctx, client, c); err == nil {
				bearer.Store("Bearer " + tok)
			} else {
				fmt.Fprintln(os.Stderr, "token refresh failed; keeping the current token:", err)
			}
		}
	}()
	return nil
}
