package importer

import (
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// TestBackfillNormalizedPower — тренировка со старым np_version пересчитывается
// из сэмплов в БД; тренировки без сэмплов или без валидной мощности не трогаем,
// чтобы не затереть device-NP нулём.
func TestBackfillNormalizedPower(t *testing.T) {
	db := openTestDB(t)

	// 1) мощность есть — пересчитываем
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: 120 * time.Second, AvgPower: 200,
	}, "hash-np-backfill", "/nonexistent/backfill.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertSamples(db, id, series(120, 1, func(int) int { return 200 })); err != nil {
		t.Fatal(err)
	}

	// 2) сэмплов нет вовсе: device-NP (200) должен уцелеть
	noSamples, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: 10 * time.Second, NormPower: 200,
	}, "hash-np-nosamples", "/nonexistent/nosamples.fit", 1)
	if err != nil {
		t.Fatal(err)
	}

	// 3) сэмплы есть, но мощность везде NULL (датчик не писал)
	noPower, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: 10 * time.Second, NormPower: 200,
	}, "hash-np-nopower", "/nonexistent/nopower.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertSamples(db, noPower, []store.Sample{{Offset: 0}, {Offset: 1}}); err != nil {
		t.Fatal(err)
	}

	done, skipped, err := BackfillNormalizedPower(db)
	if err != nil {
		t.Fatal(err)
	}
	if done != 1 || skipped != 2 {
		t.Errorf("пересчитано %d, пропущено %d; хочу 1 и 2", done, skipped)
	}

	w, err := store.GetWorkout(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if w.NormPower != 200 || w.NPVersion != impNPVersion {
		t.Errorf("с мощностью: NP=%d версия=%d, хочу 200 и %d", w.NormPower, w.NPVersion, impNPVersion)
	}

	for _, tt := range []struct {
		name string
		id   int64
	}{
		{"без сэмплов", noSamples},
		{"без валидной мощности", noPower},
	} {
		got, err := store.GetWorkout(db, tt.id)
		if err != nil {
			t.Fatal(err)
		}
		if got.NormPower != 200 {
			t.Errorf("%s: NP затёрт до %d, хочу 200", tt.name, got.NormPower)
		}
		if got.NPVersion != 0 {
			t.Errorf("%s: np_version=%d, хочу 0 (пусть посчитается позже)", tt.name, got.NPVersion)
		}
	}

	// повторный прогон: считать нечего, данные не меняются
	done2, skipped2, err := BackfillNormalizedPower(db)
	if err != nil {
		t.Fatal(err)
	}
	if done2 != 0 || skipped2 != 2 {
		t.Errorf("повторно: пересчитано %d, пропущено %d; хочу 0 и 2", done2, skipped2)
	}
	again, err := store.GetWorkout(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if again.NormPower != w.NormPower || again.NPVersion != w.NPVersion {
		t.Errorf("повторный бэкфилл изменил данные: %d/%d → %d/%d",
			w.NormPower, w.NPVersion, again.NormPower, again.NPVersion)
	}
}
