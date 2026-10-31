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

func TestWorkoutContext(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: 90 * time.Minute, HasPower: true, AvgPower: 200,
		TSS: 118, HasTSS: true,
	}, "hash-ctx", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	post := func(workoutID int64, body string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/workout/%d/context", workoutID),
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	api := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/workout/%d", id), nil))
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	if code := post(id, "rpe=7&notes=%D1%82%D0%B5%D0%BC%D0%BF%D0%BE%D0%B2%D0%B0%D1%8F"); code != http.StatusOK {
		t.Fatalf("сохранение: код %d", code)
	}
	m := api()
	if m["RPE"] != float64(7) || m["Notes"] != "темповая" {
		t.Errorf("RPE=%v Notes=%v", m["RPE"], m["Notes"])
	}
	// 7 × 90 мин = 630
	if m["SRPE"] != float64(630) {
		t.Errorf("SRPE=%v, хочу 630", m["SRPE"])
	}

	// границы шкалы
	for _, body := range []string{"rpe=1", "rpe=10"} {
		if code := post(id, body); code != http.StatusOK {
			t.Errorf("%s: код %d, хочу 200", body, code)
		}
	}
	for _, body := range []string{"rpe=0", "rpe=11", "rpe=abc"} {
		if code := post(id, body); code != http.StatusBadRequest {
			t.Errorf("%s: код %d, хочу 400", body, code)
		}
	}

	// заметка: 2000 символов можно, 2001 — нет
	if code := post(id, "notes="+strings.Repeat("a", 2000)); code != http.StatusOK {
		t.Errorf("2000 символов: код %d, хочу 200", code)
	}
	if code := post(id, "notes="+strings.Repeat("a", 2001)); code != http.StatusBadRequest {
		t.Errorf("2001 символ: код %d, хочу 400", code)
	}

	// очистка: пустые значения → NULL
	if code := post(id, "rpe=&notes="); code != http.StatusOK {
		t.Fatalf("очистка: код %d", code)
	}
	m = api()
	if m["RPE"] != float64(0) || m["Notes"] != "" || m["SRPE"] != nil {
		t.Errorf("после очистки: RPE=%v Notes=%v SRPE=%v", m["RPE"], m["Notes"], m["SRPE"])
	}

	if code := post(99999, "rpe=5"); code != http.StatusNotFound {
		t.Errorf("неизвестная тренировка: код %d, хочу 404", code)
	}
}

// TestWorkoutNotesXSS — заметка экранируется шаблоном, а не вставляется как HTML.
func TestWorkoutNotesXSS(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: time.Hour,
	}, "hash-ctx-xss", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	const payload = `<script>alert(1)</script>`
	if err := store.SetWorkoutContext(db, id, 5, payload); err != nil {
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
	if strings.Contains(body, payload) {
		t.Errorf("заметка вставлена как HTML без экранирования")
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("экранированная заметка не найдена")
	}
}
