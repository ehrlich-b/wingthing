package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const maxAuthResponseBytes = 1 << 20

var authHTTPClient = &http.Client{Timeout: 15 * time.Second}

func decodeAuthResponse(body io.Reader, destination any) error {
	data, err := io.ReadAll(io.LimitReader(body, maxAuthResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxAuthResponseBytes {
		return fmt.Errorf("authentication response exceeds %d bytes", maxAuthResponseBytes)
	}
	return json.Unmarshal(data, destination)
}

type DeviceToken struct {
	Token     string `json:"token" yaml:"device_token"`
	ExpiresAt int64  `json:"expires_at" yaml:"expires_at"`
	IssuedAt  int64  `json:"issued_at" yaml:"issued_at"`
	DeviceID  string `json:"device_id" yaml:"device_id"`
	PublicKey string `json:"public_key,omitempty" yaml:"public_key,omitempty"`
}

type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type TokenResponse struct {
	Token       string `json:"token"`
	ExpiresAt   int64  `json:"expires_at"`
	Error       string `json:"error,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Email       string `json:"email,omitempty"`
	Provider    string `json:"provider,omitempty"`
}

// UserInfo represents the authenticated user's identity from the relay.
type UserInfo struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Provider    string `json:"provider"`
}

func RequestDeviceCode(baseURL, wingID string, publicKey ...string) (*DeviceCodeResponse, error) {
	payload := map[string]string{"wing_id": wingID}
	if len(publicKey) > 0 && publicKey[0] != "" {
		payload["public_key"] = publicKey[0]
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, baseURL+"/auth/device", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := authHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request device code: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code request failed: %s", resp.Status)
	}

	var dcr DeviceCodeResponse
	if err := decodeAuthResponse(resp.Body, &dcr); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &dcr, nil
}

func PollForToken(ctx context.Context, baseURL, deviceCode string, interval int) (*TokenResponse, error) {
	body, err := json.Marshal(map[string]string{"device_code": deviceCode})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/auth/token", bytes.NewReader(body))
			if err != nil {
				return nil, fmt.Errorf("build token poll request: %w", err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := authHTTPClient.Do(req)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, ctxErr
				}
				return nil, fmt.Errorf("poll for token: %w", err)
			}
			if resp.StatusCode != http.StatusOK {
				_ = resp.Body.Close()
				return nil, fmt.Errorf("poll for token failed: %s", resp.Status)
			}

			var tr TokenResponse
			decErr := decodeAuthResponse(resp.Body, &tr)
			if err := resp.Body.Close(); err != nil {
				return nil, fmt.Errorf("close token response: %w", err)
			}
			if decErr != nil {
				return nil, fmt.Errorf("decode response: %w", decErr)
			}

			switch tr.Error {
			case "authorization_pending":
				continue
			case "slow_down":
				ticker.Reset(time.Duration(interval*2) * time.Second)
				continue
			case "":
				return &tr, nil
			default:
				return nil, fmt.Errorf("token error: %s", tr.Error)
			}
		}
	}
}

// ValidateTokenRemote checks a device token against the relay's /auth/check endpoint.
// Returns nil on 200 (valid), ErrAuthFailed on 401, or a wrapped error for network failures.
func ValidateTokenRemote(baseURL, token string) error {
	_, err := FetchUserInfo(baseURL, token)
	return err
}

// FetchUserInfo calls /auth/check and returns the authenticated user's identity.
// Returns ErrAuthFailed on 401, or a wrapped error for network failures.
func FetchUserInfo(baseURL, token string) (*UserInfo, error) {
	req, err := http.NewRequest("GET", baseURL+"/auth/check", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := authHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("relay unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrAuthFailed
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %s", resp.Status)
	}
	var info UserInfo
	if err := decodeAuthResponse(resp.Body, &info); err != nil {
		return nil, fmt.Errorf("decode user info: %w", err)
	}
	return &info, nil
}

// ErrAuthFailed is returned when the relay rejects a token with 401.
var ErrAuthFailed = errors.New("authentication failed")

func RefreshToken(baseURL string, token DeviceToken) (*TokenResponse, error) {
	body, err := json.Marshal(token)
	if err != nil {
		return nil, fmt.Errorf("marshal token: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, baseURL+"/auth/refresh", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := authHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed: %s", resp.Status)
	}

	var tr TokenResponse
	if err := decodeAuthResponse(resp.Body, &tr); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &tr, nil
}
