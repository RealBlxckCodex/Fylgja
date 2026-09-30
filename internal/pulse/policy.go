package pulse

import (
	"encoding/json"
	"strings"
	"time"
)

// Config ist dots.pulse_config.
type Config struct {
	IntervalMin      int      `json:"interval_min"` // 0 = aus
	TopK             int      `json:"top_k"`
	MaxNudgesPerDay  int      `json:"max_nudges_per_day"`
	DigestTimes      []string `json:"digest_times"`
	Feeds            []string `json:"feeds"`
	Interests        []string `json:"interests"`
	CriticalChannel  string   `json:"critical_channel"`
}

// QuietHours ist dots.quiet_hours.
type QuietHours struct {
	Start string `json:"start"` // "22:00"
	End   string `json:"end"`   // "07:00"
}

func ParseConfig(b []byte) Config {
	c := Config{IntervalMin: 30, TopK: 5, MaxNudgesPerDay: 6, DigestTimes: []string{"08:00", "18:00"}}
	if len(b) > 0 {
		_ = json.Unmarshal(b, &c)
	}
	if c.TopK <= 0 {
		c.TopK = 5
	}
	if c.MaxNudgesPerDay <= 0 {
		c.MaxNudgesPerDay = 6
	}
	return c
}

func ParseQuiet(b []byte) QuietHours {
	q := QuietHours{Start: "22:00", End: "07:00"}
	if len(b) > 0 {
		_ = json.Unmarshal(b, &q)
	}
	return q
}

func minutes(hhmm string) int {
	var h, m int
	if len(hhmm) >= 4 {
		p := strings.SplitN(hhmm, ":", 2)
		if len(p) == 2 {
			for _, r := range p[0] {
				h = h*10 + int(r-'0')
			}
			for _, r := range p[1] {
				m = m*10 + int(r-'0')
			}
		}
	}
	return h*60 + m
}

// In meldet, ob t (in lokaler Zeit des Owners) in den Quiet Hours liegt.
func (q QuietHours) In(t time.Time) bool {
	if q.Start == "" || q.End == "" || q.Start == q.End {
		return false
	}
	now := t.Hour()*60 + t.Minute()
	s, e := minutes(q.Start), minutes(q.End)
	if s < e {
		return now >= s && now < e
	}
	return now >= s || now < e // über Mitternacht
}

// Urgency eines Items.
type Urgency string

const (
	Low      Urgency = "low"
	Normal   Urgency = "normal"
	High     Urgency = "high"
	Critical Urgency = "critical"
)

// Delivery-Entscheidung.
type Delivery string

const (
	Now    Delivery = "now"
	Digest Delivery = "digest"
)

// Decide ist die Notification Policy (10.4).
func Decide(now time.Time, u Urgency, q QuietHours, sentToday, maxPerDay int) Delivery {
	if u == Critical {
		return Now // kritisch durchbricht Quiet Hours
	}
	if q.In(now) {
		return Digest
	}
	if sentToday >= maxPerDay {
		return Digest
	}
	if u == High || u == Normal {
		return Now
	}
	return Digest
}

// Interval berechnet den adaptiven Takt: bei Aktivität dichter, nachts sparsamer.
func Interval(base int, lastActivity, now time.Time, q QuietHours) time.Duration {
	if base <= 0 {
		return 0
	}
	d := time.Duration(base) * time.Minute
	switch {
	case q.In(now):
		d *= 4
	case !lastActivity.IsZero() && now.Sub(lastActivity) < 30*time.Minute:
		d /= 2
	case lastActivity.IsZero() || now.Sub(lastActivity) > 24*time.Hour:
		d *= 2
	}
	return max(d, 5*time.Minute)
}
