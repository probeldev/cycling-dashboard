package web

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// TestFitCP — на кривой W(t)=CP+W'/t регрессия восстанавливает параметры.
func TestFitCP(t *testing.T) {
	windows := []int{120, 300, 600, 900, 1200}
	recs := make([]store.PowerRecord, 0, len(windows))
	for _, w := range windows {
		recs = append(recs, store.PowerRecord{WindowSec: w, Watts: 200 + 20000/float64(w)})
	}
	res := fitCP(recs)
	if res.CP == nil || res.WPrime == nil || res.R2 == nil {
		t.Fatalf("фит не посчитался: %+v", res)
	}
	if math.Abs(*res.CP-200) > 2 || math.Abs(*res.WPrime-20000) > 200 {
		t.Errorf("CP=%v W'=%v, хочу 200 и 20000 (1%%)", *res.CP, *res.WPrime)
	}
	if *res.R2 < 0.999 {
		t.Errorf("R² = %v, хочу ≈1", *res.R2)
	}
	if res.Points != 5 {
		t.Errorf("точек %d, хочу 5", res.Points)
	}
	if res.FTPEst == nil || math.Abs(*res.FTPEst-0.95*(200+20000.0/1200)) > 0.1 {
		t.Errorf("FTP-оценка = %v, хочу 0.95·W(1200)", res.FTPEst)
	}
	if res.LowConfidence {
		t.Errorf("чистая кривая помечена низкой достоверностью")
	}
}

// TestFitCPNotEnough — меньше трёх точек: CP/W' не считаются.
func TestFitCPNotEnough(t *testing.T) {
	res := fitCP([]store.PowerRecord{{WindowSec: 60, Watts: 300}, {WindowSec: 120, Watts: 250}})
	if res.CP != nil || res.WPrime != nil || res.R2 != nil {
		t.Errorf("при 1 точке фит посчитался: %+v", res)
	}
	if res.Points != 1 {
		t.Errorf("точек %d, хочу 1", res.Points)
	}
	if res.FTPEst != nil {
		t.Errorf("FTP-оценка без 20 мин: %v", res.FTPEst)
	}
}

// TestFitCPNoisy — шумные данные помечаются низкой достоверностью.
func TestFitCPNoisy(t *testing.T) {
	res := fitCP([]store.PowerRecord{
		{WindowSec: 120, Watts: 300},
		{WindowSec: 300, Watts: 150},
		{WindowSec: 600, Watts: 280},
		{WindowSec: 900, Watts: 160},
		{WindowSec: 1200, Watts: 290},
	})
	if res.CP == nil || res.R2 == nil {
		t.Fatalf("фит не посчитался: %+v", res)
	}
	if *res.R2 >= 0.9 || !res.LowConfidence {
		t.Errorf("R² = %v, low=%v; хочу низкую достоверность", *res.R2, res.LowConfidence)
	}
}

// TestCPAPI — /api/cp отдаёт фит по записям периода.
func TestCPAPI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: time.Hour,
	}, "hash-cp-api", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	var recs []store.PowerRecord
	for _, w := range []int{120, 300, 600, 900, 1200} {
		recs = append(recs, store.PowerRecord{WindowSec: w, Watts: 200 + 20000/float64(w)})
	}
	if err := store.SetWorkoutMMP(db, id, recs, 1); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/cp?period=all", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	var got cpResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.CP == nil || math.Abs(*got.CP-200) > 2 || got.Points != 5 {
		t.Errorf("cp = %+v", got)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/cp?period=week", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("period=week: код %d, хочу 400", rec.Code)
	}
}
