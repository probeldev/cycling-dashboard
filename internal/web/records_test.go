package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

func TestParseRecordsPeriod(t *testing.T) {
	now := time.Date(2026, 11, 1, 15, 0, 0, 0, time.Local)
	day := store.StartOfDay(now)
	tests := []struct {
		name     string
		query    string
		fromZero bool
		from     time.Time
		prev     int
		wantErr  bool
	}{
		{"дефолт all", "", true, time.Time{}, 0, false},
		{"all", "period=all", true, time.Time{}, 0, false},
		{"30d", "period=30d", false, day.AddDate(0, 0, -29), 30, false},
		{"90d", "period=90d", false, day.AddDate(0, 0, -89), 90, false},
		{"365d", "period=365d", false, day.AddDate(0, 0, -364), 365, false},
		{"мусор", "period=week", false, time.Time{}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatal(err)
			}
			from, to, prev, err := parseRecordsPeriod(q, now)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("%q: жду ошибку", tt.query)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !to.Equal(day.AddDate(0, 0, 1)) {
				t.Errorf("to = %v, хочу конец сегодняшнего дня", to)
			}
			if tt.fromZero != from.IsZero() || (!tt.fromZero && !from.Equal(tt.from)) || prev != tt.prev {
				t.Errorf("%q: from=%v prev=%d, хочу %v/%d", tt.query, from, prev, tt.from, tt.prev)
			}
		})
	}
}

// recordsFixture — БД с одной тренировкой сегодня и MMP.
func recordsFixture(t *testing.T) (id int64, h http.Handler) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	id, err = store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: time.Hour, HasPower: true, AvgPower: 200,
	}, "hash-records-api", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkoutMMP(db, id, []store.PowerRecord{
		{WindowSec: 5, Watts: 300}, {WindowSec: 60, Watts: 200},
	}, 1); err != nil {
		t.Fatal(err)
	}
	h, err = Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return id, h
}

func TestRecordsAPI(t *testing.T) {
	_, h := recordsFixture(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/records?period=all", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	var recs []store.Record
	if err := json.Unmarshal(rec.Body.Bytes(), &recs); err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].WindowSec != 5 || recs[0].Watts != 300 {
		t.Errorf("records = %+v", recs)
	}
	if recs[0].Name != "" || recs[0].WorkoutID == 0 {
		t.Errorf("в записи нет id/имени: %+v", recs[0])
	}

	// период 30d: тренировка сегодня, предыдущего периода нет
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/records?period=30d", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("period=30d: код %d", rec.Code)
	}
	var period []store.Record
	if err := json.Unmarshal(rec.Body.Bytes(), &period); err != nil {
		t.Fatal(err)
	}
	if len(period) != 2 || period[0].Previous != nil {
		t.Errorf("period=30d: %+v", period)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/records?period=week", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("period=week: код %d, хочу 400", rec.Code)
	}
}

func TestWorkoutPRsAPI(t *testing.T) {
	id, h := recordsFixture(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/workout/%d/prs", id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	var prs map[string][]string
	if err := json.Unmarshal(rec.Body.Bytes(), &prs); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"5", "60"} {
		got := prs[key]
		if len(got) == 0 || got[0] != "all" {
			t.Errorf("окно %s: %v, хочу рекорд all", key, got)
		}
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/workout/99999/prs", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("неизвестный id: код %d, хочу 404", rec.Code)
	}
}

// TestWorkoutPageMMP — карточки лучших усилий с бейджем рекорда.
func TestWorkoutPageMMP(t *testing.T) {
	id, h := recordsFixture(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/workout/%d", id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Лучшие усилия", "5 с", "300.0 Вт", "рекорд за всё время (без замеров)", "1 мин", "200.0 Вт"} {
		if !strings.Contains(body, want) {
			t.Errorf("на деталке нет %q", want)
		}
	}
}

// TestRecordsAPIPrevious — previous заполняется из реального предыдущего периода.
func TestRecordsAPIPrevious(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, w := range []struct {
		start time.Time
		hash  string
		watts float64
	}{
		{time.Now(), "hash-prev-cur", 300},
		{time.Now().AddDate(0, 0, -45), "hash-prev-old", 200},
	} {
		id, err := store.InsertWorkout(db, &store.Workout{
			StartTime: w.start, Duration: time.Hour, HasPower: true, AvgPower: 200,
		}, w.hash, filepath.Join(t.TempDir(), "x.fit"), 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetWorkoutMMP(db, id, []store.PowerRecord{{WindowSec: 5, Watts: w.watts}}, 1); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func(period string) []store.Record {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/records?period="+period, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("period=%s: код %d", period, rec.Code)
		}
		var out []store.Record
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	cur := get("30d")
	if len(cur) != 1 || cur[0].Watts != 300 {
		t.Fatalf("30d: %+v", cur)
	}
	if cur[0].Previous == nil || cur[0].Previous.Watts != 200 {
		t.Errorf("30d: previous = %+v, хочу 200", cur[0].Previous)
	}
	// в предыдущем 90-дневном окне тренировок нет — previous пуст
	if got := get("90d"); len(got) != 1 || got[0].Previous != nil {
		t.Errorf("90d: %+v", got)
	}
}
