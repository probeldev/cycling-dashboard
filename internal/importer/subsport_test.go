package importer

import (
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tormoder/fit"

	"cycling-dashboard/internal/store"
)

// writeFIT — минимальный FIT активности: одна сессия с заданным subsport
// и records записей по 200 Вт с шагом 1 с. Всё лишнее остаётся invalid,
// как его инициализирует fit.NewSessionMsg / fit.NewRecordMsg.
func writeFIT(t *testing.T, path string, sub fit.SubSport, records int) {
	t.Helper()
	file, err := fit.NewFile(fit.FileTypeActivity, fit.NewHeader(fit.V20, false))
	if err != nil {
		t.Fatal(err)
	}
	act, err := file.Activity()
	if err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)
	sess := fit.NewSessionMsg()
	sess.Sport = fit.SportCycling
	sess.SubSport = sub
	sess.StartTime = start
	sess.Timestamp = start.Add(time.Duration(records) * time.Second)
	sess.TotalElapsedTime = uint32(records) * 1000 // scale 1000
	sess.TotalTimerTime = uint32(records) * 1000
	sess.Event = fit.EventSession
	sess.EventType = fit.EventTypeStop
	act.Sessions = append(act.Sessions, sess)

	for i := 0; i < records; i++ {
		r := fit.NewRecordMsg()
		r.Timestamp = start.Add(time.Duration(i) * time.Second)
		r.Power = 200
		r.Cadence = 85
		act.Records = append(act.Records, r)
	}

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := fit.Encode(f, file, binary.LittleEndian); err != nil {
		t.Fatal(err)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// deref — значение по указателю, false для nil.
func deref(b *bool) bool { return b != nil && *b }

func TestSubSportName(t *testing.T) {
	for _, tt := range []struct {
		in   fit.SubSport
		want string
	}{
		{fit.SubSportGeneric, "generic"},
		{fit.SubSportIndoorCycling, "indoor_cycling"},
		{fit.SubSportRoad, "road"},
		{fit.SubSportMountain, "mountain"},
		{fit.SubSportEBikeFitness, "e_bike_fitness"},
		{fit.SubSportInvalid, ""},
		{fit.SubSport(60), ""}, // stringer такое значение не знает
	} {
		if got := subSportName(tt.in); got != tt.want {
			t.Errorf("subSportName(%v) = %q, хочу %q", tt.in, got, tt.want)
		}
	}
}

func TestImportWritesSubSport(t *testing.T) {
	dir := t.TempDir()
	writeFIT(t, filepath.Join(dir, "indoor.fit"), fit.SubSportIndoorCycling, 120)
	db := openTestDB(t)

	imported, _, err := ImportDir(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 1 {
		t.Fatalf("импортировано %d, хочу 1", imported)
	}

	w, err := store.LatestWorkout(db)
	if err != nil {
		t.Fatal(err)
	}
	if w.SubSport != "indoor_cycling" {
		t.Errorf("subsport = %q, хочу indoor_cycling", w.SubSport)
	}
	if !w.Trainer {
		t.Error("indoor_cycling должен определяться как станок")
	}
	if w.Duration != 120*time.Second {
		t.Errorf("длительность = %v, хочу 120 с (синтетический FIT должен нормально читаться)", w.Duration)
	}

	// повторный импорт того же файла перезаписывает строку и subsport не теряет
	if _, _, err := ImportDir(db, dir, true); err != nil {
		t.Fatal(err)
	}
	if w2, err := store.LatestWorkout(db); err != nil {
		t.Fatal(err)
	} else if w2.SubSport != "indoor_cycling" {
		t.Errorf("после ре-импорта subsport = %q, хочу indoor_cycling", w2.SubSport)
	}
}

func TestBackfillSubSportIdempotent(t *testing.T) {
	dir := t.TempDir()
	writeFIT(t, filepath.Join(dir, "road.fit"), fit.SubSportRoad, 60)
	db := openTestDB(t)

	if _, _, err := ImportDir(db, dir); err != nil {
		t.Fatal(err)
	}
	w, err := store.LatestWorkout(db)
	if err != nil {
		t.Fatal(err)
	}

	// имитируем базу до появления subsport: колонка пустая, зато пользователь
	// уже задал имя и вручную снял флаг станка
	if _, err := db.Exec(`UPDATE workouts SET subsport = ''`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetName(db, w.ID, "Моя тренировка"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTrainerOverride(db, w.ID, false); err != nil {
		t.Fatal(err)
	}

	n, err := BackfillSubSport(db)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("бэкфилл заполнил %d тренировок, хочу 1", n)
	}
	before, err := store.GetWorkout(db, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.SubSport != "road" {
		t.Errorf("subsport = %q, хочу road", before.SubSport)
	}
	if before.Name != "Моя тренировка" {
		t.Errorf("бэкфилл затёр имя: %q", before.Name)
	}
	if before.Override == nil || *before.Override {
		t.Errorf("бэкфилл затёр trainer_override: %+v", before.Override)
	}

	// второй прогон не находит ничего и данные не меняет
	n2, err := BackfillSubSport(db)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Errorf("повторный бэкфилл заполнил %d тренировок, хочу 0", n2)
	}
	after, err := store.GetWorkout(db, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Override — указатель: сравниваем значение, а не адрес, поэтому обнуляем
	beforeOv, afterOv := before.Override, after.Override
	before.Override, after.Override = nil, nil
	sameOv := (beforeOv == nil) == (afterOv == nil) && deref(beforeOv) == deref(afterOv)
	if *before != *after || !sameOv {
		t.Errorf("повторный бэкфилл изменил тренировку:\n%+v\n%+v", *before, *after)
	}
}

// TestBackfillSubSportMissingFile — файла на диске нет: старт сервера не падаем.
func TestBackfillSubSportMissingFile(t *testing.T) {
	dir := t.TempDir()
	writeFIT(t, filepath.Join(dir, "gone.fit"), fit.SubSportGeneric, 30)
	db := openTestDB(t)

	if _, _, err := ImportDir(db, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workouts SET subsport = ''`); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "gone.fit")); err != nil {
		t.Fatal(err)
	}

	n, err := BackfillSubSport(db)
	if err != nil {
		t.Fatalf("бэкфилл без файла вернул ошибку: %v", err)
	}
	if n != 0 {
		t.Errorf("заполнено %d тренировок, хочу 0", n)
	}
}
