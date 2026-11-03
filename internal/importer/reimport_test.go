package importer

import (
	"path/filepath"
	"testing"

	"github.com/tormoder/fit"

	"cycling-dashboard/internal/store"
)

// TestReimportPreservesUserFields — реимпорт того же файла (upsert) не трогает
// пользовательские поля: bike_id, trainer_override и is_test.
func TestReimportPreservesUserFields(t *testing.T) {
	dir := t.TempDir()
	writeFIT(t, filepath.Join(dir, "user.fit"), fit.SubSportRoad, 30)
	db := openTestDB(t)
	if _, _, err := ImportDir(db, dir); err != nil {
		t.Fatal(err)
	}
	w, err := store.LatestWorkout(db)
	if err != nil {
		t.Fatal(err)
	}
	bikes, err := store.Bikes(db)
	if err != nil || len(bikes) == 0 {
		t.Fatalf("велосипеды: %v, %v", bikes, err)
	}
	bike := bikes[0].ID
	if err := store.SetWorkoutBike(db, w.ID, &bike); err != nil {
		t.Fatal(err)
	}
	// станок из FIT → вручную «улица», чтобы проверить сохранение override
	if err := store.SetTrainerOverride(db, w.ID, false); err != nil {
		t.Fatal(err)
	}
	// и пометка «замер», чтобы проверить сохранение is_test
	if err := store.SetWorkoutTest(db, w.ID, true); err != nil {
		t.Fatal(err)
	}

	if _, _, err := ImportDir(db, dir, true); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetWorkout(db, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BikeID != bike || got.Bike != bikes[0].Name {
		t.Errorf("bike: BikeID=%d Bike=%q, хочу %d/%q", got.BikeID, got.Bike, bike, bikes[0].Name)
	}
	if got.Override == nil || *got.Override || got.Trainer {
		t.Errorf("trainer_override потерян: override=%v trainer=%v", got.Override, got.Trainer)
	}
	if !got.IsTest {
		t.Errorf("is_test потерян после ре-импорта")
	}
}
