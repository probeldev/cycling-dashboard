package store

import (
	"math"
	"testing"
	"time"
)

func TestWeeksAggregation(t *testing.T) {
	db := openTestDB(t)
	ref := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local) // четверг
	mon := WeekStart(ref)
	a := insertTimelineWorkout(t, db, "hash-wk-a", mon.Add(10*time.Hour), 50, 60, 20, false)
	b := insertTimelineWorkout(t, db, "hash-wk-b", mon.AddDate(0, 0, 3).Add(10*time.Hour), 80, 30, 10, true)
	if err := SetWorkoutZones(db, a, [7]int{100, 200, 300, 0, 0, 0, 0}, 1); err != nil {
		t.Fatal(err)
	}
	if err := SetWorkoutZones(db, b, [7]int{50, 50, 100, 100, 0, 0, 0}, 1); err != nil {
		t.Fatal(err)
	}

	weeks, err := Weeks(db, 3, ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(weeks) != 3 {
		t.Fatalf("%d недель, хочу 3", len(weeks))
	}
	w := weeks[2] // текущая
	if !w.Start.Equal(mon) {
		t.Errorf("старт недели = %v, хочу %v", w.Start, mon)
	}
	if !near(w.TSS, 130) || !near(w.Hours, 1.5) || !near(w.DistanceKm, 30) || w.Rides != 2 {
		t.Errorf("агрегат недели: %+v", w)
	}
	if !near(w.TrainerHours, 0.5) || !near(w.OutdoorHours, 1) {
		t.Errorf("часы по типу: %v/%v", w.TrainerHours, w.OutdoorHours)
	}
	if w.Zones != [7]int{150, 250, 400, 100, 0, 0, 0} {
		t.Errorf("зоны: %v", w.Zones)
	}
	// total 900: low 400, gray 400, high 100
	if math.Abs(w.LowPct-44.4) > 0.05 || math.Abs(w.GrayPct-44.4) > 0.05 || math.Abs(w.HighPct-11.1) > 0.05 {
		t.Errorf("проценты: %v/%v/%v", w.LowPct, w.GrayPct, w.HighPct)
	}
	// предыдущие недели пустые
	for _, empty := range weeks[:2] {
		if empty.Rides != 0 || empty.TSS != 0 {
			t.Errorf("пустая неделя: %+v", empty)
		}
	}
}

// TestWeeksBoundary — воскресенье 23:00 и понедельник 00:30 — разные недели.
func TestWeeksBoundary(t *testing.T) {
	db := openTestDB(t)
	ref := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	mon := WeekStart(ref)
	sun := mon.AddDate(0, 0, -1)
	insertTimelineWorkout(t, db, "hash-wb-sun",
		time.Date(sun.Year(), sun.Month(), sun.Day(), 23, 0, 0, 0, time.Local), 40, 30, 10, false)
	insertTimelineWorkout(t, db, "hash-wb-mon", mon.Add(30*time.Minute), 60, 30, 10, false)

	weeks, err := Weeks(db, 2, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !near(weeks[0].TSS, 40) || weeks[0].Rides != 1 {
		t.Errorf("воскресенье попало не в прошлую неделю: %+v", weeks[0])
	}
	if !near(weeks[1].TSS, 60) || weeks[1].Rides != 1 {
		t.Errorf("понедельник попало не в текущую неделю: %+v", weeks[1])
	}
}

// TestWeeksEmpty — недели без тренировок присутствуют с нулями.
func TestWeeksEmpty(t *testing.T) {
	db := openTestDB(t)
	weeks, err := Weeks(db, 3, time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	if len(weeks) != 3 {
		t.Fatalf("%d недель, хочу 3", len(weeks))
	}
	for _, w := range weeks {
		if w.Rides != 0 || w.TSS != 0 || w.LowPct != 0 {
			t.Errorf("пустая неделя: %+v", w)
		}
	}
}

// TestZoneTimeByFTPKind — время в зонах группируется по базе FTP (ftp_kind),
// а не по типу тренировки: уличная с fallback на станковый FTP попадает в
// станковую базу, а тренировки без FTP в сумме не участвуют.
func TestZoneTimeByFTPKind(t *testing.T) {
	db := openTestDB(t)
	from := time.Date(2026, 10, 15, 0, 0, 0, 0, time.Local)
	to := from.AddDate(0, 0, 1)
	start := from.Add(time.Hour)
	add := func(hash, kind string, trainer bool, z [7]int) int64 {
		t.Helper()
		id := insertTimelineWorkout(t, db, hash, start, 50, 60, 20, trainer)
		if kind != "" {
			if _, err := db.Exec(`UPDATE workouts SET ftp_kind = ? WHERE id = ?`, kind, id); err != nil {
				t.Fatal(err)
			}
		}
		if err := SetWorkoutZones(db, id, z, 1); err != nil {
			t.Fatal(err)
		}
		return id
	}
	add("hash-zt-trainer", "trainer", true, [7]int{100, 0, 0, 0, 0, 0, 0})
	add("hash-zt-fallback", "trainer", false, [7]int{200, 0, 0, 0, 0, 0, 0})
	add("hash-zt-outdoor", "outdoor", false, [7]int{300, 0, 0, 0, 0, 0, 0})
	add("hash-zt-none", "", true, [7]int{400, 0, 0, 0, 0, 0, 0})

	trainer, err := ZoneTime(db, from, to, "trainer")
	if err != nil {
		t.Fatal(err)
	}
	if trainer[0] != 300 {
		t.Errorf("станковая база: Z1=%d, хочу 300 (тренировка + fallback)", trainer[0])
	}
	outdoor, err := ZoneTime(db, from, to, "outdoor")
	if err != nil {
		t.Fatal(err)
	}
	if outdoor[0] != 300 {
		t.Errorf("уличная база: Z1=%d, хочу 300", outdoor[0])
	}
	if got := trainer[1] + trainer[2] + trainer[3] + trainer[4] + trainer[5] + trainer[6]; got != 0 {
		t.Errorf("в станковую базу попали чужие зоны: %v", trainer)
	}
}

// TestSetWorkoutZonesTransactional — сбой на INSERT откатывает и DELETE:
// старые зоны и версия остаются нетронутыми.
func TestSetWorkoutZonesTransactional(t *testing.T) {
	db := openTestDB(t)
	id := insertWorkout(t, db, 1, func(int) int { return 100 })
	if err := SetWorkoutZones(db, id, [7]int{10, 0, 0, 0, 0, 0, 0}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_zones BEFORE INSERT ON workout_zones
	    BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	if err := SetWorkoutZones(db, id, [7]int{20, 0, 0, 0, 0, 0, 0}, 2); err == nil {
		t.Fatal("жду ошибку от триггера")
	}
	zones, err := WorkoutZones(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if zones[0] != 10 {
		t.Errorf("старые зоны потеряны: %v", zones)
	}
	var version int
	if err := db.QueryRow(`SELECT zones_version FROM workouts WHERE id = ?`, id).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Errorf("zones_version = %d, хочу 1 (откат)", version)
	}
}

// TestWeeksIgnoresFutureWorkout — будущая запись не раздувает текущую неделю
// ни TSS/поездками, ни зонами.
func TestWeeksIgnoresFutureWorkout(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	past := insertTimelineWorkout(t, db, "hash-wf-past", now.Add(-2*time.Hour), 30, 60, 20, false)
	future := insertTimelineWorkout(t, db, "hash-wf-future", now.Add(2*time.Hour), 1000, 240, 200, false)
	if err := SetWorkoutZones(db, past, [7]int{100, 0, 0, 0, 0, 0, 0}, 1); err != nil {
		t.Fatal(err)
	}
	if err := SetWorkoutZones(db, future, [7]int{0, 0, 1000, 0, 0, 0, 0}, 1); err != nil {
		t.Fatal(err)
	}

	weeks, err := Weeks(db, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	w := weeks[0]
	if !near(w.TSS, 30) || w.Rides != 1 {
		t.Errorf("неделя = %+v, хочу 30 TSS / 1 поездку", w)
	}
	if w.Zones[2] != 0 || w.Zones[0] != 100 {
		t.Errorf("зоны = %v, будущая серая зона попала в неделю", w.Zones)
	}
}
