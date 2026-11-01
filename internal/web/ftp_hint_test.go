package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/importer"
	"cycling-dashboard/internal/store"
)

// ftpTestServer — пустая база и готовый обработчик: профиль тест добавляет сам.
func ftpTestServer(t *testing.T) (*sql.DB, http.Handler) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return db, h
}

// ftpHintServer — база с профилем и готовый обработчик для /api/ftp-hint.
func ftpHintServer(t *testing.T, profile map[string]float64) (*sql.DB, http.Handler) {
	t.Helper()
	db, h := ftpTestServer(t)
	at := time.Now().Add(-time.Hour)
	for kind, value := range profile {
		if err := store.AddProfileEntry(db, kind, value, at); err != nil {
			t.Fatal(err)
		}
	}
	return db, h
}

// addHintWorkout — тренировка с одним окном MMP 1200 с (или без него).
func addHintWorkout(t *testing.T, db *sql.DB, trainer bool, start time.Time, watts float64) int64 {
	t.Helper()
	w := &store.Workout{StartTime: start, Duration: time.Hour, Trainer: trainer}
	if watts <= 0 {
		return 0
	}
	id, err := store.InsertWorkout(db, w,
		fmt.Sprintf("hash-hint-%v-%d", trainer, start.UnixNano()), filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	recs := []store.PowerRecord{{WindowSec: 1200, Watts: watts}}
	if err := store.SetWorkoutMMP(db, id, recs, 1); err != nil {
		t.Fatal(err)
	}
	return id
}

func getFTPHint(t *testing.T, h http.Handler) *store.FTPHint {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ftp-hint", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d, хочу 200", rec.Code)
	}
	var got *store.FTPHint
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("разбор ответа %q: %v", rec.Body.String(), err)
	}
	return got
}

// TestFTPHintThreshold — порог строгий: ровно +2% не подсказка, +2.1% — подсказка.
func TestFTPHintThreshold(t *testing.T) {
	start := time.Date(2026, 10, 31, 10, 0, 0, 0, time.Local)

	t.Run("ниже порога", func(t *testing.T) {
		db, h := ftpHintServer(t, map[string]float64{"ftp": 200})
		addHintWorkout(t, db, false, start, 210)
		if got := getFTPHint(t, h); got != nil {
			t.Errorf("подсказка при оценке ниже FTP: %+v", got)
		}
	})

	t.Run("ровно +2%", func(t *testing.T) {
		db, h := ftpHintServer(t, map[string]float64{"ftp": 200})
		// 0.95 × 214.75 = 204.0125 → оценка 204.0, ровно +2.0%
		addHintWorkout(t, db, false, start, 214.75)
		if got := getFTPHint(t, h); got != nil {
			t.Errorf("подсказка ровно на пороге: %+v", got)
		}
	})

	t.Run("+2.1%", func(t *testing.T) {
		db, h := ftpHintServer(t, map[string]float64{"ftp": 200})
		id := addHintWorkout(t, db, false, start, 214.95)
		got := getFTPHint(t, h)
		if got == nil {
			t.Fatal("нет подсказки при +2.1%")
		}
		if got.Kind != "outdoor" || got.Current != 200 || got.Estimate != 204.2 || got.DeltaPct != 2.1 {
			t.Errorf("подсказка = %+v", got)
		}
		if got.WorkoutID != id || got.Date.Format("2006-01-02") != "2026-10-31" {
			t.Errorf("тренировка в подсказке = id %d, дата %v", got.WorkoutID, got.Date)
		}
	})
}

// TestFTPHintKindFTP — станок сравнивается со станковым FTP, улица без
// ftp_outdoor — fallback на станковый.
func TestFTPHintKindFTP(t *testing.T) {
	start := time.Date(2026, 10, 31, 10, 0, 0, 0, time.Local)

	t.Run("станок и станковый FTP", func(t *testing.T) {
		db, h := ftpHintServer(t, map[string]float64{"ftp": 200, "ftp_outdoor": 100})
		id := addHintWorkout(t, db, true, start, 214.95)
		got := getFTPHint(t, h)
		if got == nil {
			t.Fatal("нет подсказки для станка")
		}
		if got.Kind != "trainer" || got.Current != 200 || got.WorkoutID != id {
			t.Errorf("подсказка станка = %+v", got)
		}
	})

	t.Run("улица без ftp_outdoor", func(t *testing.T) {
		db, h := ftpHintServer(t, map[string]float64{"ftp": 200})
		id := addHintWorkout(t, db, false, start, 214.95)
		got := getFTPHint(t, h)
		if got == nil {
			t.Fatal("нет подсказки с fallback на ftp")
		}
		if got.Kind != "outdoor" || got.Current != 200 || got.WorkoutID != id {
			t.Errorf("fallback улицы = %+v", got)
		}
	})
}

// TestFTPHintNoWindow — без окна 1200 с подсказки нет.
func TestFTPHintNoWindow(t *testing.T) {
	db, h := ftpHintServer(t, map[string]float64{"ftp": 200})
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Date(2026, 10, 31, 10, 0, 0, 0, time.Local), Duration: time.Hour,
	}, "hash-hint-no-1200", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkoutMMP(db, id, []store.PowerRecord{{WindowSec: 300, Watts: 400}}, 1); err != nil {
		t.Fatal(err)
	}
	if got := getFTPHint(t, h); got != nil {
		t.Errorf("подсказка без окна 1200: %+v", got)
	}
}

// TestFTPHintPicksLargest — при превышении по обоим видам API отдаёт большее;
// станковый и уличный текущие FTP не путаются.
func TestFTPHintPicksLargest(t *testing.T) {
	db, h := ftpHintServer(t, map[string]float64{"ftp": 200, "ftp_outdoor": 100})
	start := time.Date(2026, 10, 31, 10, 0, 0, 0, time.Local)
	trainerID := addHintWorkout(t, db, true, start, 220)  // 209 Вт, +4.5%
	outdoorID := addHintWorkout(t, db, false, start, 214) // 203.3 Вт, +103.3%
	got := getFTPHint(t, h)
	if got == nil {
		t.Fatal("нет подсказки")
	}
	if got.Kind != "outdoor" || got.WorkoutID != outdoorID || got.Current != 100 {
		t.Errorf("выбрана не большая подсказка: %+v (станок id %d)", got, trainerID)
	}
}

func postProfileEntry(t *testing.T, h http.Handler, form url.Values) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/profile/entry", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// TestFTPHintUpdateHidesHint — кнопка пишет запись на момент нажатия, а не на
// дату тренировки: после POST подсказка исчезает и для станка, и для улицы.
func TestFTPHintUpdateHidesHint(t *testing.T) {
	start := time.Now().Add(-2 * time.Hour)
	cases := []struct {
		name        string
		kind        string
		profileKind string
		postKind    string
		trainer     bool
		current     float64
		watts       float64
	}{
		{"станок", "trainer", "ftp", "ftp", true, 200, 220},
		{"улица", "outdoor", "ftp_outdoor", "ftp_outdoor", false, 180, 210},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			db, h := ftpHintServer(t, map[string]float64{tt.profileKind: tt.current})
			addHintWorkout(t, db, tt.trainer, start, tt.watts)
			hint := getFTPHint(t, h)
			if hint == nil || hint.Kind != tt.kind {
				t.Fatalf("подсказка до POST = %+v, хочу вид %s", hint, tt.kind)
			}
			code := postProfileEntry(t, h, url.Values{
				"kind":  {tt.postKind},
				"value": {strconv.FormatFloat(hint.Estimate, 'f', 1, 64)},
			})
			if code != http.StatusOK {
				t.Fatalf("POST: код %d, хочу 200", code)
			}
			if got := getFTPHint(t, h); got != nil {
				t.Errorf("подсказка %s не исчезла после POST: %+v", tt.kind, got)
			}
		})
	}
}

// TestFTPHintUpdateKeepsWorkoutsBeforeEntry — сценарий 308: запись на now не
// перештамповывает тренировки, начавшиеся до её применения (52 сохраняет
// 156/IF/TSS), а подсказка +9.6% после нажатия кнопки исчезает.
func TestFTPHintUpdateKeepsWorkoutsBeforeEntry(t *testing.T) {
	db, h := ftpTestServer(t)
	oldID, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Date(2026, 10, 28, 16, 59, 56, 0, time.UTC),
		Duration:  2064 * time.Second, Trainer: true,
		NormPower: 130, AvgPower: 106, HasPower: true,
	}, "hash-hint-52", filepath.Join(t.TempDir(), "52.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	sourceID, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Date(2026, 10, 29, 17, 48, 57, 0, time.UTC),
		Duration:  3021 * time.Second, Trainer: true,
		NormPower: 97, AvgPower: 96, HasPower: true,
	}, "hash-hint-308", filepath.Join(t.TempDir(), "308.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkoutMMP(db, sourceID, []store.PowerRecord{{WindowSec: 1200, Watts: 180}}, 1); err != nil {
		t.Fatal(err)
	}
	profileAt := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	if err := store.AddProfileEntry(db, "ftp", 156, profileAt); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.BackfillMetrics(db); err != nil {
		t.Fatal(err)
	}

	type snapshot struct {
		ftp  int
		kind string
		iff  float64
		tss  float64
		ver  int
	}
	snap := func(id int64) snapshot {
		t.Helper()
		w, err := store.GetWorkout(db, id)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot{w.FTP, w.FTPKind, w.IF, w.TSS, w.MetricsVersion}
	}
	beforeOld, beforeSource := snap(oldID), snap(sourceID)

	hint := getFTPHint(t, h)
	if hint == nil {
		t.Fatal("нет подсказки по 308")
	}
	if hint.Kind != "trainer" || hint.Current != 156 || hint.Estimate != 171 ||
		hint.DeltaPct != 9.6 || hint.WorkoutID != sourceID {
		t.Fatalf("подсказка = %+v, хочу trainer/156/171/+9.6/id %d", hint, sourceID)
	}
	if code := postProfileEntry(t, h, url.Values{
		"kind": {"ftp"}, "value": {"171"},
	}); code != http.StatusOK {
		t.Fatalf("POST: код %d, хочу 200", code)
	}
	if got := getFTPHint(t, h); got != nil {
		t.Errorf("подсказка не исчезла после POST: %+v", got)
	}
	if got := snap(oldID); got != beforeOld {
		t.Errorf("52 до записи изменилась: было %+v, стало %+v", beforeOld, got)
	}
	if got := snap(sourceID); got != beforeSource {
		t.Errorf("308 до записи изменилась: было %+v, стало %+v", beforeSource, got)
	}

	history, err := store.ProfileHistory(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("записей профиля %d, хочу 2", len(history))
	}
	if history[0].Kind != "ftp" || history[0].Value != 171 {
		t.Errorf("новая запись = %+v, хочу ftp 171", history[0])
	}
	if d := time.Since(history[0].RecordedAt); d < -time.Minute || d > time.Minute {
		t.Errorf("новая запись датирована %v, хочу момент нажатия", history[0].RecordedAt)
	}
	if !history[1].RecordedAt.Equal(profileAt) || history[1].Value != 156 {
		t.Errorf("старая запись = %+v, хочу ftp 156 на %v", history[1], profileAt)
	}
}
