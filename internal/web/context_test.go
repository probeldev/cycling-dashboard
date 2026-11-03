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

	if code := postForm(t, h, path, url.Values{"notes": {"темповая"}}); code != http.StatusOK {
		t.Fatalf("сохранение: код %d", code)
	}
	m := api()
	if m["Notes"] != "темповая" {
		t.Errorf("Notes=%v", m["Notes"])
	}

	// без полей формы — 400
	if code := postForm(t, h, path, url.Values{}); code != http.StatusBadRequest {
		t.Errorf("пустая форма: код %d, хочу 400", code)
	}

	// лимит заметки — руны, не байты: 2000 «я» (4000 байт) можно, 2001 — нет
	if code := postForm(t, h, path, url.Values{"notes": {strings.Repeat("я", 2000)}}); code != http.StatusOK {
		t.Errorf("2000 рун: код %d, хочу 200", code)
	}
	if code := postForm(t, h, path, url.Values{"notes": {strings.Repeat("я", 2001)}}); code != http.StatusBadRequest {
		t.Errorf("2001 руна: код %d, хочу 400", code)
	}

	// очистка: пустое значение → NULL
	if code := postForm(t, h, path, url.Values{"notes": {""}}); code != http.StatusOK {
		t.Fatalf("очистка: код %d", code)
	}
	m = api()
	if m["Notes"] != "" {
		t.Errorf("после очистки: Notes=%v", m["Notes"])
	}

	if code := postForm(t, h, "/workout/99999/context", url.Values{"notes": {"x"}}); code != http.StatusNotFound {
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
