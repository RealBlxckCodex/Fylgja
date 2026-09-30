package pulse

import (
	"testing"
	"time"
)

func TestCron(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Berlin")
	base := time.Date(2026, 9, 30, 21, 15, 0, 0, loc) // Mittwoch
	cases := map[string]time.Time{
		"0 8 * * 1":     time.Date(2026, 10, 5, 8, 0, 0, 0, loc),
		"*/15 * * * *":  time.Date(2026, 9, 30, 21, 30, 0, 0, loc),
		"30 7 1 * *":    time.Date(2026, 10, 1, 7, 30, 0, 0, loc),
		"0 9-17/4 * * *": time.Date(2026, 10, 1, 9, 0, 0, 0, loc),
		"0 0 * * 0":     time.Date(2026, 10, 4, 0, 0, 0, 0, loc),
		"0 0 * * 7":     time.Date(2026, 10, 4, 0, 0, 0, 0, loc),
	}
	for expr, want := range cases {
		c, err := ParseCron(expr)
		if err != nil {
			t.Fatal(expr, err)
		}
		if got := c.Next(base); !got.Equal(want) {
			t.Errorf("%s: %s want %s", expr, got, want)
		}
	}
	for _, bad := range []string{"* * *", "60 * * * *", "* 24 * * *", "a b c d e", "*/0 * * * *"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("%q akzeptiert", bad)
		}
	}
}

func TestQuietHoursAndDecide(t *testing.T) {
	q := QuietHours{Start: "22:00", End: "07:00"}
	at := func(h, m int) time.Time { return time.Date(2026, 10, 1, h, m, 0, 0, time.UTC) }
	if !q.In(at(23, 0)) || !q.In(at(6, 59)) || q.In(at(7, 0)) || q.In(at(12, 0)) {
		t.Fatal("quiet hours")
	}
	if Decide(at(23, 0), High, q, 0, 6) != Digest || Decide(at(23, 0), Critical, q, 0, 6) != Now {
		t.Fatal("quiet/critical")
	}
	if Decide(at(12, 0), Normal, q, 6, 6) != Digest || Decide(at(12, 0), Normal, q, 5, 6) != Now || Decide(at(12, 0), Low, q, 0, 6) != Digest {
		t.Fatal("rate limit / low")
	}
	if Interval(30, time.Time{}, at(23, 0), q) != 2*time.Hour || Interval(30, at(11, 50), at(12, 0), q) != 15*time.Minute || Interval(0, time.Time{}, at(12, 0), q) != 0 {
		t.Fatal("interval")
	}
}
