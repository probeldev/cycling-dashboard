package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// ftpHintServer — база с профилем и готовый обработчик для /api/ftp-hint.
func ftpHintServer(t *testing.T, profile map[string]float64) (*sql.DB, http.Handler) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	at := time.Now().Add(-time.Hour)
	for kind, value := range profile {
		if err := store.AddProfileEntry(db, kind, value, at); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
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
