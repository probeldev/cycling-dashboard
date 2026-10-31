package web

import (
	"database/sql"
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

func contextFixture(t *testing.T) (id int64, h http.Handler, db *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	id, err = store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: 90 * time.Minute, HasPower: true, AvgPower: 200,
		TSS: 118, HasTSS: true,
	}, "hash-ctx", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	h, err = Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return id, h, db
}

func postForm(t *testing.T, h http.Handler, path string, form url.Values) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestWorkoutContext(t *testing.T) {
	id, h, _ := contextFixture(t)
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
	path := fmt.Sprintf("/workout/%d/context", id)

	if code := postForm(t, h, path, url.Values{"rpe": {"7"}, "notes": {"темповая"}}); code != http.StatusOK {
		t.Fatalf("сохранение: код %d", code)
	}
	m := api()
	if m["RPE"] != float64(7) || m["Notes"] != "темповая" {
		t.Errorf("RPE=%v Notes=%v", m["RPE"], m["Notes"])
	}
	if m["SRPE"] != float64(630) { // 7 × 90 мин
		t.Errorf("SRPE=%v, хочу 630", m["SRPE"])
	}

	// частичный апдейт: только RPE не стирает заметку
	if code := postForm(t, h, path, url.Values{"rpe": {"5"}}); code != http.StatusOK {
		t.Fatalf("rpe-only: код %d", code)
	}
	m = api()
	if m["RPE"] != float64(5) || m["Notes"] != "темповая" {
		t.Errorf("после rpe-only: RPE=%v Notes=%v", m["RPE"], m["Notes"])
	}
	// только заметка не стирает RPE
	if code := postForm(t, h, path, url.Values{"notes": {"новая"}}); code != http.StatusOK {
		t.Fatalf("notes-only: код %d", code)
	}
	m = api()
	if m["RPE"] != float64(5) || m["Notes"] != "новая" {
		t.Errorf("после notes-only: RPE=%v Notes=%v", m["RPE"], m["Notes"])
	}

	// границы шкалы
	for _, rpe := range []string{"1", "10"} {
		if code := postForm(t, h, path, url.Values{"rpe": {rpe}}); code != http.StatusOK {
			t.Errorf("rpe=%s: код %d, хочу 200", rpe, code)
		}
	}
	for _, form := range []url.Values{
		{"rpe": {"0"}}, {"rpe": {"11"}}, {"rpe": {"abc"}}, {},
	} {
		if code := postForm(t, h, path, form); code != http.StatusBadRequest {
			t.Errorf("%v: код %d, хочу 400", form, code)
		}
	}

	// лимит заметки — руны, не байты: 2000 «я» (4000 байт) можно, 2001 — нет
	if code := postForm(t, h, path, url.Values{"notes": {strings.Repeat("я", 2000)}}); code != http.StatusOK {
		t.Errorf("2000 рун: код %d, хочу 200", code)
	}
	if code := postForm(t, h, path, url.Values{"notes": {strings.Repeat("я", 2001)}}); code != http.StatusBadRequest {
		t.Errorf("2001 руна: код %d, хочу 400", code)
	}

	// очистка: пустые значения → NULL
	if code := postForm(t, h, path, url.Values{"rpe": {""}, "notes": {""}}); code != http.StatusOK {
		t.Fatalf("очистка: код %d", code)
	}
	m = api()
	if m["RPE"] != float64(0) || m["Notes"] != "" || m["SRPE"] != nil {
		t.Errorf("после очистки: RPE=%v Notes=%v SRPE=%v", m["RPE"], m["Notes"], m["SRPE"])
	}

	if code := postForm(t, h, "/workout/99999/context", url.Values{"rpe": {"5"}}); code != http.StatusNotFound {
		t.Errorf("неизвестная тренировка: код %d, хочу 404", code)
	}
}

// TestWorkoutNotesXSS — заметка экранируется в textarea: breakout-пейлоады
// не вырываются из разметки.
func TestWorkoutNotesXSS(t *testing.T) {
	id, h, db := contextFixture(t)
	for _, payload := range []string{
		`</textarea><script>alert(1)</script>`,
		`</textarea><svg onload=alert(1)>`,
	} {
		note := payload
		if err := store.SetWorkoutContext(db, id, store.WorkoutContextUpdate{Notes: &note}); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/workout/%d", id), nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("код %d", rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, payload) {
			t.Errorf("заметка вставлена как HTML без экранирования: %q", payload)
		}
		if strings.Contains(body, "</textarea><script") || strings.Contains(body, "</textarea><svg") {
			t.Errorf("payload вырвался из textarea: %q", payload)
		}
		if !strings.Contains(body, "&lt;/textarea&gt;") {
			t.Errorf("экранированная заметка не найдена: %q", payload)
		}
	}
}
