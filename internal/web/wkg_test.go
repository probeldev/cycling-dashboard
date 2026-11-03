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
	want := []wkgTrendWindow{
		{Label: "30 дн", Change: "+0.30"},
		{Label: "90 дн", Change: "+0.20"},
		{Label: "365 дн", Change: "+0.10"},
	}
	if len(got.Windows) != len(want) {
		t.Fatalf("окон %d, хочу %d: %+v", len(got.Windows), len(want), got.Windows)
	}
	for i := range want {
		if got.Windows[i] != want[i] {
			t.Errorf("окно %d = %+v, хочу %+v", i, got.Windows[i], want[i])
		}
	}

	// история короче: точки отсчёта нет — прочерк и пояснение
	short := buildWkgTrend([]store.ProfilePoint{{Date: today, WPerKgTrainer: f64(2.6)}}, today)
	if !short.HasValue {
		t.Fatalf("короткая история скрыта: %+v", short)
	}
	for _, w := range short.Windows {
		if w.Change != "—" {
			t.Errorf("короткая история, %s = %q, хочу прочерк", w.Label, w.Change)
		}
	}
	if !strings.Contains(short.Note, "Изменения появятся") {
		t.Errorf("нет пояснения к прочеркам: %q", short.Note)
	}

	// текущего значения нет — карточку не показываем
	if empty := buildWkgTrend(nil, today); empty.HasValue || len(empty.Windows) != 0 {
		t.Errorf("пустая история: %+v", empty)
	}

	// точка отсчёта есть, но Вт/кг там null (не было веса/FTP) — тоже прочерк
	nullPrev := buildWkgTrend([]store.ProfilePoint{
		{Date: today.AddDate(0, 0, -365), WPerKgTrainer: f64(2.4)},
		{Date: today.AddDate(0, 0, -90), WPerKgTrainer: f64(2.5)},
		{Date: today.AddDate(0, 0, -30)},
		{Date: today, WPerKgTrainer: f64(2.6)},
	}, today)
	if nullPrev.Windows[0].Change != "—" {
		t.Errorf("null в точке отсчёта: %+v", nullPrev.Windows[0])
	}
	if !strings.Contains(nullPrev.Note, "Прочерк") {
		t.Errorf("нет пояснения к частичному прочерку: %q", nullPrev.Note)
	}

	// отрицательный ноль не показываем
	zero := buildWkgTrend([]store.ProfilePoint{
		{Date: today.AddDate(0, 0, -30), WPerKgTrainer: f64(2.6)},
		{Date: today, WPerKgTrainer: f64(2.6)},
	}, today)
	if zero.Windows[0].Change != "0.00" {
		t.Errorf("нулевое изменение: %+v", zero.Windows[0])
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
	for _, want := range []string{">30 дн</span> +0.13", ">90 дн</span> +0.10", ">365 дн</span> —"} {
		if !strings.Contains(body, want) {
			t.Errorf("нет изменения %q", want)
		}
	}
	if strings.Contains(body, "30 дн — · 90 дн — · 365 дн —") {
		t.Error("изменения Вт/кг остались в виде coverage-шаблона")
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
