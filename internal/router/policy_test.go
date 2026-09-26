package router

import "testing"

func TestNormalizePolicyDefaultsToRoundRobin(t *testing.T) {
	cases := []struct {
		in   Policy
		want Policy
	}{
		{"", PolicyRoundRobin},
		{"ROUND-ROBIN", PolicyRoundRobin},
		{"round-robin", PolicyRoundRobin},
		{"fill-first", PolicyFillFirst},
		{" FILL-FIRST ", PolicyFillFirst},
		{"sticky", PolicySticky},
		{"unknown", PolicyRoundRobin},
	}
	for _, tc := range cases {
		if got := NormalizePolicy(tc.in); got != tc.want {
			t.Fatalf("NormalizePolicy(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestOrderRoundRobinRotatesStart(t *testing.T) {
	hot := []string{"a", "b", "c"}
	var rr uint64
	got1 := Order(PolicyRoundRobin, hot, &rr, "")
	got2 := Order(PolicyRoundRobin, hot, &rr, "")
	got3 := Order(PolicyRoundRobin, hot, &rr, "")
	if stringsJoin(got1) != "a,b,c" || stringsJoin(got2) != "b,c,a" || stringsJoin(got3) != "c,a,b" {
		t.Fatalf("round-robin got %v %v %v", got1, got2, got3)
	}
}

func TestOrderFillFirstKeepsConfigOrder(t *testing.T) {
	hot := []string{"a", "b", "c"}
	var rr uint64
	for i := 0; i < 3; i++ {
		got := Order(PolicyFillFirst, hot, &rr, "")
		if stringsJoin(got) != "a,b,c" {
			t.Fatalf("fill-first call %d: %v", i, got)
		}
	}
	if rr != 0 {
		t.Fatalf("fill-first must not consume round-robin counter: %d", rr)
	}
}

func TestOrderStickyPrefersLastSuccess(t *testing.T) {
	hot := []string{"a", "b", "c"}
	var rr uint64
	got := Order(PolicySticky, hot, &rr, "b")
	if stringsJoin(got) != "b,a,c" {
		t.Fatalf("sticky: %v", got)
	}
	if rr != 0 {
		t.Fatalf("sticky must not consume round-robin counter: %d", rr)
	}
}

func TestOrderStickyWithoutMemoryIsFillFirst(t *testing.T) {
	hot := []string{"a", "b"}
	got := Order(PolicySticky, hot, nil, "")
	if stringsJoin(got) != "a,b" {
		t.Fatalf("sticky empty: %v", got)
	}
}

func TestOrderStickyMissingAccountFallsBack(t *testing.T) {
	hot := []string{"a", "c"}
	got := Order(PolicySticky, hot, nil, "b")
	if stringsJoin(got) != "a,c" {
		t.Fatalf("sticky cooled: %v", got)
	}
}

func stringsJoin(ids []string) string {
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += ","
		}
		out += id
	}
	return out
}
