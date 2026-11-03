package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// TestWorkoutJSONNullMetrics — неизвестные метрики в JSON отдаются как null,
// а не 0; известные — числа, включая валидный нуль работы.
func TestWorkoutJSONNullMetrics(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	none, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: time.Minute,
	}, "hash-json-none", filepath.Join(t.TempDir(), "a.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	full, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: time.Hour,
		NormPower: 140, AvgPower: 123, FTP: 156, AvgCadenceActive: 82,
		IF: 0.9, TSS: 100, VI: 1.1, WorkKJ: 0, // нулевая работа при наличии данных
		HasIF: true, HasTSS: true, HasVI: true, HasWork: true,
		MetricsVersion: 1,
	}, "hash-json-full", filepath.Join(t.TempDir(), "b.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}

	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func(id int64) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/workout/%d", id), nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("id=%d: код %d", id, rec.Code)
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	got := get(none)
	for _, key := range []string{"IF", "TSS", "VI", "WorkKJ", "AvgCadenceActive"} {
		v, ok := got[key]
		if !ok || v != nil {
			t.Errorf("без мощности: %s = %v, хочу null", key, v)
		}
	}

	got = get(full)
	for key, want := range map[string]float64{"IF": 0.9, "TSS": 100, "VI": 1.1, "WorkKJ": 0, "AvgCadenceActive": 82} {
		v, ok := got[key].(float64)
		if !ok || v != want {
			t.Errorf("%s = %v, хочу %v", key, got[key], want)
		}
	}
}

// TestProfileEntryRollbackOnMetricsError — ошибка пересчёта метрик откатывает
// запись профиля и отдаёт 500, а не «успешно сохранено».
func TestProfileEntryRollbackOnMetricsError(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: time.Hour, NormPower: 140, AvgPower: 123, FTP: 156,
	}, "hash-prof-fail", filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
		t.Fatal(err)
	}
	// падаем только на записи метрик (metrics_version=2), штампы проходят
	if _, err := db.Exec(`CREATE TRIGGER fail_metrics BEFORE UPDATE ON workouts
	    WHEN NEW.metrics_version = 2 BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/profile/entry",
		strings.NewReader("kind=ftp&value=200&date=2026-10-30"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("код %d, хочу 500 (метрики не пересчитались)", rec.Code)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM profile_history`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("в profile_history %d записей, хочу 0 (транзакция должна откатиться)", n)
	}
}

// TestWorkoutsListJSON — список отдаёт массив; у объекта 40 полей, Override
// присутствует (контракт JSON не должен незаметно меняться).
func TestWorkoutsListJSON(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, start := range []time.Time{time.Now().Add(-2 * time.Hour), time.Now().Add(-time.Hour)} {
		if _, err := store.InsertWorkout(db, &store.Workout{
			StartTime: start, Duration: time.Hour,
		}, fmt.Sprintf("hash-list-%d", i), filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/workouts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("%d элементов, хочу 2", len(list))
	}
	if got := len(list[0]); got != 40 {
		keys := make([]string, 0, got)
		for k := range list[0] {
			keys = append(keys, k)
		}
		t.Errorf("полей в объекте %d, хочу 40: %v", got, keys)
	}
	if _, ok := list[0]["Override"]; !ok {
		t.Errorf("в объекте нет поля Override")
	}
}

// TestWorkoutMMPAPI — /mmp отдаёт только существующие окна, неизвестный id — 404.
func TestWorkoutMMPAPI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: time.Hour,
	}, "hash-mmp-api", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkoutMMP(db, id, []store.PowerRecord{
		{WindowSec: 5, Watts: 300}, {WindowSec: 60, Watts: 200},
	}, 1); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/workout/%d/mmp", id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	var got []store.PowerRecord
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].WindowSec != 5 || got[0].Watts != 300 || got[1].WindowSec != 60 {
		t.Errorf("MMP = %+v", got)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/workout/99999/mmp", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("неизвестный id: код %d, хочу 404", rec.Code)
	}
}

// getJSON делает GET-запрос и декодирует JSON-ответ; ошибка теста при
// не-200 или при ошибке декодирования.
func getJSON(t *testing.T, h http.Handler, path string, v any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: код %d", path, rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
