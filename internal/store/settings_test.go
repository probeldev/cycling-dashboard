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
	if got.TSS != 320 || got.Source != TargetSourceManual {
		t.Errorf("WeeklyTarget = %+v, хочу override 320/manual", got)
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
	if got.TSS != 250 || got.Source != TargetSourceMedian4 {
		t.Errorf("WeeklyTarget = %+v, хочу 250/median4", got)
	}
}

func TestWeeklyTargetMedianSkewed(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	ws := WeekStart(now)
	// один выброс не должен тянуть цель за собой
	for i, tss := range []float64{100, 200, 300, 1000} {
		addWeekWorkout(t, db, ws.AddDate(0, 0, -7*(4-i)), 1, tss, "hash-target-skew-"+string(rune('a'+i)))
	}
	got, err := WeeklyTarget(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.TSS != 250 || got.Source != TargetSourceMedian4 {
		t.Errorf("WeeklyTarget = %+v, хочу 250/median4", got)
	}
}

func TestWeeklyTargetMedianIgnoresEmptyWeeks(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	ws := WeekStart(now)
	// история четыре недели, но нагрузка только в двух из них
	addWeekWorkout(t, db, ws.AddDate(0, 0, -28), 1, 0, "hash-target-gap-a")
	addWeekWorkout(t, db, ws.AddDate(0, 0, -14), 1, 220, "hash-target-gap-b")
	addWeekWorkout(t, db, ws.AddDate(0, 0, -7), 1, 220, "hash-target-gap-c")

	got, err := WeeklyTarget(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.TSS != 220 || got.Source != TargetSourceMedian4 {
		t.Errorf("WeeklyTarget = %+v, хочу 220/median4: пустые недели не тянут медиану вниз", got)
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
	if got.TSS != 180 || got.Source != TargetSourceLastFull {
		t.Errorf("WeeklyTarget = %+v, хочу 180/lastFull", got)
	}
}

func TestWeeklyTargetNoHistory(t *testing.T) {
	db := openTestDB(t)
	got, err := WeeklyTarget(db, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.TSS != 0 || got.Source != TargetSourceNone {
		t.Errorf("WeeklyTarget = %+v, хочу none", got)
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
	if got.TSS != 0 || got.Source != TargetSourceNone {
		t.Errorf("WeeklyTarget = %+v, хочу none: текущая неделя не может быть целью себе", got)
	}
}

func TestWeeklyTargetEmptyFullWeeksIsNone(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	ws := WeekStart(now)
	// история есть, но все 4 полные недели пусты; нагрузка только в текущей
	addWeekWorkout(t, db, ws.AddDate(0, 0, -35), 2, 0, "hash-target-empty-old")
	addWeekWorkout(t, db, ws, 1, 219.6, "hash-target-empty-current")

	got, err := WeeklyTarget(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.TSS != 0 || got.Source != TargetSourceNone {
		t.Errorf("WeeklyTarget = %+v, хочу none: полных недель с нагрузкой нет", got)
	}
}

func TestWeeklyTargetInvalidSettingFallsBack(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	ws := WeekStart(now)
	for i, tss := range []float64{100, 300, 200, 400} {
		addWeekWorkout(t, db, ws.AddDate(0, 0, -7*(4-i)), 1, tss, "hash-target-bad-"+string(rune('a'+i)))
	}
	for _, raw := range []string{"abc", "NaN", "Inf", "50", "2000"} {
		if _, err := db.Exec(`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, datetime('now'))
		    ON CONFLICT(key) DO UPDATE SET value = excluded.value`, weeklyTargetKey, raw); err != nil {
			t.Fatal(err)
		}
		got, err := WeeklyTarget(db, now)
		if err != nil {
			t.Fatal(err)
		}
		if got.TSS != 250 || got.Source != TargetSourceMedian4 {
			t.Errorf("setting %q: WeeklyTarget = %+v, хочу 250/median4", raw, got)
		}
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
		if got.TSS != v || got.Source != TargetSourceManual {
			t.Errorf("после SetWeeklyTarget(%v) WeeklyTarget = %+v", v, got)
		}
	}
}

func TestClearWeeklyTarget(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	ws := WeekStart(now)
	for i, tss := range []float64{100, 300, 200, 400} {
		addWeekWorkout(t, db, ws.AddDate(0, 0, -7*(4-i)), 1, tss, "hash-target-clear-"+string(rune('a'+i)))
	}
	if err := SetWeeklyTarget(db, 320); err != nil {
		t.Fatal(err)
	}
	if err := ClearWeeklyTarget(db); err != nil {
		t.Fatal(err)
	}
	got, err := WeeklyTarget(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.TSS != 250 || got.Source != TargetSourceMedian4 {
		t.Errorf("после сброса WeeklyTarget = %+v, хочу авто 250/median4", got)
	}
}

func TestCalendarDaysBetweenDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("нет tzdata")
	}
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, loc)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, loc)
	if got := calendarDaysBetween(from, to); got != 31 {
		t.Errorf("calendarDaysBetween = %d, хочу 31", got)
	}
}

// TestWeeklyTargetIgnoresFutureFirstWorkout — тренировка с будущим start_time
// не делает историю начатой: авто-цели пока нет.
func TestWeeklyTargetIgnoresFutureFirstWorkout(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	addWeekWorkout(t, db, WeekStart(now), 6, 220, "hash-target-future") // сегодня 12:00, now — 10:00
	got, err := WeeklyTarget(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != TargetSourceNone || got.TSS != 0 {
		t.Errorf("WeeklyTarget = %+v, хочу none: будущая тренировка не считается", got)
	}
}

// TestWeeklyTargetAutoRoundsToZeroIsNone — авто-цель 0.4 TSS округлилась бы
// до нуля; вместо «0 из 0» источник должен стать none.
func TestWeeklyTargetAutoRoundsToZeroIsNone(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	ws := WeekStart(now)
	addWeekWorkout(t, db, ws.AddDate(0, 0, -7), 2, 0.4, "hash-target-round-zero")
	got, err := WeeklyTarget(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != TargetSourceNone || got.TSS != 0 {
		t.Errorf("WeeklyTarget = %+v, хочу none/0", got)
	}
}
