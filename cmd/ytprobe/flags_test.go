package main

import (
	"testing"
	"time"
)

func TestParseDurations(t *testing.T) {
	got, err := parseDurations("10s, 1.5s,1s")
	if err != nil || len(got) != 3 || got[1] != 1500*time.Millisecond {
		t.Fatalf("parseDurations = %v, %v", got, err)
	}
	for _, bad := range []string{"", "fast", "1s,2s", "0s", "-1s"} {
		if _, err := parseDurations(bad); err == nil {
			t.Errorf("parseDurations(%q) should fail", bad)
		}
	}
}

func TestParseClasses(t *testing.T) {
	got, err := parseClasses("browse, player")
	if err != nil || len(got) != 2 || got[1] != "player" {
		t.Fatalf("parseClasses = %v, %v", got, err)
	}
	for _, bad := range []string{"", "brows", "browse,api"} {
		if _, err := parseClasses(bad); err == nil {
			t.Errorf("parseClasses(%q) should fail", bad)
		}
	}
}
