package importer

import (
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// TestBackfillMetrics — TSS считается от duration/NP/FTP даже без сэмплов;
// без NP нагрузочные метрики остаются NULL; повторный прогон ничего не меняет.
func TestBackfillMetrics(t *testing.T) {
	db := openTestDB(t)

	// 1) NP и FTP есть, сэмплов нет — TSS считается от duration_sec
	withNP, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: 3600 * time.Second, NormPower: 200, AvgPower: 200, FTP: 200,
	}, "hash-metrics-np", "/nonexistent/metrics.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 2) без NP — IF/TSS/VI не считаются
	noNP, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: 600 * time.Second, FTP: 200,
	}, "hash-metrics-nonp", "/nonexistent/nonp.fit", 1)
	if err != nil {
		t.Fatal(err)
	}

	n, err := BackfillMetrics(db)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("пересчитано %d, хочу 2", n)
	}

	w, err := store.GetWorkout(db, withNP)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(w.IF-1) > 1e-9 || math.Abs(w.TSS-100) > 0.01 {
		t.Errorf("IF=%v TSS=%v, хочу 1 и 100", w.IF, w.TSS)
	}
	if math.Abs(w.VI-1) > 1e-9 {
		t.Errorf("VI = %v, хочу 1", w.VI)
	}
	if w.MetricsVersion != metricsVersion {
		t.Errorf("metrics_version = %d, хочу %d", w.MetricsVersion, metricsVersion)
	}
	if w.ElapsedSec != 0 || w.WorkKJ != 0 {
		t.Errorf("без FIT elapsed/work должны остаться NULL: %d/%v", w.ElapsedSec, w.WorkKJ)
	}

	empty, err := store.GetWorkout(db, noNP)
	if err != nil {
		t.Fatal(err)
	}
	if empty.IF != 0 || empty.TSS != 0 || empty.VI != 0 {
		t.Errorf("без NP метрики не NULL: IF=%v TSS=%v VI=%v", empty.IF, empty.TSS, empty.VI)
	}

	// повторный прогон: всё уже актуально
	n2, err := BackfillMetrics(db)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Errorf("повторно пересчитано %d, хочу 0", n2)
	}
	again, err := store.GetWorkout(db, withNP)
	if err != nil {
		t.Fatal(err)
	}
	if again.TSS != w.TSS || again.MetricsVersion != w.MetricsVersion {
		t.Errorf("повторный бэкфилл изменил данные: %+v → %+v", *w, *again)
	}
}

// TestMetricsInvalidatedByFTP — смена FTP сбрасывает метрики, бэкфилл их
// пересчитывает; удаление записи профиля возвращает прежний FTP и метрики.
func TestMetricsInvalidatedByFTP(t *testing.T) {
	db := openTestDB(t)
	start := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	if err := store.AddProfileEntry(db, "ftp", 156, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: start, Duration: 3600 * time.Second, NormPower: 140, AvgPower: 123, FTP: 156,
	}, "hash-ftp-inval", "/nonexistent/x.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := BackfillMetrics(db); err != nil || n != 1 {
		t.Fatalf("бэкфилл: %d, %v; хочу 1", n, err)
	}
	before, err := store.GetWorkout(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if want := 140.0 / 156; math.Abs(before.IF-want) > 1e-9 {
		t.Fatalf("IF = %v, хочу %v", before.IF, want)
	}

	// FTP 200 с мая — у июньской тренировки штамп меняется, метрики устаревают
	if err := store.AddProfileEntry(db, "ftp", 200, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	stale, err := store.GetWorkout(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if stale.FTP != 200 {
		t.Fatalf("FTP = %d, хочу 200", stale.FTP)
	}
	if stale.MetricsVersion != 0 {
		t.Fatalf("метрики не инвалидированы: metrics_version=%d", stale.MetricsVersion)
	}
	if n, err := BackfillMetrics(db); err != nil || n != 1 {
		t.Fatalf("бэкфилл после FTP: %d, %v; хочу 1", n, err)
	}
	after, err := store.GetWorkout(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(after.IF-0.7) > 1e-9 {
		t.Errorf("IF = %v, хочу 0.7", after.IF)
	}
	if math.Abs(after.TSS-49) > 0.01 {
		t.Errorf("TSS = %v, хочу 49", after.TSS)
	}

	// удаляем запись профиля — FTP возвращается, метрики пересчитываются обратно
	history, err := store.ProfileHistory(db)
	if err != nil {
		t.Fatal(err)
	}
	var entry int64
	for _, e := range history {
		if e.Kind == "ftp" && e.Value == 200 {
			entry = e.ID
		}
	}
	if entry == 0 {
		t.Fatal("не нашёл запись FTP=200")
	}
	if err := store.DeleteProfileEntry(db, entry); err != nil {
		t.Fatal(err)
	}
	if n, err := BackfillMetrics(db); err != nil || n != 1 {
		t.Fatalf("бэкфилл после удаления: %d, %v; хочу 1", n, err)
	}
	restored, err := store.GetWorkout(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if restored.FTP != 156 {
		t.Errorf("FTP = %d, хочу 156", restored.FTP)
	}
	if math.Abs(restored.IF-before.IF) > 1e-9 || math.Abs(restored.TSS-before.TSS) > 0.01 {
		t.Errorf("метрики не вернулись: IF=%v TSS=%v, было %v/%v",
			restored.IF, restored.TSS, before.IF, before.TSS)
	}
}

// TestMetricsInvalidatedByNP — смена NP в SetNormPowers сбрасывает метрики,
// бэкфилл считает их от нового NP.
func TestMetricsInvalidatedByNP(t *testing.T) {
	db := openTestDB(t)
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: 3600 * time.Second, NormPower: 140, AvgPower: 123, FTP: 156,
	}, "hash-np-inval", "/nonexistent/np.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BackfillMetrics(db); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNormPowers(db, []store.NormPowerUpdate{{ID: id, NP: 100, Version: 99}}); err != nil {
		t.Fatal(err)
	}
	w, err := store.GetWorkout(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if w.MetricsVersion != 0 {
		t.Fatalf("метрики не инвалидированы: version=%d", w.MetricsVersion)
	}
	if n, err := BackfillMetrics(db); err != nil || n != 1 {
		t.Fatalf("бэкфилл: %d, %v; хочу 1", n, err)
	}
	w, err = store.GetWorkout(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(w.IF-100.0/156) > 1e-9 {
		t.Errorf("IF = %v, хочу %v", w.IF, 100.0/156)
	}
}

// TestBackfillMetricsFromFIT — у старой строки нет elapsed/work: бэкфилл
// дочитывает их из FIT-сессии по сохранённому пути.
func TestBackfillMetricsFromFIT(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.fit")
	writeSessionFIT(t, path, 12000, 100000, 100000, 100) // elapsed 100 с, работа 12 кДж
	db := openTestDB(t)
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: 100 * time.Second,
		NormPower: 200, AvgPower: 200, FTP: 200,
	}, "hash-metrics-fit", path, 1)
	if err != nil {
		t.Fatal(err)
	}
	// FIT читаем только для тренировок с мощностью — добавим сэмплы
	if err := store.InsertSamples(db, id, series(100, 1, func(int) int { return 200 })); err != nil {
		t.Fatal(err)
	}
	if n, err := BackfillMetrics(db); err != nil || n != 1 {
		t.Fatalf("бэкфилл: %d, %v; хочу 1", n, err)
	}
	w, err := store.GetWorkout(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if w.ElapsedSec != 100 {
		t.Errorf("ElapsedSec = %d, хочу 100 (из FIT-сессии)", w.ElapsedSec)
	}
	if math.Abs(w.WorkKJ-12) > 1e-9 {
		t.Errorf("WorkKJ = %v, хочу 12 (из TotalWork)", w.WorkKJ)
	}
	if w.PauseSec != 0 {
		t.Errorf("PauseSec = %d, хочу 0", w.PauseSec)
	}
}

// TestMetricsClearedWhenFTPRemoved — удаление последней записи FTP обнуляет
// IF/TSS (VI от FTP не зависит и пересчитывается), повторный прогон старые
// значения не воскрешает.
func TestMetricsClearedWhenFTPRemoved(t *testing.T) {
	db := openTestDB(t)
	if err := store.AddProfileEntry(db, "ftp", 156, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		Duration:  3600 * time.Second, NormPower: 140, AvgPower: 123, FTP: 156,
	}, "hash-ftp-removed", "/nonexistent/x.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BackfillMetrics(db); err != nil {
		t.Fatal(err)
	}

	history, err := store.ProfileHistory(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range history {
		if e.Kind == "ftp" {
			if err := store.DeleteProfileEntry(db, e.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	var ftpNull int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workouts WHERE id = ? AND ftp_w IS NULL`, id).Scan(&ftpNull); err != nil {
		t.Fatal(err)
	}
	if ftpNull != 1 {
		t.Fatalf("ftp_w не NULL после удаления последней записи FTP")
	}

	if _, err := BackfillMetrics(db); err != nil {
		t.Fatal(err)
	}
	w, err := store.GetWorkout(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if w.IF != 0 || w.TSS != 0 {
		t.Errorf("IF/TSS не обнулились: %v/%v", w.IF, w.TSS)
	}
	if math.Abs(w.VI-140.0/123) > 1e-9 {
		t.Errorf("VI = %v, хочу %v (от FTP не зависит)", w.VI, 140.0/123)
	}
	if w.MetricsVersion != metricsVersion {
		t.Errorf("metrics_version = %d, хочу %d", w.MetricsVersion, metricsVersion)
	}

	// повторный прогон не находит работы и не воскрешает старые значения
	if n, err := BackfillMetrics(db); err != nil || n != 0 {
		t.Fatalf("повторный бэкфилл: %d, %v; хочу 0", n, err)
	}
	w, err = store.GetWorkout(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if w.IF != 0 || w.TSS != 0 {
		t.Errorf("старые значения воскресли: IF=%v TSS=%v", w.IF, w.TSS)
	}
}

// TestMetricsClearedWhenNPZero — NP стал нулём: IF/TSS/VI обнуляются.
func TestMetricsClearedWhenNPZero(t *testing.T) {
	db := openTestDB(t)
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: 3600 * time.Second, NormPower: 140, AvgPower: 123, FTP: 156,
	}, "hash-np-zero", "/nonexistent/np0.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BackfillMetrics(db); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNormPowers(db, []store.NormPowerUpdate{{ID: id, NP: 0, Version: 99}}); err != nil {
		t.Fatal(err)
	}
	if _, err := BackfillMetrics(db); err != nil {
		t.Fatal(err)
	}
	w, err := store.GetWorkout(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if w.IF != 0 || w.TSS != 0 || w.VI != 0 {
		t.Errorf("при NP=0 метрики не NULL: IF=%v TSS=%v VI=%v", w.IF, w.TSS, w.VI)
	}
}

// TestBackfillMetricsRollback — ошибка на середине пачки откатывает всю
// транзакцию: ни одна тренировка не получает частично обновлённые метрики.
func TestBackfillMetricsRollback(t *testing.T) {
	db := openTestDB(t)
	a, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: 3600 * time.Second, NormPower: 140, AvgPower: 123, FTP: 156,
	}, "hash-rb-a", "/nonexistent/a.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: 3600 * time.Second, NormPower: 130, AvgPower: 120, FTP: 156,
	}, "hash-rb-b", "/nonexistent/b.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`CREATE TRIGGER fail_metrics BEFORE UPDATE ON workouts
	    WHEN NEW.id = %d BEGIN SELECT RAISE(ABORT, 'boom'); END`, b)); err != nil {
		t.Fatal(err)
	}
	if _, err := BackfillMetrics(db); err == nil {
		t.Fatal("жду ошибку от триггера")
	}
	for _, id := range []int64{a, b} {
		w, err := store.GetWorkout(db, id)
		if err != nil {
			t.Fatal(err)
		}
		if w.MetricsVersion != 0 || w.TSS != 0 || w.IF != 0 {
			t.Errorf("id=%d не откатился: IF=%v TSS=%v version=%d", id, w.IF, w.TSS, w.MetricsVersion)
		}
	}
}
