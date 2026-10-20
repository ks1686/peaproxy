package jsonx

import (
	"encoding/json"
	"testing"
)

func TestDropTopLevelKeysAdjacentAndDuplicateMembers(t *testing.T) {
	cases := []struct {
		name, in, want string
		keys           []string
	}{
		{"first two", `{"store":true,"stream_options":{"a":1},"model":"m"}`, `{"model":"m"}`, []string{"store", "stream_options"}},
		{"first three", `{"a":1, "b":2 ,"c":3,"d":4}`, `{"d":4}`, []string{"a", "b", "c"}},
		{"duplicate first key", `{"store":true,"store":false,"model":"m"}`, `{"model":"m"}`, []string{"store"}},
		{"all members", `{"a":1,"b":2}`, `{}`, []string{"a", "b"}},
		{"last two", `{"m":1,"a":2,"b":3}`, `{"m":1}`, []string{"a", "b"}},
		{"middle", `{"m":1,"a":2,"b":3,"z":4}`, `{"m":1,"z":4}`, []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DropTopLevelKeys([]byte(tc.in), tc.keys...)
			var gotV, wantV any
			if err := json.Unmarshal(got, &gotV); err != nil {
				t.Fatalf("invalid JSON %q: %v", got, err)
			}
			_ = json.Unmarshal([]byte(tc.want), &wantV)
			gb, _ := json.Marshal(gotV)
			wb, _ := json.Marshal(wantV)
			if string(gb) != string(wb) {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func FuzzDropTopLevelKeys(f *testing.F) {
	f.Add(`{"store":true,"stream_options":{},"model":"m"}`, "store", "stream_options")
	f.Add(`{"a":1,"a":2}`, "a", "b")
	f.Add(`{ "x" : [1,{"y":"}"}] , "z":"\"" }`, "x", "z")
	f.Fuzz(func(t *testing.T, in, k1, k2 string) {
		out := DropTopLevelKeys([]byte(in), k1, k2)
		var obj map[string]json.RawMessage
		if json.Unmarshal([]byte(in), &obj) != nil {
			return
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("valid input %q gave invalid output %q: %v", in, out, err)
		}
		for _, k := range []string{k1, k2} {
			if _, ok := got[k]; ok {
				t.Fatalf("key %q survived in %q", k, out)
			}
		}
	})
}

func TestSetTopLevelRawWinsOverDuplicateKeys(t *testing.T) {
	out := SetStream([]byte(`{"stream":true,"model":"m","stream":false}`), true)
	var p struct {
		Stream bool   `json:"stream"`
		Model  string `json:"model"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	if !p.Stream || p.Model != "m" {
		t.Fatalf("SetStream(true) on duplicated keys decodes to stream=%v model=%q: %s", p.Stream, p.Model, out)
	}
}
