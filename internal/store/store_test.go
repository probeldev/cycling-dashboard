package store

import (
	"database/sql"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return openTestDBAt(t, filepath.Join(t.TempDir(), "test.db"))
}

// openTestDBAt — база по конкретному пути, чтобы переоткрыть её и прогнать миграции.
func openTestDBAt(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// addOrphan — сэмпл без существующей тренировки.
func addOrphan(t *testing.T, db *sql.DB, workoutID int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO samples (workout_id, ts_offset, power) VALUES (?, 0, 100)`, workoutID); err != nil {
		t.Fatal(err)
	}
}

func countSamples(t *testing.T, db *sql.DB, where string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM samples WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// insertWorkout — тренировка с n сэмплами по 1 с; power задаёт мощность записи.
func insertWorkout(t *testing.T, db *sql.DB, n int, power func(i int) int) int64 {
	t.Helper()
	id, err := InsertWorkout(db, &Workout{
		StartTime: time.Now(), Duration: time.Duration(n) * time.Second,
	}, "hash-"+t.Name(), filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]Sample, n)
	for i := range samples {
		samples[i] = Sample{Offset: int64(i), Power: power(i), HasPwr: true}
	}
	if err := InsertSamples(db, id, samples); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSamplesBound(t *testing.T) {
	db := openTestDB(t)
	id := insertWorkout(t, db, 10, func(i int) int { return i })

	if got, err := CountSamples(db, id); err != nil || got != 10 {
		t.Fatalf("CountSamples = %d, %v; хочу 10, nil", got, err)
	}
	for _, tt := range []struct{ max, want int }{
		{1, 1}, {3, 3}, {10, 10}, {100, 10},
	} {
		got, err := Samples(db, id, tt.max)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != tt.want {
			t.Errorf("max=%d: %d точек, хочу %d", tt.max, len(got), tt.want)
		}
	}

	// усреднение по бакету, а не выборка каждой N-й: среднее 0..9 = 4.5 → 4
	one, err := Samples(db, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if one[0].Power != 4 {
		t.Errorf("средняя по бакету = %d Вт, хочу 4 (AVG 0..9)", one[0].Power)
	}

	// maxN <= 0 не должен ронять делением на ноль
	if got, err := Samples(db, id, 0); err != nil || len(got) != 1 {
		t.Errorf("maxN=0: %d точек, %v; хочу 1 точку", len(got), err)
	}
}

func TestRawSamplesKeepsPeaks(t *testing.T) {
	db := openTestDB(t)
	id := insertWorkout(t, db, 100, func(i int) int {
		if i == 50 {
			return 900
		}
		return 100
	})

	raw, err := RawSamples(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 100 {
		t.Fatalf("%d точек, хочу 100", len(raw))
	}
	if raw[50].Power != 900 {
		t.Errorf("пик = %d Вт, хочу 900", raw[50].Power)
	}

	bucketed, err := Samples(db, id, 10)
	if err != nil {
		t.Fatal(err)
	}
	peak := 0
	for _, s := range bucketed {
		peak = max(peak, s.Power)
	}
	if peak >= 900 {
		t.Errorf("при усреднении пик не размазался: %d Вт", peak)
	}
}

func TestRawSamplesNulls(t *testing.T) {
	db := openTestDB(t)
	id := insertWorkout(t, db, 1, func(int) int { return 0 })
	// вторая запись — полная дыра: все датчики отвалились
	if err := InsertSamples(db, id, []Sample{{Offset: 5}}); err != nil {
		t.Fatal(err)
	}

	raw, err := RawSamples(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2 {
		t.Fatalf("%d точек, хочу 2", len(raw))
	}
	if !raw[0].HasPwr || raw[0].Power != 0 {
		t.Errorf("честный ноль потерялся: %+v", raw[0])
	}
	if raw[1].HasPwr || raw[1].HasCad || raw[1].HasPos || raw[1].HasAlt || raw[1].HasDist {
		t.Errorf("дыра превратилась в данные: %+v", raw[1])
	}
}

func TestSamplesEmptyWorkout(t *testing.T) {
	db := openTestDB(t)
	id, err := InsertWorkout(db, &Workout{StartTime: time.Now()},
		"hash-empty", filepath.Join(t.TempDir(), "empty.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Samples(db, id, 500); err != nil || got == nil || len(got) != 0 {
		t.Errorf("Samples без сэмплов: %v, %v; хочу пустой (не nil) срез", got, err)
	}
	if got, err := RawSamples(db, id); err != nil || got == nil || len(got) != 0 {
		t.Errorf("RawSamples без сэмплов: %v, %v; хочу пустой (не nil) срез", got, err)
	}
}

// TestSamplesBucketOffsetIsMean — offset точки — среднее по записям бакета,
// а не MIN: иначе после длинной паузы внутри бакета точка садится на его край.
func TestSamplesBucketOffsetIsMean(t *testing.T) {
	db := openTestDB(t)
	id, err := InsertWorkout(db, &Workout{StartTime: time.Now(), Duration: 105 * time.Second},
		"hash-bucket-mean", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	var samples []Sample
	for i := 0; i < 5; i++ { // до паузы
		samples = append(samples, Sample{Offset: int64(i), Power: 100, HasPwr: true})
	}
	for i := 0; i < 5; i++ { // после паузы в 96 с
		samples = append(samples, Sample{Offset: int64(100 + i), Power: 100, HasPwr: true})
	}
	if err := InsertSamples(db, id, samples); err != nil {
		t.Fatal(err)
	}

	one, err := Samples(db, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 {
		t.Fatalf("%d точек, хочу 1", len(one))
	}
	// среднее (0+1+2+3+4+100+101+102+103+104)/10 = 52
	if one[0].Offset != 52 {
		t.Errorf("offset точки = %d, хочу 52 (среднее по бакету, а не MIN)", one[0].Offset)
	}
}

// TestCleanupOrphanSamples — миграция удаляет сэмплы без тренировки и не трогает валидные.
func TestCleanupOrphanSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db := openTestDBAt(t, path)
	id := insertWorkout(t, db, 3, func(i int) int { return 100 })
	addOrphan(t, db, 0)   // старый импорт писал workout_id=0
	addOrphan(t, db, 999) // ссылка на удалённую тренировку
	db.Close()

	reopened := openTestDBAt(t, path)
	if got := countSamples(t, reopened, "workout_id = ?", id); got != 3 {
		t.Errorf("валидных сэмплов %d, хочу 3", got)
	}
	if got := countSamples(t, reopened, "workout_id NOT IN (SELECT id FROM workouts)"); got != 0 {
		t.Errorf("сирот осталось %d, хочу 0", got)
	}
}

// TestCleanupOrphanSamplesEmptyWorkouts — если workouts пуста, миграция не трогает
// сэмплы: иначе недосозданная или частично восстановленная база потеряла бы всё.
func TestCleanupOrphanSamplesEmptyWorkouts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db := openTestDBAt(t, path)
	addOrphan(t, db, 0)
	db.Close()

	reopened := openTestDBAt(t, path)
	if got := countSamples(t, reopened, "1=1"); got != 1 {
		t.Errorf("после миграции %d сэмплов, хочу 1 (guard при пустой workouts)", got)
	}
}

// TestProfileEntryDoesNotTouchSamples — добавление и удаление записи профиля
// не должны запускать чистку сэмплов.
func TestProfileEntryDoesNotTouchSamples(t *testing.T) {
	db := openTestDB(t)
	id := insertWorkout(t, db, 3, func(i int) int { return 100 })
	addOrphan(t, db, 0)
	before := countSamples(t, db, "1=1")

	if err := AddProfileEntry(db, "weight", 75, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := countSamples(t, db, "1=1"); got != before {
		t.Fatalf("AddProfileEntry удалил сэмплы: было %d, стало %d", before, got)
	}

	history, err := ProfileHistory(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("история профиля: %d записей, хочу 1", len(history))
	}
	if err := DeleteProfileEntry(db, history[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := countSamples(t, db, "1=1"); got != before {
		t.Fatalf("DeleteProfileEntry удалил сэмплы: было %d, стало %d", before, got)
	}
	if got := countSamples(t, db, "workout_id = ?", id); got != 3 {
		t.Errorf("валидных сэмплов %d, хочу 3", got)
	}
}

// TestSetNormPowersRollback — ошибка на середине пачки откатывает всю транзакцию,
// иначе база осталась бы с частично обновлённым NP.
func TestSetNormPowersRollback(t *testing.T) {
	db := openTestDB(t)
	first := insertWorkout(t, db, 1, func(int) int { return 100 })
	second := insertWorkout(t, db, 1, func(int) int { return 100 })

	// триггер валит UPDATE второй тренировки
	if _, err := db.Exec(fmt.Sprintf(`CREATE TRIGGER fail_np BEFORE UPDATE ON workouts
	    WHEN NEW.id = %d BEGIN SELECT RAISE(ABORT, 'boom'); END`, second)); err != nil {
		t.Fatal(err)
	}

	err := SetNormPowers(db, []NormPowerUpdate{
		{ID: first, NP: 111, Version: 9},
		{ID: second, NP: 222, Version: 9},
	})
	if err == nil {
		t.Fatal("жду ошибку от триггера")
	}

	got, err := GetWorkout(db, first)
	if err != nil {
		t.Fatal(err)
	}
	if got.NormPower != 0 || got.NPVersion != 0 {
		t.Errorf("первая тренировка не откатилась: NP=%d версия=%d", got.NormPower, got.NPVersion)
	}
}

// TestListTreeMonthMetrics — месячный агрегат суммирует TSS и считает средний IF,
// взвешенный по времени тренировок.
func TestListTreeMonthMetrics(t *testing.T) {
	db := openTestDB(t)
	jan := func(day int) time.Time { return time.Date(2026, 1, day, 10, 0, 0, 0, time.Local) }
	for _, w := range []struct {
		hash string
		w    Workout
	}{
		{"hash-m1", Workout{StartTime: jan(10), Duration: time.Hour, TSS: 100, IF: 1}},
		{"hash-m2", Workout{StartTime: jan(20), Duration: 30 * time.Minute, TSS: 20, IF: 0.8}},
	} {
		if _, err := InsertWorkout(db, &w.w, w.hash, "/tmp/"+w.hash+".fit", 1); err != nil {
			t.Fatal(err)
		}
	}

	years, err := ListTree(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(years) != 1 || len(years[0].Months) != 1 {
		t.Fatalf("дерево: %d лет, хочу 1 год с 1 месяцем", len(years))
	}
	m := years[0].Months[0]
	if math.Abs(m.TSS-120) > 1e-9 {
		t.Errorf("TSS за месяц = %v, хочу 120", m.TSS)
	}
	// sqrt(Σ(IF²·t)/Σt) = sqrt(120 / (1.5·100)) = sqrt(0.8)
	wantIF := math.Sqrt((1.0*1.0*3600 + 0.8*0.8*1800) / 5400.0)
	if math.Abs(m.AvgIF-wantIF) > 1e-9 {
		t.Errorf("AvgIF = %v, хочу %v", m.AvgIF, wantIF)
	}
	for _, r := range m.Rides {
		if r.TSS == 0 || r.IF == 0 {
			t.Errorf("в строке тренировки нет метрик: %+v", r)
		}
	}
}

// TestWorkoutDataGapsPct — процент пропусков считается от duration (не от
// elapsed) и остаётся вещественным, чтобы 5.4% не терялись при округлении.
func TestWorkoutDataGapsPct(t *testing.T) {
	w := Workout{Duration: 1000 * time.Second, ElapsedSec: 5000, NoPowerSec: 54}
	if got := w.DataGapsPct(); math.Abs(got-5.4) > 1e-9 {
		t.Errorf("DataGapsPct = %v, хочу 5.4 (база — duration)", got)
	}
	w = Workout{Duration: 100 * time.Second, NoPowerSec: 6}
	if got := w.DataGapsPct(); math.Abs(got-6) > 1e-9 {
		t.Errorf("DataGapsPct = %v, хочу 6", got)
	}
	if got := (Workout{Duration: 0, NoPowerSec: 10}).DataGapsPct(); got != 0 {
		t.Errorf("DataGapsPct без длительности = %v, хочу 0", got)
	}
}
