package oauth

import (
	"crypto/subtle"
	"fmt"
)

// CheckState rejects a callback whose state does not match the login that
// was started. Both values are required so an empty callback cannot be
// substituted for the pending login.
func CheckState(expected, got string) error {
	if expected == "" || got == "" {
		return fmt.Errorf("oauth: state mismatch")
	}
	if subtle.ConstantTimeCompare([]byte(expected), []byte(got)) != 1 {
		return fmt.Errorf("oauth: state mismatch")
	}
	return nil
}

// ConfirmCallbackState requires a match when the code came from the loopback
// callback or the caller supplied a state. A bare authorization code pasted
// by the operator has no state and is left alone.
func ConfirmCallbackState(expected, got string, fromLoopback bool) error {
	if !fromLoopback && got == "" {
		return nil
	}
	return CheckState(expected, got)
}
