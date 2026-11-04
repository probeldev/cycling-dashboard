package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/advice"
	"cycling-dashboard/internal/store"
)

func hasAdviceCard(cards []advice.Card, title string) bool {
	for _, c := range cards {
		if c.Title == title {
			return true
		}
	}
	return false
}

// TestWorkoutAdviceBlock — на деталке карточки инсайтов идут сразу после шапки
// без отдельного заголовка, по фактическим данным тренировки, не больше пяти.
func TestWorkoutAdviceBlock(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now().Add(-time.Hour), Duration: time.Hour,
		AvgCadence: 66, AvgCadenceActive: 66, AvgPower: 120, MaxPower: 300,
	}, "hash-advice", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
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
	for _, want := range []string{"Низкий каденс", "Пульса нет"} {
		if !strings.Contains(body, want) {
			t.Errorf("на деталке нет %q", want)
		}
	}
	if strings.Contains(body, "Инсайты") {
		t.Error("заголовок «Инсайты» убран, карточки идут сразу после шапки")
	}
	if n := strings.Count(body, `class="insight insight--`); n > 5 {
		t.Errorf("карточек %d, хочу не больше 5", n)
	}
	if strings.Contains(body, "Рваная езда") || strings.Contains(body, "Пропуски данных") {
		t.Errorf("карточка без данных: %q", body)
	}
}

// TestWorkoutAdviceContext — контекст собирается из БД: вчерашний TSS, TSB на
// день тренировки, серая зона недели и decoupling по сэмплам.
func TestWorkoutAdviceContext(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	day := store.WeekStart(time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local)).Add(12 * time.Hour)
	// 10 тяжёлых дней до тренировки: вчерашний TSS > 80 и TSB < -20
	for i := 10; i >= 1; i-- {
		if _, err := store.InsertWorkout(db, &store.Workout{
			StartTime: day.AddDate(0, 0, -i), Duration: time.Hour, TSS: 150,
		}, fmt.Sprintf("hash-advice-ctx-%d", i), filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
			t.Fatal(err)
		}
	}
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: day, Duration: time.Hour, TSS: 150,
		AvgCadence: 85, AvgPower: 150, NormPower: 150, HasPower: true,
		VI: 1.0, AvgHR: 130, MaxHR: 160, Trainer: true,
	}, "hash-advice-ctx-main", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]store.Sample, 600)
	for i := range samples {
		samples[i] = store.Sample{Offset: int64(i), Power: 200, HasPwr: true, HR: 140}
	}
	if err := store.InsertSamples(db, id, samples); err != nil {
		t.Fatal(err)
	}
	// 30% серой зоны в единственной тренировке текущей недели
	if err := store.SetWorkoutZones(db, id, [7]int{700, 0, 300, 0, 0, 0, 0}, 1); err != nil {
		t.Fatal(err)
	}
	w, err := store.GetWorkout(db, id)
	if err != nil {
		t.Fatal(err)
	}
	cards, err := workoutAdvice(db, w)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Два тяжёлых дня подряд", "Накопленная усталость",
		"Много серой зоны", "Ровная работа",
	} {
		if !hasAdviceCard(cards, want) {
			t.Errorf("нет карточки %q: %+v", want, cards)
		}
	}
}
