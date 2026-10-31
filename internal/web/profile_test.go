package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// TestProfileKinds — профиль показывает оба вида FTP, kind валидируется,
// запись ftp_outdoor сохраняется.
func TestProfileKinds(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.AddProfileEntry(db, "weight", 75, at); err != nil {
		t.Fatal(err)
	}
	if err := store.AddProfileEntry(db, "ftp", 156, at); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	post := func(kind, value string) int {
		t.Helper()
		body := "kind=" + kind + "&value=" + value + "&date=2026-10-30"
		req := httptest.NewRequest(http.MethodPost, "/profile/entry", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := post("garbage", "200"); code != http.StatusBadRequest {
		t.Errorf("kind=garbage: код %d, хочу 400", code)
	}
	if code := post("ftp_outdoor", "170"); code != http.StatusOK {
		t.Errorf("kind=ftp_outdoor: код %d, хочу 200", code)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profile", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /profile: код %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"FTP (станок)", "FTP (улица)", "156", "170"} {
		if !strings.Contains(body, want) {
			t.Errorf("на /profile нет %q", want)
		}
	}
}

// TestWorkoutPageFTPKind — рядом с FTP подписан источник значения.
func TestWorkoutPageFTPKind(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: time.Hour, FTP: 170, FTPKind: "outdoor",
		HasPower: true, AvgPower: 200, MaxPower: 400,
	}, "hash-page-kind", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workout/"+strconv.FormatInt(id, 10), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "FTP на дату (улица)") {
		t.Errorf("нет подписи источника FTP")
	}
}
