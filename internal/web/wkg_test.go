package web

import (
	"html"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

func f64(v float64) *float64 { return &v }

// TestBuildWkgTrend — изменения за 30/90/365 дней считаются от значений на
// эти даты; где истории нет — прочерк, а не ноль.
func TestBuildWkgTrend(t *testing.T) {
	today := store.StartOfDay(time.Now())
	points := []store.ProfilePoint{
		{Date: today.AddDate(0, 0, -365), WPerKgTrainer: f64(2.5)},
		{Date: today.AddDate(0, 0, -90), WPerKgTrainer: f64(2.4)},
		{Date: today.AddDate(0, 0, -30), WPerKgTrainer: f64(2.3)},
		{Date: today, WPerKgTrainer: f64(2.6)},
	}
	got := buildWkgTrend(points, today)
	if !got.HasValue || got.Value != 2.6 {
		t.Fatalf("текущее: %+v, хочу 2.6", got)
	}
	want := "30 дн +0.30 · 90 дн +0.20 · 365 дн +0.10"
	if got.Text != want {
		t.Errorf("текст = %q, хочу %q", got.Text, want)
	}

	// история короче: точки отсчёта нет — прочерк
	short := buildWkgTrend([]store.ProfilePoint{{Date: today, WPerKgTrainer: f64(2.6)}}, today)
	if !short.HasValue || short.Text != "30 дн — · 90 дн — · 365 дн —" {
		t.Errorf("короткая история: %+v", short)
	}

	// текущего значения нет — карточку не показываем
	if empty := buildWkgTrend(nil, today); empty.HasValue || empty.Text != "" {
		t.Errorf("пустая история: %+v", empty)
	}

	// точка отсчёта есть, но Вт/кг там null (не было веса/FTP) — тоже прочерк
	nullPrev := buildWkgTrend([]store.ProfilePoint{
		{Date: today.AddDate(0, 0, -30)},
		{Date: today, WPerKgTrainer: f64(2.6)},
	}, today)
	if !strings.HasPrefix(nullPrev.Text, "30 дн —") {
		t.Errorf("null в точке отсчёта: %q", nullPrev.Text)
	}

	// отрицательный ноль не показываем
	zero := buildWkgTrend([]store.ProfilePoint{
		{Date: today.AddDate(0, 0, -30), WPerKgTrainer: f64(2.6)},
		{Date: today, WPerKgTrainer: f64(2.6)},
	}, today)
	if !strings.HasPrefix(zero.Text, "30 дн 0.00") {
		t.Errorf("нулевое изменение: %q", zero.Text)
	}
}

// TestProgressWkgCard — на /progress карточка текущего Вт/кг (станок) и
// изменения за 30/90/365 дней; при короткой истории — прочерк.
func TestProgressWkgCard(t *testing.T) {
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
	add("weight", 75, 100)
	add("weight", 76, 50)
	add("ftp", 200, 100)
	add("ftp", 210, 20)

	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/progress", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	body := html.UnescapeString(rec.Body.String())
	if !strings.Contains(body, "Вт/кг (станок)") {
		t.Fatalf("нет карточки Вт/кг")
	}
	want := "30 дн +0.13 · 90 дн +0.10 · 365 дн —"
	if !strings.Contains(body, want) {
		t.Errorf("нет изменений %q", want)
	}
	if !strings.Contains(body, ">2.8</div>") {
		t.Errorf("нет текущего значения 2.8 (210/76)")
	}
}

// TestProgressWkgCardHiddenWithoutHistory — без истории профиля карточки нет,
// а не нули.
func TestProgressWkgCardHiddenWithoutHistory(t *testing.T) {
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
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/progress", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Вт/кг (станок)") {
		t.Errorf("карточка Вт/кг показана без истории профиля")
	}
}
