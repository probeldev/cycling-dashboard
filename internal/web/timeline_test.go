package web

import (
	"net/url"
	"testing"
	"time"
)

func TestParseTimelineRange(t *testing.T) {
	now := time.Date(2026, 11, 1, 15, 30, 0, 0, time.Local)
	day := func(s string) time.Time {
		d, err := time.ParseInLocation("2006-01-02", s, time.Local)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	tests := []struct {
		name    string
		query   string
		from    time.Time
		to      time.Time
		wantErr bool
	}{
		{"дефолт", "", day("2026-11-01").AddDate(0, 0, -179), day("2026-11-01"), false},
		{"явный", "from=2026-10-01&to=2026-10-31", day("2026-10-01"), day("2026-10-31"), false},
		{"только to", "to=2026-10-31", day("2026-10-31").AddDate(0, 0, -179), day("2026-10-31"), false},
		{"мусор from", "from=2026-13-01", time.Time{}, time.Time{}, true},
		{"мусор to", "to=x", time.Time{}, time.Time{}, true},
		{"from позже to", "from=2026-11-01&to=2026-10-01", time.Time{}, time.Time{}, true},
		{"слишком длинный", "from=2000-01-01&to=2026-11-01", time.Time{}, time.Time{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatal(err)
			}
			from, to, err := parseTimelineRange(q, now)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("%q: жду ошибку, получил %v..%v", tt.query, from, to)
				}
				return
			}
			if err != nil {
				t.Fatalf("%q: %v", tt.query, err)
			}
			if !from.Equal(tt.from) || !to.Equal(tt.to) {
				t.Errorf("%q: %v..%v, хочу %v..%v", tt.query, from, to, tt.from, tt.to)
			}
		})
	}
}
