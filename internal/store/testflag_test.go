package store

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

// TestWorkoutTestFlag — флаг сохраняется, снимается и переживает ре-импорт
// (upsert того же файла не трогает пользовательское поле).
func TestWorkoutTestFlag(t *testing.T) {
	db := openTestDB(t)
	start := time.Now().Add(-time.Hour)
	id, err := InsertWorkout(db, &Workout{StartTime: start, Duration: time.Hour},
		"hash-test-flag", "/tmp/test-flag.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	if w, err := GetWorkout(db, id); err != nil || w.IsTest {
		t.Fatalf("новая тренировка: IsTest=%v, err=%v; хочу false", w.IsTest, err)
	}

	if err := SetWorkoutTest(db, id, true); err != nil {
		t.Fatal(err)
	}
	if w, err := GetWorkout(db, id); err != nil || !w.IsTest {
		t.Fatalf("после пометки: IsTest=%v, err=%v; хочу true", w.IsTest, err)
	}

	// ре-импорт того же файла: is_test не входит в INSERT/ON CONFLICT
	if _, err := InsertWorkout(db, &Workout{StartTime: start, Duration: time.Hour},
		"hash-test-flag", "/tmp/test-flag.fit", 2); err != nil {
		t.Fatal(err)
	}
	if w, err := GetWorkout(db, id); err != nil || !w.IsTest {
		t.Errorf("после ре-импорта: IsTest=%v, err=%v; флаг потерян", w.IsTest, err)
	}

	if err := SetWorkoutTest(db, id, false); err != nil {
		t.Fatal(err)
	}
	if w, err := GetWorkout(db, id); err != nil || w.IsTest {
		t.Errorf("после снятия: IsTest=%v, err=%v; хочу false", w.IsTest, err)
	}

	if err := SetWorkoutTest(db, 99999, true); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("неизвестный id: err=%v, хочу sql.ErrNoRows", err)
	}
}

// TestRecordsExcludeTests — рекорды и PR-флаги по умолчанию не видят тестовые
// тренировки; includeTests возвращает их.
func TestRecordsExcludeTests(t *testing.T) {
	db := openTestDB(t)
	now := time.Now()
	mk := func(hash string, start time.Time, recs ...PowerRecord) int64 {
		t.Helper()
		id, err := InsertWorkout(db, &Workout{StartTime: start, Duration: time.Hour},
			hash, "/tmp/"+hash+".fit", 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := SetWorkoutMMP(db, id, recs, 1); err != nil {
			t.Fatal(err)
		}
		return id
	}

	normal := mk("hash-tf-normal", now.AddDate(0, 0, -2),
		PowerRecord{WindowSec: 5, Watts: 300}, PowerRecord{WindowSec: 60, Watts: 200})
	testID := mk("hash-tf-test", now.AddDate(0, 0, -1),
		PowerRecord{WindowSec: 5, Watts: 400}, PowerRecord{WindowSec: 60, Watts: 250})
	if err := SetWorkoutTest(db, testID, true); err != nil {
		t.Fatal(err)
	}

	recs, err := BestRecords(db, time.Time{}, now.Add(time.Minute), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("%d окон, хочу 2", len(recs))
	}
	for _, r := range recs {
		if r.WorkoutID != normal || r.Watts != map[int]float64{5: 300, 60: 200}[r.WindowSec] {
			t.Errorf("окно %d без тестов: %+v, хочу тренировку %d", r.WindowSec, r, normal)
		}
	}

	recs, err = BestRecords(db, time.Time{}, now.Add(time.Minute), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.WorkoutID != testID {
			t.Errorf("окно %d с тестами: %+v, хочу тестовую %d", r.WindowSec, r, testID)
		}
	}

	// PR-флаг тестовой тренировки по умолчанию пуст: рекорд из замера — не рекорд
	prs, err := WorkoutPRs(db, testID, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 0 {
		t.Errorf("PR тестовой по умолчанию: %v, хочу пусто", prs)
	}
	prs, err = WorkoutPRs(db, testID, now, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs[5]) == 0 || prs[5][0] != "all" {
		t.Errorf("PR тестовой с includeTests: %v, хочу all", prs)
	}
	// рабочая тренировка остаётся рекордсменом, пока тест исключён
	prs, err = WorkoutPRs(db, normal, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs[5]) != 4 || prs[5][0] != "all" {
		t.Errorf("PR рабочей без тестов: %v, хочу все 4 периода", prs)
	}
}

// TestTimelineTestNames — тестовая тренировка остаётся в PMC (TSS, поездки),
// но попадает в отдельное поле для пометки «тест» в календаре.
func TestTimelineTestNames(t *testing.T) {
	withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	work, err := InsertWorkout(db, &Workout{
		StartTime: dayOf(t, 1), Duration: time.Hour, TSS: 60,
	}, "hash-tl-work", "/tmp/work.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	test, err := InsertWorkout(db, &Workout{
		StartTime: dayOf(t, 2), Duration: time.Hour, TSS: 40,
	}, "hash-tl-test", "/tmp/test.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	// name — пользовательское поле, InsertWorkout его не пишет
	if err := SetName(db, work, "работа"); err != nil {
		t.Fatal(err)
	}
	if err := SetName(db, test, "Ramp Test"); err != nil {
		t.Fatal(err)
	}
	if err := SetWorkoutTest(db, test, true); err != nil {
		t.Fatal(err)
	}

	days, err := Timeline(db, dayOf(t, 1), dayOf(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 {
		t.Fatalf("%d дней, хочу 2", len(days))
	}
	if days[0].TestNames != "" || days[1].TestNames != "Ramp Test" {
		t.Errorf("testNames: %q / %q", days[0].TestNames, days[1].TestNames)
	}
	if days[1].Rides != 1 || days[1].TSS != 40 {
		t.Errorf("тест выпал из PMC: %+v", days[1])
	}
	if days[1].Names != "Ramp Test" {
		t.Errorf("names: %q, хочу название теста", days[1].Names)
	}
}
