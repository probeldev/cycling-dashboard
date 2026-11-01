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

func TestRenderTodayAndRidesRoutes(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now().AddDate(0, 0, -1), Duration: time.Hour, TSS: 80,
		Trainer: true, AvgPower: 150, AvgCadence: 85,
	}, "hash-today-rides", filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}

	code, body := get("/")
	if code != http.StatusOK || !strings.Contains(body, "</html>") {
		t.Fatalf("GET /: код %d, тело %d байт", code, len(body))
	}
	for _, want := range []string{"Сегодня", "Рекомендация", "Форма (CTL)", "Усталость (ATL)",
		"Свежесть (TSB)", "Последняя тренировка", "Неделя", "Готовность", "Поездки"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /: нет блока %q", want)
		}
	}
	for _, bad := range []string{"NaN", "<no value>", "<b>0 Вт</b>", ">0 Вт<"} {
		if strings.Contains(body, bad) {
			t.Errorf("GET /: в вёрстке %q", bad)
		}
	}

	code, body = get("/rides")
	if code != http.StatusOK || !strings.Contains(body, "Поездки") || !strings.Contains(body, "</html>") {
		t.Errorf("GET /rides: код %d, тело %d байт", code, len(body))
	}
}

func TestRenderEmptyToday(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: код %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Мало данных", "Цель не задана", "Тренировок пока нет"} {
		if !strings.Contains(body, want) {
			t.Errorf("пустая главная: нет %q", want)
		}
	}
	for _, bad := range []string{"NaN", "<no value>", "0 из 0"} {
		if strings.Contains(body, bad) {
			t.Errorf("пустая главная: в вёрстке %q", bad)
		}
	}
}

func TestRenderTodayWithTargetAndFTP(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	if err := store.AddProfileEntry(db, "ftp", 200, now.AddDate(0, 0, -7)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertWorkout(db, &store.Workout{
		StartTime: now.AddDate(0, 0, -1), Duration: time.Hour, TSS: 100,
		Trainer: true, AvgPower: 150,
	}, "hash-today-target", filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
		t.Fatal(err)
	}
	if err := store.SetWeeklyTarget(db, 300); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: код %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"300", "вручную", "осталось", "≈", "110–150"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET / с целью: нет %q", want)
		}
	}
}
