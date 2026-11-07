package poll_test

import (
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/poll"
)

// Waits double from Min up to Max while nothing is found, and start again
// from Min once something is.
func TestBackoff(t *testing.T) {
	var b poll.Backoff
	var got []time.Duration
	for i := 0; i < 7; i++ {
		got = append(got, b.Next())
	}
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30}
	for i := range want {
		if got[i] != want[i]*time.Second {
			t.Fatalf("waits %v, want %v seconds", got, want)
		}
	}
	b.Reset()
	if d := b.Next(); d != time.Second {
		t.Errorf("after work: %v", d)
	}
	small := poll.Backoff{Min: time.Second, Max: 10 * time.Millisecond}
	if d := small.Next(); d != 10*time.Millisecond {
		t.Errorf("Max below Min: %v", d)
	}
}
