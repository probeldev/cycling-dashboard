package web

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// TestTSBFormat — один знак после запятой, без «-0.0», подпись по округлённому
// значению согласована с порогами.
func TestTSBFormat(t *testing.T) {
	for _, tt := range []struct {
		in    float64
		text  string
		label string
	}{
		{-0.04, "0.0", "норма"},
		{-9.976, "−10.0", "норма"}, // ровно -10 ещё «норма», порог строгий
		{-10.14, "−10.1", "загружен"},
		{-25.14, "−25.1", "перегружен"},
		{5.06, "5.1", "норма"},
		{10.06, "10.1", "свежий"},
	} {
		v := round1(tt.in)
		if got := format1(v); got != tt.text {
			t.Errorf("format1(%v) = %q, хочу %q", tt.in, got, tt.text)
		}
		if got := tsbLabel(v); got != tt.label {
			t.Errorf("tsbLabel(%v) = %q, хочу %q", tt.in, got, tt.label)
		}
	}
}

// TestProgressCards — карточка «TSS за 4 недели» включает текущую неделю.
func TestProgressCards(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws := store.WeekStart(time.Now())
	for _, w := range []struct {
		start time.Time
		tss   float64
		hash  string
	}{
		{ws.Add(12 * time.Hour), 100, "hash-prog-cur"},
		{ws.AddDate(0, 0, -7).Add(12 * time.Hour), 50, "hash-prog-prev"},
	} {
		if _, err := store.InsertWorkout(db, &store.Workout{
			StartTime: w.start, Duration: time.Hour, TSS: w.tss,
		}, w.hash, filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/progress", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, ">150</div><div class=\"k\">TSS за 4 недели") {
		t.Errorf("карточка 4 недель не включает текущую (жду 150)")
	}
	if !strings.Contains(body, ">100</div><div class=\"k\">TSS за неделю") {
		t.Errorf("карточка недели не показывает 100")
	}

	// число и подпись TSB согласованы между собой; PMC — на конец прошлого дня
	now := time.Now()
	today := store.StartOfDay(now)
	days, err := store.TimelineUntil(db, today.AddDate(0, 0, -1), today, now)
	if err != nil {
		t.Fatal(err)
	}
	pmcDay := days[len(days)-1]
	if len(days) >= 2 {
		pmcDay = days[len(days)-2]
	}
	tsb := round1(round1(pmcDay.CTL) - round1(pmcDay.ATL))
	if !strings.Contains(body, ">"+format1(tsb)+"</div><div class=\"k\">TSB · "+tsbLabel(tsb)) {
		t.Errorf("карточка TSB не совпадает с расчётом %v", tsb)
	}
}

// TestProgressPMCWatchesStatus — карточка PMC на /progress показывает тот же
// момент, что и /api/status: конец предыдущего дня. Иначе CTL−ATL не равно TSB
// (сегодняшние CTL/ATL смешивались бы со свежестью до нагрузки сегодня).
func TestProgressPMCWatchesStatus(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	today := store.StartOfDay(now)
	for i := 1; i <= 3; i++ {
		if _, err := store.InsertWorkout(db, &store.Workout{
			StartTime: today.AddDate(0, 0, -i).Add(12 * time.Hour),
			Duration:  time.Hour,
			TSS:       70,
		}, fmt.Sprintf("hash-progress-pmc-%d", i), filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/progress", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	status, err := buildStatus(db, now, false)
	if err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, want := range []string{
		">" + trimFloat(status.State.CTL.Value) + "</div><div class=\"k\">CTL · форма",
		">" + trimFloat(status.State.ATL.Value) + "</div><div class=\"k\">ATL · усталость",
		">" + format1(status.State.TSB.Value) + "</div><div class=\"k\">TSB · " +
			tsbLabel(status.State.TSB.Value),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("на /progress нет %q", want)
		}
	}
	if diff := math.Abs(status.State.CTL.Value - status.State.ATL.Value - status.State.TSB.Value); diff > 0.1 {
		t.Errorf("CTL−ATL−TSB = %.1f, хочу 0", diff)
	}
}

// TestProgressBikeStats — блок велосипедов: часы включают станок, километры —
// только реальную дистанцию (у станковой тренировки distance = 0).
func TestProgressBikeStats(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bikes, err := store.Bikes(db)
	if err != nil || len(bikes) == 0 {
		t.Fatalf("велосипеды: %v, %v", bikes, err)
	}
	bike := bikes[0].ID
	for _, w := range []struct {
		hash    string
		km      float64
		dur     time.Duration
		trainer bool
	}{
		{"hash-progress-bike-road", 30, 2 * time.Hour, false},
		{"hash-progress-bike-trainer", 0, time.Hour, true},
	} {
		id, err := store.InsertWorkout(db, &store.Workout{
			StartTime: time.Now(), Duration: w.dur, DistanceKm: w.km, Trainer: w.trainer,
		}, w.hash, filepath.Join(t.TempDir(), "x.fit"), 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetWorkoutBike(db, id, &bike); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/progress", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Пробег по велосипедам") {
		t.Fatalf("нет блока велосипедов")
	}
	want := "<td>" + bikes[0].Name + "</td><td>2</td><td>30.0</td><td>3.0</td>"
	if !strings.Contains(body, want) {
		t.Errorf("строка велосипеда не совпала: хочу %q", want)
	}
}

// TestProgressFourWeekBounds — в «4 недели» входит начало недели ws-21,
// а ws-28 — уже нет.
func TestProgressFourWeekBounds(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws := store.WeekStart(time.Now())
	for _, w := range []struct {
		start time.Time
		tss   float64
		hash  string
	}{
		{ws.Add(12 * time.Hour), 10, "hash-b-cur"},                         // текущая
		{ws.AddDate(0, 0, -21).Add(12 * time.Hour), 20, "hash-b-edge-in"},  // ровно ws-21 — входит
		{ws.AddDate(0, 0, -28).Add(12 * time.Hour), 40, "hash-b-edge-out"}, // ws-28 — не входит
	} {
		if _, err := store.InsertWorkout(db, &store.Workout{
			StartTime: w.start, Duration: time.Hour, TSS: w.tss,
		}, w.hash, filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/progress", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	// 10 + 20 = 30: включаем ровно 4 недели (текущая + 3), без ws-28
	if !strings.Contains(rec.Body.String(), ">30</div><div class=\"k\">TSS за 4 недели") {
		t.Errorf("границы 4 недель неверные (жду 30, без 40)")
	}
}
