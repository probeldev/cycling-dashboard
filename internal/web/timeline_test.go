package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
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

// TestTimelineAPIIgnoresFuture — будущая тренировка не попадает ни в окно
// вокруг сегодня, ни в данные heatmap (тот же /api/timeline за год).
func TestTimelineAPIIgnoresFuture(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	if _, err := store.InsertWorkout(db, &store.Workout{
		StartTime: now.Add(-2 * time.Hour), Duration: time.Hour,
		TSS: 30, DistanceKm: 20,
	}, "hash-tl-api-past", filepath.Join(t.TempDir(), "p.fit"), 1); err != nil {
		t.Fatal(err)
	}
	future := now.Add(2 * time.Hour)
	if _, err := store.InsertWorkout(db, &store.Workout{
		StartTime: future, Duration: 4 * time.Hour,
		TSS: 1000, DistanceKm: 200,
	}, "hash-tl-api-future", filepath.Join(t.TempDir(), "f.fit"), 1); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func(from, to time.Time) []store.Day {
		t.Helper()
		u := fmt.Sprintf("/api/timeline?from=%s&to=%s",
			from.Format("2006-01-02"), to.Format("2006-01-02"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, u, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: код %d (%s)", u, rec.Code, rec.Body.String())
		}
		var days []store.Day
		if err := json.Unmarshal(rec.Body.Bytes(), &days); err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		return days
	}

	today := store.StartOfDay(now)
	var tss, dist float64
	rides := 0
	for _, d := range get(today.AddDate(0, 0, -1), store.StartOfDay(future)) {
		tss += d.TSS
		dist += d.DistanceKm
		rides += d.Rides
	}
	if tss != 30 || rides != 1 || dist != 20 {
		t.Errorf("timeline = %.0f TSS / %d поездка / %.0f км, хочу 30 / 1 / 20", tss, rides, dist)
	}

	// heatmap запрашивает весь год: будущая тренировка не должна появиться
	// ни в своём дне, ни сложиться с прошедшей в тот же день
	year := future.Year()
	for _, d := range get(time.Date(year, 1, 1, 0, 0, 0, 0, time.Local), time.Date(year, 12, 31, 0, 0, 0, 0, time.Local)) {
		if d.TSS > 30 || d.DistanceKm > 20 || d.Rides > 1 {
			t.Errorf("будущая тренировка попала в heatmap: %+v", d)
		}
	}
}
