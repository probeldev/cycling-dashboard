package importer

import (
	"math"
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
