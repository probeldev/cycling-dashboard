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

// TestTSBFormat — один знак после запятой, без «-0.0», подпись по округлённому
// значению согласована с порогами.
func TestTSBFormat(t *testing.T) {
	for _, tt := range []struct {
		in    float64
		text  string
		label string
	}{
		{-0.04, "0.0", "норма"},
		{-9.976, "-10.0", "норма"}, // ровно -10 ещё «норма», порог строгий
		{-10.14, "-10.1", "загружен"},
		{5.04, "5.0", "норма"},
		{5.06, "5.1", "свежий"},
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
	for i, w := range []struct {
		start time.Time
		tss   float64
		hash  string
	}{
		{ws.Add(12 * time.Hour), 100, "hash-prog-cur"},
		{ws.AddDate(0, 0, -7).Add(12 * time.Hour), 50, "hash-prog-prev"},
	} {
		_ = i
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

	// число и подпись TSB согласованы между собой
	today := store.StartOfDay(time.Now())
	days, err := store.Timeline(db, today, today)
	if err != nil {
		t.Fatal(err)
	}
	tsb := days[0].TSB
	if !strings.Contains(body, ">"+format1(round1(tsb))+"</div><div class=\"k\">TSB · "+tsbLabel(round1(tsb))) {
		t.Errorf("карточка TSB не совпадает с расчётом %v", tsb)
	}
}
