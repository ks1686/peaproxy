package oauth

import (
	"strings"
	"testing"
)

func TestParseDeviceCodePrefersCompleteURL(t *testing.T) {
	d, err := ParseDeviceCode([]byte(`{
		"device_code":"dc-1",
		"user_code":"ABCD-EFGH",
		"verification_uri":"https://example.com/device",
		"verification_uri_complete":"https://example.com/device?user_code=ABCD-EFGH",
		"expires_in":600,
		"interval":5
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if d.UserCode != "ABCD-EFGH" || d.DeviceCode != "dc-1" {
		t.Fatalf("%#v", d)
	}
	if !strings.Contains(d.LoginURL(), "user_code=") {
		t.Fatalf("login url %s", d.LoginURL())
	}
}

func TestInterpretDevicePollPendingAndToken(t *testing.T) {
	pending, err := InterpretDevicePoll(200, []byte(`{"error":"authorization_pending"}`))
	if err != nil || !pending.Pending {
		t.Fatalf("%#v %v", pending, err)
	}
	slow, err := InterpretDevicePoll(400, []byte(`{"error":"slow_down"}`))
	if err != nil || !slow.SlowDown {
		t.Fatalf("%#v %v", slow, err)
	}
	got, err := InterpretDevicePoll(200, []byte(`{"access_token":"at","refresh_token":"rt","expires_in":3600}`))
	if err != nil || got.Token.AccessToken != "at" || got.Token.RefreshToken != "rt" {
		t.Fatalf("%#v %v", got, err)
	}
	if _, err := InterpretDevicePoll(200, []byte(`{"error":"access_denied"}`)); err == nil {
		t.Fatal("expected denied")
	}
}
