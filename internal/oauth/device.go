package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// DeviceGrantType is the RFC 8628 device-code grant.
const DeviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// DeviceCode is an RFC 8628 device authorization response.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// LoginURL prefers the complete verification URL when present.
func (d DeviceCode) LoginURL() string {
	if strings.TrimSpace(d.VerificationURIComplete) != "" {
		return d.VerificationURIComplete
	}
	return d.VerificationURI
}

// ParseDeviceCode reads a device-authorization JSON body.
func ParseDeviceCode(raw []byte) (DeviceCode, error) {
	var d DeviceCode
	if err := json.Unmarshal(raw, &d); err != nil {
		return DeviceCode{}, err
	}
	if strings.TrimSpace(d.DeviceCode) == "" {
		return DeviceCode{}, fmt.Errorf("oauth device: missing device_code")
	}
	if strings.TrimSpace(d.UserCode) == "" && d.LoginURL() == "" {
		return DeviceCode{}, fmt.Errorf("oauth device: missing user_code and verification URI")
	}
	return d, nil
}

// DevicePollResult classifies a token-endpoint poll body.
type DevicePollResult struct {
	Token     Token
	Pending   bool
	SlowDown  bool
	Denied    bool
	Expired   bool
	ErrorCode string
}

type devicePollBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	IDToken          string `json:"id_token"`
	ExpiresIn        int    `json:"expires_in"`
	TokenType        string `json:"token_type"`
}

// InterpretDevicePoll parses RFC 8628 token poll JSON.
func InterpretDevicePoll(status int, raw []byte) (DevicePollResult, error) {
	var body devicePollBody
	if err := json.Unmarshal(raw, &body); err != nil {
		if status >= 300 {
			return DevicePollResult{}, fmt.Errorf("oauth device poll HTTP %d: %s", status, truncateBody(raw))
		}
		return DevicePollResult{}, err
	}
	switch body.Error {
	case "authorization_pending":
		return DevicePollResult{Pending: true, ErrorCode: body.Error}, nil
	case "slow_down":
		return DevicePollResult{Pending: true, SlowDown: true, ErrorCode: body.Error}, nil
	case "access_denied":
		return DevicePollResult{Denied: true, ErrorCode: body.Error}, fmt.Errorf("oauth device: access denied")
	case "expired_token":
		return DevicePollResult{Expired: true, ErrorCode: body.Error}, fmt.Errorf("oauth device: device code expired")
	case "":
		if status >= 300 {
			return DevicePollResult{}, fmt.Errorf("oauth device poll HTTP %d: %s", status, truncateBody(raw))
		}
		tok, err := ParseTokenResponse(raw)
		if err != nil {
			return DevicePollResult{}, err
		}
		if tok.AccessToken == "" {
			return DevicePollResult{}, fmt.Errorf("oauth device: missing access_token")
		}
		return DevicePollResult{Token: tok}, nil
	default:
		if body.ErrorDescription != "" {
			return DevicePollResult{ErrorCode: body.Error}, fmt.Errorf("oauth device: %s: %s", body.Error, body.ErrorDescription)
		}
		return DevicePollResult{ErrorCode: body.Error}, fmt.Errorf("oauth device: %s", body.Error)
	}
}

// PollDevice repeatedly calls fn until a token is issued, denied, or ctx ends.
func PollDevice(ctx context.Context, interval, max time.Duration, fn func() (DevicePollResult, error)) (Token, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if max <= 0 {
		max = 15 * time.Minute
	}
	deadline := time.Now().Add(max)
	first := true
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return Token{}, ctx.Err()
		case <-timer.C:
			if !first && time.Now().After(deadline) {
				return Token{}, fmt.Errorf("oauth device: timed out")
			}
			first = false
			res, err := fn()
			if res.Token.Valid() {
				return res.Token, nil
			}
			if !res.Pending {
				if err != nil {
					return Token{}, err
				}
				return Token{}, fmt.Errorf("oauth device: unexpected poll result")
			}
			next := interval
			if res.SlowDown {
				next += interval
			}
			timer.Reset(next)
		}
	}
}

func truncateBody(b []byte) string {
	const n = 240
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
