package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func insertReadinessWorkout(t *testing.T, db *sql.DB, hash string, start time.Time,
	km float64, dur time.Duration, tss float64, calories, ascent int) int64 {
	t.Helper()
	id, err := InsertWorkout(db, &Workout{
		StartTime: start, Duration: dur, DistanceKm: km, TSS: tss,
		Calories: calories, AscentM: ascent,
	}, hash, filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestReadinessWindows — окна 7/28/90 дней считаются по локальным дням,
// включая сегодня. Поездка в 00:00 первого дня входит, в 23:59 дня перед
// ним — уже нет. Калории взвешены по времени, набор — на 100 км.
func TestReadinessWindows(t *testing.T) {
	loc := withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	now := time.Date(2026, 11, 1, 18, 30, 0, 0, loc)
	today := StartOfDay(now)
	at := func(daysAgo, hour, min int) time.Time {
		return today.AddDate(0, 0, -daysAgo).
			Add(time.Duration(hour)*time.Hour + time.Duration(min)*time.Minute)
	}

	insertReadinessWorkout(t, db, "hash-rd-a", at(6, 0, 0), 10, 2*time.Hour, 40, 400, 100)
	insertReadinessWorkout(t, db, "hash-rd-b", at(7, 23, 59), 20, time.Hour, 20, 200, 50)
	insertReadinessWorkout(t, db, "hash-rd-c", at(27, 0, 0), 30, time.Hour, 30, 300, 150)
	insertReadinessWorkout(t, db, "hash-rd-d", at(28, 23, 59), 40, time.Hour, 10, 0, 0)
	e := insertReadinessWorkout(t, db, "hash-rd-e", at(89, 0, 0), 50, 30*time.Minute, 5, 0, 0)
	f := insertReadinessWorkout(t, db, "hash-rd-f", at(90, 23, 59), 60, time.Hour, 1, 0, 0)

	r, err := Readiness(db, now)
	if err != nil {
		t.Fatal(err)
	}

	if r.Rolling7.Rides != 1 || !near(r.Rolling7.KM, 10) ||
		!near(r.Rolling7.Hours, 2) || !near(r.Rolling7.TSS, 40) {
		t.Errorf("7 дней: %+v, хочу 10 км / 2 ч / 40 TSS / 1 поездку", r.Rolling7)
	}
	if r.Rolling28.Rides != 3 || !near(r.Rolling28.KM, 60) ||
		!near(r.Rolling28.Hours, 4) || !near(r.Rolling28.TSS, 90) {
		t.Errorf("28 дней: %+v, хочу 60 км / 4 ч / 90 TSS / 3 поездки", r.Rolling28)
	}

	if r.Longest90d == nil || !near(r.Longest90d.KM, 50) || r.Longest90d.WorkoutID != e {
		t.Errorf("макс. за 90 дней: %+v, хочу 50 км (id %d)", r.Longest90d, e)
	} else if !r.Longest90d.Date.Equal(at(89, 0, 0)) {
		t.Errorf("дата макс. за 90 дней: %v, хочу %v", r.Longest90d.Date, at(89, 0, 0))
	}
	if r.LongestEver == nil || !near(r.LongestEver.KM, 60) || r.LongestEver.WorkoutID != f {
		t.Errorf("макс. за всё время: %+v, хочу 60 км (id %d)", r.LongestEver, f)
	}
	if r.CenturyCount != 0 {
		t.Errorf("поездок 100+ км: %d, хочу 0", r.CenturyCount)
	}

	if r.KcalPerHour == nil || !near(*r.KcalPerHour, 225) {
		t.Errorf("ккал/ч: %v, хочу 225 (900 ккал / 4 ч)", r.KcalPerHour)
	}
	if r.AscentPer100km == nil || !near(*r.AscentPer100km, 200) {
		t.Errorf("набор на 100 км: %v, хочу 200 (300 м / 150 км)", r.AscentPer100km)
	}
	// пары A/B, C/D и E/F идут подряд, но длиннее двух дней серий нет
	if r.LongestStreak != 2 {
		t.Errorf("серия дней: %d, хочу 2", r.LongestStreak)
	}
}

// TestReadinessExcludesTests — замеры не участвуют ни в окнах, ни в рекордах,
// ни в ккал/ч и наборе: это не рабочая нагрузка.
func TestReadinessExcludesTests(t *testing.T) {
	withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.Local)
	today := StartOfDay(now)
	insertReadinessWorkout(t, db, "hash-rd-work", today.AddDate(0, 0, -1).Add(12*time.Hour),
		10, time.Hour, 20, 100, 10)
	testID := insertReadinessWorkout(t, db, "hash-rd-test", today.AddDate(0, 0, -2).Add(12*time.Hour),
		150, 5*time.Hour, 300, 1000, 500)
	if err := SetWorkoutTest(db, testID, true); err != nil {
		t.Fatal(err)
	}

	r, err := Readiness(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if r.Rolling7.Rides != 1 || !near(r.Rolling7.KM, 10) {
		t.Errorf("7 дней с замером: %+v, хочу одну рабочую на 10 км", r.Rolling7)
	}
	if r.Longest90d == nil || !near(r.Longest90d.KM, 10) || r.Longest90d.WorkoutID == testID {
		t.Errorf("макс. за 90 дней: %+v, замер попал в рекорд", r.Longest90d)
	}
	if r.LongestEver == nil || !near(r.LongestEver.KM, 10) {
		t.Errorf("макс. за всё время: %+v, хочу 10 км без замера", r.LongestEver)
	}
	if r.CenturyCount != 0 {
		t.Errorf("100+ км: %d, замер не должен считаться", r.CenturyCount)
	}
	if r.KcalPerHour == nil || !near(*r.KcalPerHour, 100) {
		t.Errorf("ккал/ч: %v, хочу 100 без замера", r.KcalPerHour)
	}
	if r.AscentPer100km == nil || !near(*r.AscentPer100km, 100) {
		t.Errorf("набор на 100 км: %v, хочу 100 без замера", r.AscentPer100km)
	}
	if r.LongestStreak != 1 {
		t.Errorf("серия дней: %d, хочу 1 (замер не продлевает)", r.LongestStreak)
	}
}

// TestReadinessKcalWeighted — ккал/ч взвешены по времени, а не среднее
// арифметическое часовых норм; поездки без калорий знаменатель не раздувают.
func TestReadinessKcalWeighted(t *testing.T) {
	withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.Local)
	today := StartOfDay(now)
	insertReadinessWorkout(t, db, "hash-rd-kcal-1", today.AddDate(0, 0, -1).Add(time.Hour),
		0, 2*time.Hour, 40, 1000, 0)
	insertReadinessWorkout(t, db, "hash-rd-kcal-2", today.AddDate(0, 0, -2).Add(time.Hour),
		0, time.Hour, 20, 100, 0)
	insertReadinessWorkout(t, db, "hash-rd-kcal-none", today.AddDate(0, 0, -3).Add(time.Hour),
		0, 4*time.Hour, 10, 0, 0)

	r, err := Readiness(db, now)
	if err != nil {
		t.Fatal(err)
	}
	// 1100 ккал за 3 часа, а не (500 + 100 + 0) / 3
	if r.KcalPerHour == nil || !near(*r.KcalPerHour, 1100.0/3) {
		t.Errorf("ккал/ч: %v, хочу %v", r.KcalPerHour, 1100.0/3)
	}
	if r.AscentPer100km != nil {
		t.Errorf("набор на 100 км без дистанции: %v, хочу null", *r.AscentPer100km)
	}
}

// TestReadinessCenturyThreshold — 100+ км считаются включительно, замеры — нет.
func TestReadinessCenturyThreshold(t *testing.T) {
	withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.Local)
	today := StartOfDay(now)
	insertReadinessWorkout(t, db, "hash-rd-100", today.AddDate(0, 0, -1).Add(time.Hour),
		100, 5*time.Hour, 100, 0, 0)
	insertReadinessWorkout(t, db, "hash-rd-999", today.AddDate(0, 0, -2).Add(time.Hour),
		99.9, 5*time.Hour, 100, 0, 0)
	long := insertReadinessWorkout(t, db, "hash-rd-120", today.AddDate(0, 0, -3).Add(time.Hour),
		120, 6*time.Hour, 120, 0, 0)
	testID := insertReadinessWorkout(t, db, "hash-rd-200-test", today.AddDate(0, 0, -4).Add(time.Hour),
		200, 8*time.Hour, 200, 0, 0)
	if err := SetWorkoutTest(db, testID, true); err != nil {
		t.Fatal(err)
	}

	r, err := Readiness(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if r.CenturyCount != 2 {
		t.Errorf("100+ км: %d, хочу 2 (100 и 120, без замера 200)", r.CenturyCount)
	}
	if r.LongestEver == nil || !near(r.LongestEver.KM, 120) || r.LongestEver.WorkoutID != long {
		t.Errorf("макс. за всё время: %+v, хочу 120 км (id %d)", r.LongestEver, long)
	}
}

// TestReadinessStreak — самая длинная серия подряд идущих дней за год;
// замер не продлевает серию, день ровно на границе года входит.
func TestReadinessStreak(t *testing.T) {
	withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.Local)
	today := StartOfDay(now)
	at := func(daysAgo int) time.Time {
		return today.AddDate(0, 0, -daysAgo).Add(12 * time.Hour)
	}
	for i, daysAgo := range []int{0, 1, 2} {
		insertReadinessWorkout(t, db, "hash-rd-streak-"+string(rune('a'+i)), at(daysAgo),
			10, time.Hour, 10, 0, 0)
	}
	testID := insertReadinessWorkout(t, db, "hash-rd-streak-test", at(3), 10, time.Hour, 10, 0, 0)
	if err := SetWorkoutTest(db, testID, true); err != nil {
		t.Fatal(err)
	}
	for i, daysAgo := range []int{10, 11, 12, 13, 14} {
		insertReadinessWorkout(t, db, "hash-rd-streak-b"+string(rune('0'+i)), at(daysAgo),
			10, time.Hour, 10, 0, 0)
	}
	insertReadinessWorkout(t, db, "hash-rd-streak-edge", at(364), 10, time.Hour, 10, 0, 0)

	r, err := Readiness(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if r.LongestStreak != 5 {
		t.Errorf("серия дней: %d, хочу 5", r.LongestStreak)
	}
}

// TestReadinessEmpty — пустая БД: пустые значения null, счётчики нули.
func TestReadinessEmpty(t *testing.T) {
	withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	r, err := Readiness(db, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Longest90d != nil || r.LongestEver != nil ||
		r.KcalPerHour != nil || r.AscentPer100km != nil {
		t.Errorf("пустая БД: %+v, пустые значения должны быть null", r)
	}
	if r.Rolling7.Rides != 0 || r.Rolling28.Rides != 0 ||
		r.LongestStreak != 0 || r.CenturyCount != 0 {
		t.Errorf("пустая БД: счётчики не нули: %+v", r)
	}
}

// TestReadinessIgnoresFutureWorkout — будущая тренировка не участвует ни в
// longestEver/centuryCount, ни в скользящих окнах.
func TestReadinessIgnoresFutureWorkout(t *testing.T) {
	loc := withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, loc)
	past := insertReadinessWorkout(t, db, "hash-rf-past", now.Add(-2*time.Hour), 50, 2*time.Hour, 80, 600, 200)
	if _, err := InsertWorkout(db, &Workout{
		StartTime: now.Add(2 * time.Hour), Duration: 5 * time.Hour,
		DistanceKm: 200, TSS: 1000, Calories: 4000, AscentM: 2000,
	}, "hash-rf-future", filepath.Join(t.TempDir(), "f.fit"), 1); err != nil {
		t.Fatal(err)
	}

	r, err := Readiness(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if r.LongestEver == nil || r.LongestEver.WorkoutID != past || !near(r.LongestEver.KM, 50) {
		t.Errorf("longestEver = %+v, хочу 50 км (id %d)", r.LongestEver, past)
	}
	if r.CenturyCount != 0 {
		t.Errorf("centuryCount = %d, хочу 0", r.CenturyCount)
	}
	if r.Rolling7.Rides != 1 || !near(r.Rolling7.KM, 50) || !near(r.Rolling7.TSS, 80) {
		t.Errorf("rolling7 = %+v, хочу 50 км / 80 TSS / 1 поездку", r.Rolling7)
	}
}
