package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
		NormPower: 140, AvgPower: 123, FTP: 156,
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
	for _, key := range []string{"IF", "TSS", "VI", "WorkKJ"} {
		v, ok := got[key]
		if !ok || v != nil {
			t.Errorf("без мощности: %s = %v, хочу null", key, v)
		}
	}

	got = get(full)
	for key, want := range map[string]float64{"IF": 0.9, "TSS": 100, "VI": 1.1, "WorkKJ": 0} {
		v, ok := got[key].(float64)
		if !ok || v != want {
			t.Errorf("%s = %v, хочу %v", key, got[key], want)
		}
	}
}
