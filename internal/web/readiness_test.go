package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// TestReadinessAPI — эндпоинт отдаёт показатели по рабочим тренировкам,
// а /progress рисует карточку готовности теми же числами и coverage-пометкой.
func TestReadinessAPI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	today := store.StartOfDay(time.Now())
	start := today
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: start, Duration: 4 * time.Hour, DistanceKm: 100,
		TSS: 200, Calories: 1600, AscentM: 500,
	}, "hash-readiness-api", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}

	var r store.ReadinessStats
	getJSON(t, h, "/api/readiness", &r)
	if r.Longest90d == nil || r.Longest90d.KM != 100 || r.Longest90d.WorkoutID != id {
		t.Errorf("longest90d: %+v, хочу 100 км (id %d)", r.Longest90d, id)
	} else if !r.Longest90d.Date.Equal(start) {
		t.Errorf("дата longest90d: %v, хочу %v", r.Longest90d.Date, start)
	}
	if r.LongestEver == nil || r.LongestEver.KM != 100 {
		t.Errorf("longestEver: %+v, хочу 100 км", r.LongestEver)
	}
	if r.Rolling7.Rides != 1 || r.Rolling7.KM != 100 ||
		r.Rolling7.Hours != 4 || r.Rolling7.TSS != 200 {
		t.Errorf("rolling7: %+v, хочу 100 км / 4 ч / 200 TSS / 1 поездку", r.Rolling7)
	}
	if r.KcalPerHour == nil || *r.KcalPerHour != 400 {
		t.Errorf("kcalPerHour: %v, хочу 400", r.KcalPerHour)
	}
	if r.AscentPer100km == nil || *r.AscentPer100km != 500 {
		t.Errorf("ascentPer100km: %v, хочу 500", r.AscentPer100km)
	}
	if r.CenturyCount != 1 || r.LongestStreak != 1 {
		t.Errorf("centuryCount=%d longestStreak=%d, хочу 1/1", r.CenturyCount, r.LongestStreak)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/progress", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/progress: код %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Готовность к длинным дистанциям",
		"100.0 км</div>",
		"class=\"k\">макс. выезд за 90 дней · " + start.Format("02.01.2006"),
		`class="coverage">1 день из 90`,
		"100.0 км / 4.0 ч</div>",
		"class=\"k\">7 дней",
		`class="coverage">1 день из 7`,
		"class=\"k\">28 дней",
		`class="coverage">1 день из 28`,
		"400.0 ккал/ч</div>",
		"500.0 м</div>",
		"class=\"k\">набор на 100 км · 90 дней",
		"1 раз</div>",
		"class=\"k\">100+ км за всё время",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("на /progress нет %q", want)
		}
	}
}

// TestReadinessEmpty — без тренировок API отдаёт null (не нули) и пустые
// окна, а карточка на /progress — пояснение вместо голых прочерков.
func TestReadinessEmpty(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/readiness", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/readiness: код %d", rec.Code)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"longest90d", "longestEver", "kcalPerHour", "ascentPer100km"} {
		if got := strings.TrimSpace(string(raw[key])); got != "null" {
			t.Errorf("%s = %s, хочу null", key, got)
		}
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/progress", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/progress: код %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`empty-value">за 90 дней выездов не было</div>`,
		`class="k">макс. выезд за 90 дней`,
		`empty-value">поездок за 7 дней не было</div>`,
		`class="k">7 дней`,
		`empty-value">поездок за 28 дней не было</div>`,
		`class="k">28 дней`,
		`empty-value">нет данных о калориях</div>`,
		`class="k">ккал/ч · 90 дней`,
		`empty-value">нет поездок с дистанцией</div>`,
		`class="k">набор на 100 км · 90 дней`,
		`class="coverage">0 дней из 90`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("пустая карточка: нет %q", want)
		}
	}
}

// TestReadinessAPIIgnoresFutureWorkout — будущая тренировка не попадает ни в
// рекорды, ни в скользящие окна готовности.
func TestReadinessAPIIgnoresFutureWorkout(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	past, err := store.InsertWorkout(db, &store.Workout{
		StartTime: now.Add(-2 * time.Hour), Duration: 2 * time.Hour,
		DistanceKm: 50, TSS: 80, Calories: 600, AscentM: 200,
	}, "hash-readiness-api-past", filepath.Join(t.TempDir(), "p.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertWorkout(db, &store.Workout{
		StartTime: now.Add(2 * time.Hour), Duration: 5 * time.Hour,
		DistanceKm: 200, TSS: 1000, Calories: 4000, AscentM: 2000,
	}, "hash-readiness-api-future", filepath.Join(t.TempDir(), "f.fit"), 1); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var r store.ReadinessStats
	getJSON(t, h, "/api/readiness", &r)
	if r.LongestEver == nil || r.LongestEver.WorkoutID != past || r.LongestEver.KM != 50 {
		t.Errorf("longestEver = %+v, хочу 50 км (id %d)", r.LongestEver, past)
	}
	if r.Longest90d == nil || r.Longest90d.KM != 50 {
		t.Errorf("longest90d = %+v, хочу 50 км", r.Longest90d)
	}
	if r.CenturyCount != 0 {
		t.Errorf("centuryCount = %d, хочу 0", r.CenturyCount)
	}
	if r.Rolling7.Rides != 1 || r.Rolling7.KM != 50 || r.Rolling7.TSS != 80 {
		t.Errorf("rolling7 = %+v, хочу 50 км / 80 TSS / 1 поездку", r.Rolling7)
	}
}
