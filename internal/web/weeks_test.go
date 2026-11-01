package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

func TestParseWeeksN(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		want    int
		wantErr bool
	}{
		{"дефолт", "", defaultWeeks, false},
		{"явный", "n=26", 26, false},
		{"максимум", fmt.Sprintf("n=%d", maxWeeks), maxWeeks, false},
		{"мусор", "n=abc", 0, true},
		{"ноль", "n=0", 0, true},
		{"отрицательное", "n=-1", 0, true},
		{"выше максимума", fmt.Sprintf("n=%d", maxWeeks+1), 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatal(err)
			}
			got, err := parseWeeksN(q)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("%q: жду ошибку, получил %d", tt.query, got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("%q: %d, %v; хочу %d", tt.query, got, err, tt.want)
			}
		})
	}
}

// TestWeeksAPI — /api/weeks отдаёт последние недели с TSS и зонами.
func TestWeeksAPI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// середина текущей недели: не зависит от дня запуска теста
	start := store.WeekStart(time.Now()).Add(12 * time.Hour)
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: start, Duration: time.Hour, TSS: 100,
	}, "hash-weeks-api", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkoutZones(db, id, [7]int{100, 200, 300, 400, 0, 0, 0}, 1); err != nil {
		t.Fatal(err)
	}
	rpe := 6 // 6 × 60 мин = 360
	if err := store.SetWorkoutContext(db, id, store.WorkoutContextUpdate{RPE: &rpe}); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func(query string) (*httptest.ResponseRecorder, []store.Week) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/weeks"+query, nil))
		var weeks []store.Week
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &weeks); err != nil {
				t.Fatal(err)
			}
		}
		return rec, weeks
	}

	rec, weeks := get("?n=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	if len(weeks) != 2 {
		t.Fatalf("%d недель, хочу 2", len(weeks))
	}
	cur := weeks[1]
	if cur.TSS != 100 || cur.Rides != 1 {
		t.Errorf("текущая неделя: %+v", cur)
	}
	if cur.Zones != [7]int{100, 200, 300, 400, 0, 0, 0} {
		t.Errorf("зоны: %v", cur.Zones)
	}
	// total 1000: low 300, gray 300, high 400
	if cur.LowPct != 30 || cur.GrayPct != 30 || cur.HighPct != 40 {
		t.Errorf("проценты: %v/%v/%v", cur.LowPct, cur.GrayPct, cur.HighPct)
	}

	// srpeSum проверяем по ключу JSON, а не через симметричный store.Week:
	// иначе пропавшее из ответа поле осталось бы незамеченным.
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	var srpe float64
	if err := json.Unmarshal(raw[1]["srpeSum"], &srpe); err != nil {
		t.Fatalf("в JSON нет srpeSum: %v", err)
	}
	if srpe != 360 {
		t.Errorf("srpeSum = %v, хочу 360", srpe)
	}

	if rec, _ := get("?n=abc"); rec.Code != http.StatusBadRequest {
		t.Errorf("n=abc: код %d, хочу 400", rec.Code)
	}
}

// TestWeeksAPIIgnoresFutureWorkout — будущая тренировка не раздувает TSS и
// число поездок ни в одной из отданных недель.
func TestWeeksAPIIgnoresFutureWorkout(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	for _, w := range []struct {
		hash  string
		start time.Time
		tss   float64
	}{
		{"hash-weeks-api-past", now.Add(-2 * time.Hour), 30},
		{"hash-weeks-api-future", now.Add(2 * time.Hour), 1000},
	} {
		if _, err := store.InsertWorkout(db, &store.Workout{
			StartTime: w.start, Duration: time.Hour, TSS: w.tss,
		}, w.hash, filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/weeks?n=3", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	var weeks []store.Week
	if err := json.Unmarshal(rec.Body.Bytes(), &weeks); err != nil {
		t.Fatal(err)
	}
	var tss float64
	rides := 0
	for _, w := range weeks {
		tss += w.TSS
		rides += w.Rides
	}
	if tss != 30 || rides != 1 {
		t.Errorf("недели = %.0f TSS / %d поездка, хочу 30 / 1", tss, rides)
	}
}
