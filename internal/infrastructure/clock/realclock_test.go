package clock

import (
	"testing"
	"time"
)

func TestNowIsUTCAndCurrent(t *testing.T) {
	c := New()
	before := time.Now().UTC()
	now := c.Now()
	after := time.Now().UTC()

	if loc := now.Location(); loc != time.UTC {
		t.Errorf("Now().Location() = %v, want UTC", loc)
	}
	if now.Before(before.Add(-time.Second)) || now.After(after.Add(time.Second)) {
		t.Errorf("Now() = %v outside wall-clock window [%s, %s]", now, before, after)
	}
}
