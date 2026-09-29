package clients

import (
	"encoding/json"
	"testing"
)

func TestUpsertJSONKeyDoesNotAliasSourceCapacity(t *testing.T) {
	body := []byte(`{"a":{"b":1}}`)
	for _, key := range []string{"c", "d", "e"} {
		var err error
		if body, err = upsertJSONKey(body, key, key); err != nil {
			t.Fatal(err)
		}
		if !json.Valid(body) {
			t.Fatalf("upsert %s produced invalid json: %s", key, body)
		}
	}
	if want := `{"a":{"b":1},"c":"c","d":"d","e":"e"}`; string(body) != want {
		t.Fatalf("got %s want %s", body, want)
	}
}
