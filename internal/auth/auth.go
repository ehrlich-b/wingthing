package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type DeviceToken struct {
	Token     string `json:"token" yaml:"device_token"`
	ExpiresAt int64  `json:"expires_at" yaml:"expires_at"`
	IssuedAt  int64  `json:"issued_at" yaml:"issued_at"`
	DeviceID  string `json:"device_id" yaml:"device_id"`
}

type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type TokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	Error     string `json:"error,omitempty"`
}

func RequestDeviceCode(baseURL, machineID string) (*DeviceCodeResponse, error) {
	body, err := json.Marshal(map[string]string{"machine_id": machineID})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := http.Post(baseURL+"/auth/device", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("request device code: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code request failed: %s", resp.Status)
	}

	var dcr DeviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&dcr); err != nil {
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
			resp, err := http.Post(baseURL+"/auth/token", "application/json", bytes.NewReader(body))
			if err != nil {
				return nil, fmt.Errorf("poll for token: %w", err)
			}

			var tr TokenResponse
			decErr := json.NewDecoder(resp.Body).Decode(&tr)
			resp.Body.Close()
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

func RefreshToken(baseURL string, token DeviceToken) (*TokenResponse, error) {
	body, err := json.Marshal(token)
	if err != nil {
		return nil, fmt.Errorf("marshal token: %w", err)
	}

	resp, err := http.Post(baseURL+"/auth/refresh", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed: %s", resp.Status)
	}

	var tr TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &tr, nil
}
