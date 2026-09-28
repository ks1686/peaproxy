package router

import "testing"

func TestAutomaticNames(t *testing.T) {
	if !Automatic("pea/free") || Automatic("gpt-4o") {
		t.Fatal("automatic detection")
	}
}
