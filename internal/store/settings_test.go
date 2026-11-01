package store

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

// addWeekWorkout — тренировка с TSS в указанный день недели (0 — понедельник).
func addWeekWorkout(t *testing.T, db DBTX, weekStart time.Time, day int, tss float64, hash string) {
	t.Helper()
	if _, err := InsertWorkout(db, &Workout{
		StartTime: weekStart.AddDate(0, 0, day).Add(12 * time.Hour),
		Duration:  time.Hour,
		TSS:       tss,
	}, hash, filepath.Join(t.TempDir(), hash+".fit"), 1); err != nil {
		t.Fatal(err)
	}
}

func TestWeeklyTargetOverride(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	ws := WeekStart(now)
	// история дала бы медиану 250, но явная настройка важнее
	for i, tss := range []float64{100, 200, 300, 400} {
		addWeekWorkout(t, db, ws.AddDate(0, 0, -7*(4-i)), 1, tss, "hash-target-override-"+string(rune('a'+i)))
	}
	if err := SetWeeklyTarget(db, 320); err != nil {
		t.Fatal(err)
	}
	got, err := WeeklyTarget(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if got != 320 {
		t.Errorf("WeeklyTarget = %v, хочу override 320", got)
	}
}

func TestWeeklyTargetMedianFourWeeks(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	ws := WeekStart(now)
	// полные недели: -4, -3, -2, -1; текущая (-0) не должна участвовать
	for i, tss := range []float64{100, 300, 200, 400} {
		addWeekWorkout(t, db, ws.AddDate(0, 0, -7*(4-i)), 1, tss, "hash-target-median-"+string(rune('a'+i)))
	}
	addWeekWorkout(t, db, ws, 1, 9000, "hash-target-median-current")

	got, err := WeeklyTarget(db, now)
	if err != nil {
		t.Fatal(err)
	}
	// медиана чётного набора 100,200,300,400 = (200+300)/2
	if got != 250 {
		t.Errorf("WeeklyTarget = %v, хочу 250", got)
	}
}

func TestMedian(t *testing.T) {
	for _, tt := range []struct {
		in   []float64
		want float64
	}{
		{nil, 0},
		{[]float64{42}, 42},
		{[]float64{300, 100, 200}, 200},            // нечётный набор
		{[]float64{400, 100, 300, 200}, 250},       // чётный: среднее центральных
		{[]float64{10, 20, 30, 40}, 25},            // порядок не важен
		{[]float64{5, 1, 3, 9, 7}, 5},              // нечётный после сортировки
		{[]float64{1, 2, 100, 200, 300, 400}, 150}, // чётный с разбросом
	} {
		if got := median(tt.in); math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("median(%v) = %v, хочу %v", tt.in, got, tt.want)
		}
	}
}

func TestWeeklyTargetFewerThanFourWeeks(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	ws := WeekStart(now)
	lastFull := ws.AddDate(0, 0, -7)
	// история всего 2 полные недели: медиану не считаем, берём последнюю
	addWeekWorkout(t, db, lastFull.AddDate(0, 0, -7), 2, 100, "hash-target-short-a")
	addWeekWorkout(t, db, lastFull, 3, 180, "hash-target-short-b")

	got, err := WeeklyTarget(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if got != 180 {
		t.Errorf("WeeklyTarget = %v, хочу TSS последней полной недели 180", got)
	}
}

func TestWeeklyTargetNoHistory(t *testing.T) {
	db := openTestDB(t)
	got, err := WeeklyTarget(db, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got != defaultWeeklyTarget {
		t.Errorf("WeeklyTarget = %v, хочу %v", got, defaultWeeklyTarget)
	}
}

func TestWeeklyTargetOnlyCurrentWeek(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	addWeekWorkout(t, db, WeekStart(now), 1, 220, "hash-target-current-only")

	got, err := WeeklyTarget(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if got != defaultWeeklyTarget {
		t.Errorf("WeeklyTarget = %v, хочу дефолт %v: полных недель ещё нет", got, defaultWeeklyTarget)
	}
}

func TestSetWeeklyTargetBounds(t *testing.T) {
	db := openTestDB(t)
	for _, v := range []float64{99.9, 99, 0, -1, 1500.1, 2000, math.NaN(), math.Inf(1)} {
		if err := SetWeeklyTarget(db, v); err == nil {
			t.Errorf("SetWeeklyTarget(%v): ошибки нет, хочу отказ", v)
		}
	}
	for _, v := range []float64{100, 450.5, 1500} {
		if err := SetWeeklyTarget(db, v); err != nil {
			t.Errorf("SetWeeklyTarget(%v): %v", v, err)
		}
		got, err := WeeklyTarget(db, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if got != v {
			t.Errorf("после SetWeeklyTarget(%v) WeeklyTarget = %v", v, got)
		}
	}
}
