package importer

import (
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// TestBackfillNormalizedPower — тренировка со старым np_version пересчитывается
// из сэмплов в БД новым алгоритмом, повторный прогон ничего не меняет.
func TestBackfillNormalizedPower(t *testing.T) {
	db := openTestDB(t)

	// np_version=0 — как у строк, импортированных до появления версии
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: 120 * time.Second, AvgPower: 200,
	}, "hash-np-backfill", "/nonexistent/backfill.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertSamples(db, id, series(120, 1, func(int) int { return 200 })); err != nil {
		t.Fatal(err)
	}

	// тренировка вообще без мощности: не должна падать и зацикливаться
	noPwr, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: 10 * time.Second,
	}, "hash-np-nopower", "/nonexistent/nopower.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertSamples(db, noPwr, []store.Sample{{Offset: 0}, {Offset: 1}}); err != nil {
		t.Fatal(err)
	}

	n, err := BackfillNormalizedPower(db)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("пересчитано %d тренировок, хочу 2", n)
	}

	w, err := store.GetWorkout(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if w.NormPower != 200 {
		t.Errorf("NP = %d, хочу 200 (постоянная мощность 200 Вт)", w.NormPower)
	}
	if w.NPVersion != impNPVersion {
		t.Errorf("np_version = %d, хочу %d", w.NPVersion, impNPVersion)
	}

	empty, err := store.GetWorkout(db, noPwr)
	if err != nil {
		t.Fatal(err)
	}
	if empty.NPVersion != impNPVersion || empty.NormPower != 0 {
		t.Errorf("без мощности: NP=%d версия=%d, хочу 0 и %d",
			empty.NormPower, empty.NPVersion, impNPVersion)
	}

	// повторный прогон: пересчитывать нечего, данные не меняются
	n2, err := BackfillNormalizedPower(db)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Errorf("повторно пересчитано %d тренировок, хочу 0", n2)
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
