package store

import (
	"database/sql"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
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

// seq делает хеши файлов уникальными: два insertWorkout в одном тесте должны
// создавать две тренировки, а не перезаписывать одну.
var seq int

// insertWorkout — тренировка с n сэмплами по 1 с; power задаёт мощность записи.
func insertWorkout(t *testing.T, db *sql.DB, n int, power func(i int) int) int64 {
	t.Helper()
	seq++
	id, err := InsertWorkout(db, &Workout{
		StartTime: time.Now(), Duration: time.Duration(n) * time.Second,
	}, "hash-"+t.Name()+"-"+strconv.Itoa(seq), filepath.Join(t.TempDir(), "x.fit"), 1)
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

func TestHasTrackNeedsTwoPoints(t *testing.T) {
	db := openTestDB(t)
	id := insertWorkout(t, db, 0, func(int) int { return 0 })

	check := func(label string, want bool) {
		t.Helper()
		got, err := HasTrack(db, id)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: HasTrack = %v, хочу %v", label, got, want)
		}
	}

	check("0 точек", false)
	if err := InsertSamples(db, id, []Sample{
		{Offset: 0, Lat: 55.75, Lon: 37.61, HasPos: true},
	}); err != nil {
		t.Fatal(err)
	}
	check("1 точка", false)
	if err := InsertSamples(db, id, []Sample{{Offset: 60}}); err != nil {
		t.Fatal(err)
	}
	check("точка без координат", false)
	if err := InsertSamples(db, id, []Sample{
		{Offset: 120, Lat: 55.76, Lon: 37.62, HasPos: true},
	}); err != nil {
		t.Fatal(err)
	}
	check("2 точки", true)
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

// TestListTreeMonthMetrics — месячный агрегат суммирует TSS и считает средний IF
// как квадратичное среднее sqrt(Σ(IF²·t)/Σt).
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

// TestWorkoutHasDataGaps — порог строго больше 5% и только при наличии мощности.
func TestWorkoutHasDataGaps(t *testing.T) {
	for _, tt := range []struct {
		name string
		w    Workout
		want bool
	}{
		{"ровно 5%", Workout{HasPower: true, Duration: 100 * time.Second, NoPowerSec: 5}, false},
		{"5.1%", Workout{HasPower: true, Duration: 1000 * time.Second, NoPowerSec: 51}, true},
		{"без мощности", Workout{Duration: 100 * time.Second, NoPowerSec: 50}, false},
	} {
		if got := tt.w.HasDataGaps(); got != tt.want {
			t.Errorf("%s: HasDataGaps = %v, хочу %v (%.1f%%)", tt.name, got, tt.want, tt.w.DataGapsPct())
		}
	}
}

// TestProfileRollbackOnStampError — ошибка при пересчёте штампов откатывает
// и саму запись профиля: в profile_history ничего не остаётся.
func TestProfileRollbackOnStampError(t *testing.T) {
	db := openTestDB(t)
	insertWorkout(t, db, 1, func(int) int { return 100 })
	if _, err := db.Exec(`CREATE TRIGGER fail_stamp BEFORE UPDATE ON workouts
	    BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	if err := AddProfileEntry(db, "ftp", 200, time.Now()); err == nil {
		t.Fatal("жду ошибку от триггера")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM profile_history`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("в profile_history %d записей, хочу 0 (откат)", n)
	}
}

// TestProfileAtTieBreak — две записи с одинаковым recorded_at разрешаются по
// id: действует более поздняя. Так же и для fallback на самую раннюю запись.
func TestProfileAtTieBreak(t *testing.T) {
	db := openTestDB(t)
	at := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	if err := AddProfileEntry(db, "ftp", 150, at); err != nil {
		t.Fatal(err)
	}
	if err := AddProfileEntry(db, "ftp", 160, at); err != nil {
		t.Fatal(err)
	}
	if got, ok := ProfileAt(db, "ftp", at.Add(time.Hour)); !ok || got != 160 {
		t.Errorf("на момент после записей: %v ok=%v, хочу 160", got, ok)
	}
	if got, ok := ProfileAt(db, "ftp", at.Add(-time.Hour)); !ok || got != 160 {
		t.Errorf("fallback до записей: %v ok=%v, хочу 160 (поздняя по id)", got, ok)
	}
}

// TestProfileAtStrictTieBreak — строгий поиск не откатывается на самую раннюю
// запись, а при одинаковом recorded_at выбирает более позднюю по id.
func TestProfileAtStrictTieBreak(t *testing.T) {
	db := openTestDB(t)
	at := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	if err := AddProfileEntry(db, "ftp_outdoor", 150, at); err != nil {
		t.Fatal(err)
	}
	if err := AddProfileEntry(db, "ftp_outdoor", 160, at); err != nil {
		t.Fatal(err)
	}
	if _, ok := profileAtStrict(db, "ftp_outdoor", at.Add(-time.Hour)); ok {
		t.Error("строгий поиск до первой записи вернул значение, хочу «нет»")
	}
	if got, ok := profileAtStrict(db, "ftp_outdoor", at); !ok || got != 160 {
		t.Errorf("строгий поиск на recorded_at: %v ok=%v, хочу 160", got, ok)
	}
	if got, ok := profileAtStrict(db, "ftp_outdoor", at.Add(time.Hour)); !ok || got != 160 {
		t.Errorf("строгий поиск после записей: %v ok=%v, хочу 160", got, ok)
	}
}

// TestProfileStampsByKind — FTP станка и улицы берутся из разных видов профиля,
// у улицы при отсутствии ftp_outdoor — fallback на станковый.
func TestProfileStampsByKind(t *testing.T) {
	db := openTestDB(t)
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 12, 0, 0, 0, time.UTC) }
	if err := AddProfileEntry(db, "ftp", 156, day(1, 1)); err != nil {
		t.Fatal(err)
	}
	trainer, err := InsertWorkout(db, &Workout{
		StartTime: day(6, 1), Duration: time.Hour, Trainer: true,
	}, "hash-kind-tr", "/tmp/tr.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	outdoor, err := InsertWorkout(db, &Workout{
		StartTime: day(6, 2), Duration: time.Hour,
	}, "hash-kind-out", "/tmp/out.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	// уличного FTP ещё нет: обе тренировки на станковом
	for _, id := range []int64{trainer, outdoor} {
		w, err := GetWorkout(db, id)
		if err != nil {
			t.Fatal(err)
		}
		if w.FTP != 156 || w.FTPKind != "trainer" {
			t.Errorf("id=%d: FTP=%d kind=%q, хочу 156/trainer (fallback)", id, w.FTP, w.FTPKind)
		}
	}

	// появился уличный FTP — у улицы он, у станка по-прежнему станковый
	if err := AddProfileEntry(db, "ftp_outdoor", 170, day(5, 1)); err != nil {
		t.Fatal(err)
	}
	tr, _ := GetWorkout(db, trainer)
	out, _ := GetWorkout(db, outdoor)
	if tr.FTP != 156 || tr.FTPKind != "trainer" {
		t.Errorf("станок: FTP=%d kind=%q, хочу 156/trainer", tr.FTP, tr.FTPKind)
	}
	if out.FTP != 170 || out.FTPKind != "outdoor" {
		t.Errorf("улица: FTP=%d kind=%q, хочу 170/outdoor", out.FTP, out.FTPKind)
	}
}

// TestFTPChangeInvalidatesOnlyOwnKind — смена уличного FTP инвалидирует только
// уличные тренировки, смена станкового при явном уличном — только станковые.
func TestFTPChangeInvalidatesOnlyOwnKind(t *testing.T) {
	db := openTestDB(t)
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 12, 0, 0, 0, time.UTC) }
	if err := AddProfileEntry(db, "ftp", 156, day(1, 1)); err != nil {
		t.Fatal(err)
	}
	trainer, _ := InsertWorkout(db, &Workout{StartTime: day(6, 1), Duration: time.Hour, Trainer: true},
		"hash-inv-tr", "/tmp/tr.fit", 1)
	outdoor, _ := InsertWorkout(db, &Workout{StartTime: day(6, 2), Duration: time.Hour},
		"hash-inv-out", "/tmp/out.fit", 1)
	if err := BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workouts SET metrics_version = 1, norm_power = 140, avg_power = 123`); err != nil {
		t.Fatal(err)
	}

	// появился уличный FTP — инвалидируется только улица
	if err := AddProfileEntry(db, "ftp_outdoor", 180, day(2, 1)); err != nil {
		t.Fatal(err)
	}
	if got := metricsVersionOf(t, db, outdoor); got != 0 {
		t.Errorf("улица: metrics_version=%d, хочу 0 (FTP изменился)", got)
	}
	if got := metricsVersionOf(t, db, trainer); got != 1 {
		t.Errorf("станок: metrics_version=%d, хочу 1 (уличный FTP не влияет)", got)
	}

	// пересчёт возвращает актуальность обеим (здесь — эмуляция успешного
	// бэкфилла); смена станкового FTP при явном уличном не должна трогать улицу
	if _, err := db.Exec(`UPDATE workouts SET metrics_version = 1`); err != nil {
		t.Fatal(err)
	}
	if got := metricsVersionOf(t, db, trainer); got != 1 {
		t.Fatalf("после бэкфилла станок: metrics_version=%d, хочу 1", got)
	}
	if err := AddProfileEntry(db, "ftp", 160, day(3, 1)); err != nil {
		t.Fatal(err)
	}
	if got := metricsVersionOf(t, db, trainer); got != 0 {
		t.Errorf("станок: metrics_version=%d, хочу 0 (FTP изменился)", got)
	}
	if got := metricsVersionOf(t, db, outdoor); got != 1 {
		t.Errorf("улица: metrics_version=%d, хочу 1 (у неё свой FTP 180)", got)
	}
}

// TestFTPFallbackInvalidation — улица без своего FTP живёт на станковом:
// его изменение инвалидирует и уличные тренировки.
func TestFTPFallbackInvalidation(t *testing.T) {
	db := openTestDB(t)
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 12, 0, 0, 0, time.UTC) }
	if err := AddProfileEntry(db, "ftp", 156, day(1, 1)); err != nil {
		t.Fatal(err)
	}
	outdoor, _ := InsertWorkout(db, &Workout{StartTime: day(6, 1), Duration: time.Hour},
		"hash-fb-out", "/tmp/out.fit", 1)
	if err := BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workouts SET metrics_version = 1, norm_power = 140`); err != nil {
		t.Fatal(err)
	}
	if err := AddProfileEntry(db, "ftp", 200, day(2, 1)); err != nil {
		t.Fatal(err)
	}
	if got := metricsVersionOf(t, db, outdoor); got != 0 {
		t.Errorf("улица на fallback: metrics_version=%d, хочу 0", got)
	}
	w, _ := GetWorkout(db, outdoor)
	if w.FTP != 200 || w.FTPKind != "trainer" {
		t.Errorf("после fallback: FTP=%d kind=%q, хочу 200/trainer", w.FTP, w.FTPKind)
	}
}

// TestFTPKindOnlyChangeKeepsMetrics — уличный FTP равен станковому: меняется
// только источник (kind), значение то же — метрики не инвалидируются.
func TestFTPKindOnlyChangeKeepsMetrics(t *testing.T) {
	db := openTestDB(t)
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 12, 0, 0, 0, time.UTC) }
	if err := AddProfileEntry(db, "ftp", 156, day(1, 1)); err != nil {
		t.Fatal(err)
	}
	outdoor, _ := InsertWorkout(db, &Workout{StartTime: day(6, 1), Duration: time.Hour},
		"hash-kind-only", "/tmp/out.fit", 1)
	if err := BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workouts SET metrics_version = 1, norm_power = 140`); err != nil {
		t.Fatal(err)
	}
	if err := AddProfileEntry(db, "ftp_outdoor", 156, day(2, 1)); err != nil {
		t.Fatal(err)
	}
	w, _ := GetWorkout(db, outdoor)
	if w.FTP != 156 || w.FTPKind != "outdoor" {
		t.Errorf("FTP=%d kind=%q, хочу 156/outdoor (источник обновился)", w.FTP, w.FTPKind)
	}
	if got := metricsVersionOf(t, db, outdoor); got != 1 {
		t.Errorf("metrics_version=%d, хочу 1: значение FTP не изменилось", got)
	}
}

// TestBothFTPsRemoved — удалили оба вида FTP: штамп NULL, kind пустой,
// метрики обнуляются.
func TestBothFTPsRemoved(t *testing.T) {
	db := openTestDB(t)
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 12, 0, 0, 0, time.UTC) }
	for _, e := range []struct {
		kind  string
		value float64
	}{{"ftp", 156}, {"ftp_outdoor", 170}} {
		if err := AddProfileEntry(db, e.kind, e.value, day(1, 1)); err != nil {
			t.Fatal(err)
		}
	}
	trainer, _ := InsertWorkout(db, &Workout{StartTime: day(6, 1), Duration: time.Hour, Trainer: true,
		NormPower: 140, AvgPower: 123}, "hash-both-tr", "/tmp/tr.fit", 1)
	outdoor, _ := InsertWorkout(db, &Workout{StartTime: day(6, 2), Duration: time.Hour,
		NormPower: 140, AvgPower: 123}, "hash-both-out", "/tmp/out.fit", 1)
	if err := BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workouts SET metrics_version = 1`); err != nil {
		t.Fatal(err)
	}

	history, err := ProfileHistory(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range history {
		if err := DeleteProfileEntry(db, e.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{trainer, outdoor} {
		var ftpNull int
		if err := db.QueryRow(`SELECT COUNT(*) FROM workouts WHERE id = ? AND ftp_w IS NULL`, id).Scan(&ftpNull); err != nil {
			t.Fatal(err)
		}
		if ftpNull != 1 {
			t.Errorf("id=%d: ftp_w не NULL после удаления обоих FTP", id)
		}
		w, _ := GetWorkout(db, id)
		if w.FTPKind != "" || w.MetricsVersion != 0 {
			t.Errorf("id=%d: kind=%q version=%d, хочу пустой kind и 0", id, w.FTPKind, w.MetricsVersion)
		}
	}
}

func metricsVersionOf(t *testing.T, db *sql.DB, id int64) int {
	t.Helper()
	var v int
	if err := db.QueryRow(`SELECT metrics_version FROM workouts WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestSetTrainerOverrideRestampsFTP — смена типа тренировки перевыбирает
// источник FTP и инвалидирует метрики.
func TestSetTrainerOverrideRestampsFTP(t *testing.T) {
	db := openTestDB(t)
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 12, 0, 0, 0, time.UTC) }
	if err := AddProfileEntry(db, "ftp", 156, day(1, 1)); err != nil {
		t.Fatal(err)
	}
	if err := AddProfileEntry(db, "ftp_outdoor", 170, day(1, 1)); err != nil {
		t.Fatal(err)
	}
	id, err := InsertWorkout(db, &Workout{StartTime: day(6, 1), Duration: time.Hour},
		"hash-override", "/tmp/o.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workouts SET metrics_version = 1, norm_power = 140 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	if err := SetTrainerOverride(db, id, true); err != nil {
		t.Fatal(err)
	}
	w, _ := GetWorkout(db, id)
	if w.FTP != 156 || w.FTPKind != "trainer" || w.MetricsVersion != 0 {
		t.Errorf("после станка: FTP=%d kind=%q version=%d, хочу 156/trainer/0",
			w.FTP, w.FTPKind, w.MetricsVersion)
	}
	if err := SetTrainerOverride(db, id, false); err != nil {
		t.Fatal(err)
	}
	w, _ = GetWorkout(db, id)
	if w.FTP != 170 || w.FTPKind != "outdoor" {
		t.Errorf("после улицы: FTP=%d kind=%q, хочу 170/outdoor", w.FTP, w.FTPKind)
	}
}
