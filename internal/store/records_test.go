package store

import (
	"math"
	"reflect"
	"testing"
	"time"
)

// TestBestRecords — лучшее по окну за период; при равенстве побеждает более
// ранняя тренировка.
func TestBestRecords(t *testing.T) {
	db := openTestDB(t)
	day := func(d int) time.Time { return time.Date(2026, 10, d, 12, 0, 0, 0, time.Local) }
	mk := func(hash string, start time.Time, records ...PowerRecord) int64 {
		t.Helper()
		id, err := InsertWorkout(db, &Workout{StartTime: start, Duration: time.Hour},
			hash, "/tmp/"+hash+".fit", 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := SetWorkoutMMP(db, id, records, 1); err != nil {
			t.Fatal(err)
		}
		return id
	}

	a := mk("hash-rec-1", day(1), PowerRecord{WindowSec: 5, Watts: 300}, PowerRecord{WindowSec: 60, Watts: 200})
	b := mk("hash-rec-2", day(2), PowerRecord{WindowSec: 5, Watts: 350}, PowerRecord{WindowSec: 60, Watts: 150})

	recs, err := BestRecords(db, time.Time{}, day(10), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("%d окон, хочу 2", len(recs))
	}
	if recs[0].WindowSec != 5 || math.Abs(recs[0].Watts-350) > 1e-9 || recs[0].WorkoutID != b {
		t.Errorf("5 с: %+v, хочу b/350", recs[0])
	}
	if recs[1].WindowSec != 60 || math.Abs(recs[1].Watts-200) > 1e-9 || recs[1].WorkoutID != a {
		t.Errorf("60 с: %+v, хочу a/200", recs[1])
	}

	// равное значение позже — рекорд остаётся у более ранней
	c := mk("hash-rec-3", day(3), PowerRecord{WindowSec: 5, Watts: 350})
	recs, err = BestRecords(db, time.Time{}, day(10), false)
	if err != nil {
		t.Fatal(err)
	}
	if recs[0].WorkoutID != b {
		t.Errorf("при равенстве рекорд не у ранней: id=%d (b=%d, c=%d)", recs[0].WorkoutID, b, c)
	}

	// период: в [day(3), day(10)) остаётся только c
	recs, err = BestRecords(db, day(3), day(10), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].WorkoutID != c || recs[0].WindowSec != 5 {
		t.Errorf("период: %+v, хочу только c/5", recs)
	}

	// границы: [from, to) — запись ровно на from входит, ровно на to — нет
	d := mk("hash-rec-edge", day(5), PowerRecord{WindowSec: 5, Watts: 400})
	if recs, err := BestRecords(db, day(5), day(6), false); err != nil || len(recs) != 1 || recs[0].WorkoutID != d {
		t.Errorf("from включительно: %+v, %v", recs, err)
	}
	if recs, err := BestRecords(db, day(4), day(5), false); err != nil || len(recs) != 0 {
		t.Errorf("to исключительно: %+v, %v", recs, err)
	}
}

// TestBestRecordsUntil — верхняя граница включительная: запись ровно в
// until входит, будущая — нет.
func TestBestRecordsUntil(t *testing.T) {
	db := openTestDB(t)
	now := time.Now()
	mk := func(hash string, start time.Time, watts float64) int64 {
		t.Helper()
		id, err := InsertWorkout(db, &Workout{StartTime: start, Duration: time.Hour},
			hash, "/tmp/"+hash+".fit", 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := SetWorkoutMMP(db, id, []PowerRecord{{WindowSec: 60, Watts: watts}}, 1); err != nil {
			t.Fatal(err)
		}
		return id
	}
	atNow := mk("hash-until-now", now, 300)
	mk("hash-until-future", now.Add(2*time.Hour), 500)

	recs, err := BestRecordsUntil(db, time.Time{}, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].WorkoutID != atNow {
		t.Errorf("записи до now: %+v, хочу только %d", recs, atNow)
	}
}

// TestWorkoutPRs — рекордные окна по периодам; равенство отдаётся ранней.
func TestWorkoutPRs(t *testing.T) {
	db := openTestDB(t)
	now := time.Now()
	id, err := InsertWorkout(db, &Workout{StartTime: now.Add(-time.Hour), Duration: time.Hour},
		"hash-pr-1", "/tmp/pr.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetWorkoutMMP(db, id, []PowerRecord{
		{WindowSec: 5, Watts: 300}, {WindowSec: 60, Watts: 200},
	}, 1); err != nil {
		t.Fatal(err)
	}
	prs, err := WorkoutPRs(db, id, now, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []int{5, 60} {
		got := prs[w]
		if len(got) != 4 || got[0] != "all" || got[1] != "365d" || got[2] != "90d" || got[3] != "30d" {
			t.Errorf("окно %d: %v, хочу [all 365d 90d 30d]", w, got)
		}
	}
}

// TestWorkoutPRsIgnoreFuture — будущая тренировка не отнимает PR у прошлой и
// до наступления не получает собственных флагов.
func TestWorkoutPRsIgnoreFuture(t *testing.T) {
	db := openTestDB(t)
	now := time.Now()
	mk := func(hash string, start time.Time, watts float64) int64 {
		t.Helper()
		id, err := InsertWorkout(db, &Workout{StartTime: start, Duration: time.Hour},
			hash, "/tmp/"+hash+".fit", 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := SetWorkoutMMP(db, id, []PowerRecord{
			{WindowSec: 60, Watts: watts},
			{WindowSec: 300, Watts: watts - 50},
			{WindowSec: 1200, Watts: watts - 100},
		}, 1); err != nil {
			t.Fatal(err)
		}
		return id
	}

	past := mk("hash-pr-future-past", now.Add(-24*time.Hour), 300)
	before, err := WorkoutPRs(db, past, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(before[60]) == 0 || before[60][0] != "all" {
		t.Fatalf("у прошлой тренировки нет PR: %v", before)
	}

	future := mk("hash-pr-future", now.Add(2*time.Hour), 500)
	after, err := WorkoutPRs(db, past, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Errorf("PR прошлой изменились от будущей:\nдо  %v\nпосле %v", before, after)
	}

	prs, err := WorkoutPRs(db, future, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 0 {
		t.Errorf("PR будущей тренировки: %v, хочу пусто", prs)
	}

	recs, err := BestRecordsUntil(db, time.Time{}, now, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.WorkoutID == future {
			t.Errorf("окно %d: будущая тренировка попала в рекорды", r.WindowSec)
		}
	}
}
