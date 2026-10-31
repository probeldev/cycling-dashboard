package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// TestRenderPages — все HTML-страницы рендерятся целиком с реальными данными:
// главная с непустым деревом, деталка и профиль.
func TestRenderPages(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime:  time.Date(2026, 10, 31, 10, 0, 0, 0, time.Local),
		Duration:   90 * time.Minute,
		DistanceKm: 27.8,
		AvgPower:   123,
		MaxPower:   436,
		NormPower:  140,
		FTP:        156,
		IF:         0.8974,
		TSS:        118.17,
		VI:         1.138,
		HasPower:   true,
		HasIF:      true,
		HasTSS:     true,
		HasVI:      true,
	}, "hash-render", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}

	code, body := get("/")
	if code != http.StatusOK {
		t.Fatalf("GET /: код %d", code)
	}
	if !strings.Contains(body, "</html>") || strings.Contains(body, "template:") {
		t.Errorf("GET /: тело обрезано или содержит ошибку шаблона")
	}
	if !strings.Contains(body, "118.2 TSS") {
		t.Errorf("GET /: в строке тренировки нет TSS")
	}

	code, body = get(fmt.Sprintf("/workout/%d", id))
	if code != http.StatusOK || !strings.Contains(body, "Мощность") || !strings.Contains(body, "</html>") {
		t.Errorf("GET /workout: код %d, тело %d байт", code, len(body))
	}

	code, body = get("/profile")
	if code != http.StatusOK || !strings.Contains(body, "Профиль") || !strings.Contains(body, "</html>") {
		t.Errorf("GET /profile: код %d, тело %d байт", code, len(body))
	}

	code, body = get("/progress")
	if code != http.StatusOK || !strings.Contains(body, "CTL") || !strings.Contains(body, "heatmap") || !strings.Contains(body, "</html>") {
		t.Errorf("GET /progress: код %d, тело %d байт", code, len(body))
	}
}

// TestRenderErrorReturns500 — ошибка шаблона даёт 500, а не 200 с мусором.
func TestRenderErrorReturns500(t *testing.T) {
	rec := httptest.NewRecorder()
	render(rec, "no-such-template.html", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("код %d, хочу 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "</html>") {
		t.Error("в ответе остался HTML")
	}
}
