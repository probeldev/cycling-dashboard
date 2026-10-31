package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// TestWorkoutDBErrorIs500 — ошибка базы не маскируется под 404.
func TestWorkoutDBErrorIs500(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.InsertWorkout(db, &store.Workout{StartTime: time.Now(), Duration: time.Hour},
		"hash-db-closed", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/workout/%d", id), nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("код %d, хочу 500", rec.Code)
	}
}
