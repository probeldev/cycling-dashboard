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

// TestWorkoutBike — назначение, смена и сброс велосипеда через веб; мусор — 400.
func TestWorkoutBike(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: time.Hour,
	}, "hash-bike-web", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	bikes, err := store.Bikes(db)
	if err != nil || len(bikes) == 0 {
		t.Fatalf("велосипеды: %v, %v", bikes, err)
	}
	post := func(workoutID int64, bikeID string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/workout/%d/bike", workoutID),
			strings.NewReader("bike_id="+bikeID))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	getBike := func() (int64, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/workout/%d", id), nil))
		var w store.Workout
		if err := json.Unmarshal(rec.Body.Bytes(), &w); err != nil {
			t.Fatal(err)
		}
		return w.BikeID, w.Bike
	}

	if code := post(id, fmt.Sprint(bikes[0].ID)); code != http.StatusOK {
		t.Fatalf("назначение: код %d", code)
	}
	if gotID, name := getBike(); gotID != bikes[0].ID || name != bikes[0].Name {
		t.Errorf("BikeID=%d Bike=%q, хочу %d/%q", gotID, name, bikes[0].ID, bikes[0].Name)
	}
	if code := post(id, fmt.Sprint(bikes[1].ID)); code != http.StatusOK {
		t.Fatalf("смена: код %d", code)
	}
	if _, name := getBike(); name != bikes[1].Name {
		t.Errorf("после смены Bike=%q", name)
	}
	if code := post(id, ""); code != http.StatusOK {
		t.Fatalf("сброс: код %d", code)
	}
	if gotID, name := getBike(); gotID != 0 || name != "" {
		t.Errorf("после сброса BikeID=%d Bike=%q, хочу 0/пусто", gotID, name)
	}

	if code := post(id, "99999"); code != http.StatusBadRequest {
		t.Errorf("неизвестный bike_id: код %d, хочу 400", code)
	}
	if code := post(id, "abc"); code != http.StatusBadRequest {
		t.Errorf("мусор в bike_id: код %d, хочу 400", code)
	}
	if code := post(99999, "1"); code != http.StatusNotFound {
		t.Errorf("неизвестная тренировка: код %d, хочу 404", code)
	}
}

// TestWorkoutPageBike — на деталке есть выбор велосипеда с текущим значением.
func TestWorkoutPageBike(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bikes, err := store.Bikes(db)
	if err != nil || len(bikes) == 0 {
		t.Fatalf("велосипеды: %v, %v", bikes, err)
	}
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: time.Hour,
	}, "hash-bike-page", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkoutBike(db, id, &bikes[0].ID); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/workout/%d", id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	body := rec.Body.String()
	for _, name := range []string{bikes[0].Name, bikes[1].Name, bikes[2].Name} {
		if !strings.Contains(body, name) {
			t.Errorf("в выборе нет %q", name)
		}
	}
	if !strings.Contains(body, fmt.Sprintf(`value="%d" selected`, bikes[0].ID)) {
		t.Errorf("текущий велосипед не выбран")
	}
}
