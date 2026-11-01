package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// TestBuildPowerZones — границы от FTP 156 берутся из ridedata.ZoneBounds и
// печатаются без округления до целых; проценты — по тем же границам.
func TestBuildPowerZones(t *testing.T) {
	rows := buildPowerZones(156, [7]int{60, 120, 180, 0, 0, 0, 0})
	wantRanges := []string{"0–87.4", "87.4–118.6", "118.6–142", "142–165.4", "165.4–188.8", "188.8–235.6", "235.6+"}
	wantPct := []string{"0–55%", "56–75%", "76–90%", "91–105%", "106–120%", "121–150%", "151%+"}
	wantTime := []string{"1 мин", "2 мин", "3 мин", "—", "—", "—", "—"}
	if len(rows) != 7 {
		t.Fatalf("зон %d, хочу 7", len(rows))
	}
	for i, row := range rows {
		if row.Range != wantRanges[i] || row.Pct != wantPct[i] || row.Time != wantTime[i] {
			t.Errorf("Z%d = %q / %q / %q, хочу %q / %q / %q",
				i+1, row.Range, row.Pct, row.Time, wantRanges[i], wantPct[i], wantTime[i])
		}
	}
}

// TestZoneTimeLabel — короткие зоны печатаются в секундах, дальше в минутах и
// часах; ноль — прочерк.
func TestZoneTimeLabel(t *testing.T) {
	for _, tt := range []struct {
		secs int
		want string
	}{
		{0, "—"},
		{45, "45 с"},
		{60, "1 мин"},
		{3599, "59 мин"},
		{3600, "1 ч 0 мин"},
	} {
		if got := zoneTimeLabel(tt.secs); got != tt.want {
			t.Errorf("zoneTimeLabel(%d) = %q, хочу %q", tt.secs, got, tt.want)
		}
	}
}

// TestProfileZonesPage — на /profile две секции зон (станок и улица) и время
// из workout_zones соответствующего типа тренировки.
func TestProfileZonesPage(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Now().Add(-time.Hour)
	if err := store.AddProfileEntry(db, "ftp", 156, at); err != nil {
		t.Fatal(err)
	}
	if err := store.AddProfileEntry(db, "ftp_outdoor", 180, at); err != nil {
		t.Fatal(err)
	}
	for _, w := range []struct {
		hash    string
		trainer bool
		ftpKind string
		zones   [7]int
	}{
		{"hash-zones-trainer", true, "trainer", [7]int{60, 120, 180, 0, 0, 0, 0}},
		{"hash-zones-outdoor", false, "outdoor", [7]int{0, 3600, 0, 0, 0, 0, 0}},
	} {
		id, err := store.InsertWorkout(db, &store.Workout{
			StartTime: time.Now().Add(-time.Hour), Duration: time.Hour, Trainer: w.trainer,
			FTPKind: w.ftpKind,
		}, w.hash, filepath.Join(t.TempDir(), "x.fit"), 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetWorkoutZones(db, id, w.zones, 1); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profile", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Мощностные зоны",
		"Станок · FTP 156 Вт", "Улица · FTP 180 Вт",
		"0–87.4", "87.4–118.6", "0–100.8", "100.8–136.8",
		"1 ч 0 мин", "3 мин",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("на /profile нет %q", want)
		}
	}
}

// TestProfileFractionalFTP — дробный FTP не усекается до целого: заголовок,
// границы зон и Вт/кг считаются от 156.5, а не от 156.
func TestProfileFractionalFTP(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Now().Add(-time.Hour)
	if err := store.AddProfileEntry(db, "ftp", 156.5, at); err != nil {
		t.Fatal(err)
	}
	if err := store.AddProfileEntry(db, "weight", 75, at); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profile", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"156.5 Вт", "Станок · FTP 156.5 Вт", "0–87.6", "2.1"} {
		if !strings.Contains(body, want) {
			t.Errorf("на /profile нет %q", want)
		}
	}
	if strings.Contains(body, "87.4") {
		t.Errorf("границы зон посчитаны от усечённого FTP 156")
	}
}

// TestProfileZonesEmptyHistory — без тренировок за 90 дней зоны всё равно
// показаны, а время — прочерк, а не «0 мин».
func TestProfileZonesEmptyHistory(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.AddProfileEntry(db, "ftp", 156, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profile", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "0–87.4") || !strings.Contains(body, "87.4–118.6") {
		t.Errorf("нет зон от FTP 156")
	}
	if !strings.Contains(body, "<td>—</td>") {
		t.Errorf("пустая история времени не даёт прочерк")
	}
}
