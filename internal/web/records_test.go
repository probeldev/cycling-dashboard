package web

import (
	"bytes"
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
			if !to.Equal(now) {
				t.Errorf("to = %v, хочу now %v: будущая тренировка не попадает в период", to, now)
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

// TestFutureWorkoutIgnoredInRecordsAndCP — будущая тренировка с
// power_records не меняет рекорды, CP/W'/FTP-оценку и PR-флаги: окна
// рекордов заканчиваются на now, а не на полуночи следующего дня.
func TestFutureWorkoutIgnoredInRecordsAndCP(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	past, err := store.InsertWorkout(db, &store.Workout{
		StartTime: now.Add(-24 * time.Hour), Duration: time.Hour, HasPower: true, AvgPower: 200,
	}, "hash-future-past", filepath.Join(t.TempDir(), "p.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkoutMMP(db, past, []store.PowerRecord{
		{WindowSec: 120, Watts: 250}, {WindowSec: 300, Watts: 220},
		{WindowSec: 600, Watts: 205}, {WindowSec: 1200, Watts: 190},
	}, 1); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}

	paths := []string{
		"/api/records?period=all",
		"/api/records?period=30d",
		"/api/records?period=90d",
		"/api/records?period=365d",
		"/api/cp?period=all",
		fmt.Sprintf("/api/workout/%d/prs", past),
		"/progress",
	}
	get := func(path string) []byte {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: код %d", path, rec.Code)
		}
		return rec.Body.Bytes()
	}
	before := make(map[string][]byte, len(paths))
	for _, p := range paths {
		before[p] = get(p)
	}

	// До вставки данные реальные: рекорды есть, CP и FTP-оценка посчитаны.
	var recs []store.Record
	if err := json.Unmarshal(before["/api/records?period=all"], &recs); err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 || recs[0].WorkoutID != past {
		t.Fatalf("рекорды до будущей: %+v", recs)
	}
	var cp cpResult
	if err := json.Unmarshal(before["/api/cp?period=all"], &cp); err != nil {
		t.Fatal(err)
	}
	if cp.CP == nil || cp.FTPEst == nil {
		t.Fatalf("CP до будущей: %+v", cp)
	}

	future, err := store.InsertWorkout(db, &store.Workout{
		StartTime: now.Add(2 * time.Hour), Duration: 4 * time.Hour, HasPower: true, AvgPower: 450,
		TSS: 1000, DistanceKm: 200, Calories: 4000, AscentM: 2000,
	}, "hash-future-workout", filepath.Join(t.TempDir(), "f.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkoutMMP(db, future, []store.PowerRecord{
		{WindowSec: 60, Watts: 500}, {WindowSec: 300, Watts: 450}, {WindowSec: 1200, Watts: 400},
	}, 1); err != nil {
		t.Fatal(err)
	}

	for _, p := range paths {
		if after := get(p); !bytes.Equal(before[p], after) {
			t.Errorf("%s изменился от будущей тренировки:\nдо  %s\nпосле %s", p, before[p], after)
		}
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
	for _, want := range []string{"Лучшие усилия", "5 с", ">300.0<", "рекорд за всё время (без замеров)", "1 мин", ">200.0<",
		`class="w-summary"`, "Рекордные отрезки: 5 с и 1 мин (за всё время)", `class="chip chip--record"`} {
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
