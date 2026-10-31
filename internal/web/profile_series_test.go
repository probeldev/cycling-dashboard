package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// TestProfileSeriesAPI — шаги по датам из истории, null для ещё не заданных
// значений, ряд начинается с первой записи, а не с начала окна.
func TestProfileSeriesAPI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	today := store.StartOfDay(time.Now())
	add := func(kind string, value float64, daysAgo int) {
		t.Helper()
		if err := store.AddProfileEntry(db, kind, value, today.AddDate(0, 0, -daysAgo)); err != nil {
			t.Fatal(err)
		}
	}
	add("weight", 75, 5)
	add("ftp", 156, 5)
	add("ftp", 170, 2)
	add("ftp_outdoor", 180, 1)

	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var points []store.ProfilePoint
	getJSON(t, h, "/api/profile/series?days=10", &points)
	if len(points) != 6 {
		t.Fatalf("%d точек, хочу 6 (с первой записи 5 дней назад)", len(points))
	}
	if !points[0].Date.Equal(today.AddDate(0, 0, -5)) || !points[5].Date.Equal(today) {
		t.Errorf("границы ряда: %v..%v", points[0].Date, points[5].Date)
	}
	if points[0].FTPTrainer == nil || *points[0].FTPTrainer != 156 || points[0].FTPOutdoor != nil {
		t.Errorf("первая точка: %+v, хочу FTP 156 без уличного", points[0])
	}
	if points[3].FTPTrainer == nil || *points[3].FTPTrainer != 170 {
		t.Errorf("шаг FTP не на 2 дня назад: %+v", points[3])
	}
	if points[4].FTPOutdoor == nil || *points[4].FTPOutdoor != 180 {
		t.Errorf("шаг уличного FTP не на 1 день назад: %+v", points[4])
	}

	// null должен быть честным null в JSON, а не 0
	var raw []map[string]any
	getJSON(t, h, "/api/profile/series?days=10", &raw)
	if v, ok := raw[0]["ftpOutdoor"]; !ok || v != nil {
		t.Errorf("ftpOutdoor первой точки = %v (есть: %v), хочу null", v, ok)
	}
	if v, ok := raw[0]["wPerKgOutdoor"]; !ok || v != nil {
		t.Errorf("wPerKgOutdoor первой точки = %v (есть: %v), хочу null", v, ok)
	}
	if v, ok := raw[0]["wPerKgTrainer"].(float64); !ok || v != 156.0/75 {
		t.Errorf("wPerKgTrainer = %v, хочу %v", raw[0]["wPerKgTrainer"], 156.0/75)
	}
}

// TestProfileSeriesAPIEmpty — без истории отдаётся [] (не null и не 365 null).
func TestProfileSeriesAPIEmpty(t *testing.T) {
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
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/profile/series", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("пустая история: %q, хочу []", got)
	}
}

// TestProfileSeriesDaysParam — days проверяется, значение по умолчанию 365.
func TestProfileSeriesDaysParam(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// запись давно: окно по умолчанию (365) её захватит, окно 30 — нет
	old := store.StartOfDay(time.Now()).AddDate(0, 0, -100)
	if err := store.AddProfileEntry(db, "weight", 75, old); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var points []store.ProfilePoint
	getJSON(t, h, "/api/profile/series", &points)
	if len(points) != 101 {
		t.Errorf("по умолчанию %d точек, хочу 101 (100 дней + сегодня)", len(points))
	}
	// старое значение действует и в коротком окне: ряд обрезан по дням,
	// но не начинается с null
	var short []store.ProfilePoint
	getJSON(t, h, "/api/profile/series?days=30", &short)
	if len(short) != 30 {
		t.Fatalf("days=30: %d точек, хочу 30", len(short))
	}
	if short[0].Weight == nil || *short[0].Weight != 75 {
		t.Errorf("days=30: вес первой точки %v, хочу 75 из старой записи", short[0].Weight)
	}

	for _, q := range []string{"days=0", "days=-1", "days=abc", "days=3661"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/profile/series?"+q, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: код %d, хочу 400", q, rec.Code)
		}
	}
}

// TestProfilePageWkgCards — текущие Вт/кг для станка и улицы на /profile,
// плюс контейнеры графиков.
func TestProfilePageWkgCards(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Now().AddDate(0, 0, -1)
	for kind, v := range map[string]float64{"weight": 75, "ftp": 150, "ftp_outdoor": 165} {
		if err := store.AddProfileEntry(db, kind, v, at); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profile", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Вт/кг (станок)", "Вт/кг (улица)", "profile-ftp", "profile-wkg", "profile-few", "Мало данных для графика", "!res.ok"} {
		if !strings.Contains(body, want) {
			t.Errorf("на /profile нет %q", want)
		}
	}
}
