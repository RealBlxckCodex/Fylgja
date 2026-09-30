// Package pulse ist die proaktive Engine (Spec Kapitel 10): Scheduler, Signale,
// Triage/Attention-Queue, Read-only-Pulse-Runs, Notification Policy und Digest.
package pulse

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron ist ein geparster 5-Feld-Ausdruck (Minute Stunde Tag Monat Wochentag).
type Cron struct {
	min, hour, dom, mon, dow [64]bool
	domStar, dowStar         bool
}

func parseField(f string, lo, hi int, set *[64]bool) (star bool, err error) {
	for _, part := range strings.Split(f, ",") {
		step := 1
		if a, b, ok := strings.Cut(part, "/"); ok {
			part = a
			if step, err = strconv.Atoi(b); err != nil || step <= 0 {
				return false, fmt.Errorf("cron: schritt %q ungültig", b)
			}
		}
		from, to := lo, hi
		switch {
		case part == "*":
			star = step == 1
		case strings.Contains(part, "-"):
			a, b, _ := strings.Cut(part, "-")
			if from, err = strconv.Atoi(a); err != nil {
				return false, err
			}
			if to, err = strconv.Atoi(b); err != nil {
				return false, err
			}
		default:
			if from, err = strconv.Atoi(part); err != nil {
				return false, fmt.Errorf("cron: %q ungültig", part)
			}
			to = from
			if step > 1 {
				to = hi
			}
		}
		if from < lo || to > hi || from > to {
			return false, fmt.Errorf("cron: bereich %d-%d außerhalb %d-%d", from, to, lo, hi)
		}
		for i := from; i <= to; i += step {
			set[i] = true
		}
	}
	return star, nil
}

// ParseCron parst einen Ausdruck; Wochentag 0 und 7 = Sonntag.
func ParseCron(expr string) (*Cron, error) {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, fmt.Errorf("cron: 5 felder erwartet, %d erhalten", len(f))
	}
	c := &Cron{}
	var err error
	if _, err = parseField(f[0], 0, 59, &c.min); err != nil {
		return nil, err
	}
	if _, err = parseField(f[1], 0, 23, &c.hour); err != nil {
		return nil, err
	}
	if c.domStar, err = parseField(f[2], 1, 31, &c.dom); err != nil {
		return nil, err
	}
	if _, err = parseField(f[3], 1, 12, &c.mon); err != nil {
		return nil, err
	}
	if c.dowStar, err = parseField(f[4], 0, 7, &c.dow); err != nil {
		return nil, err
	}
	if c.dow[7] {
		c.dow[0] = true
	}
	return c, nil
}

func (c *Cron) dayMatches(t time.Time) bool {
	d, w := c.dom[t.Day()], c.dow[int(t.Weekday())]
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return w
	case c.dowStar:
		return d
	}
	return d || w // klassische Cron-Semantik
}

// Next liefert den nächsten Zeitpunkt nach t (in der Zeitzone von t).
func (c *Cron) Next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		if !c.mon[int(t.Month())] {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !c.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !c.hour[t.Hour()] {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if !c.min[t.Minute()] {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}
