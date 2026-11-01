package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/advice"
	"cycling-dashboard/internal/store"
)

// statusFixture — 7 полных недель по 220 TSS и текущая неделя 220. Предыдущая
// неделя пустая: это даёт deltaPct=null, а медиана (0,220,220,220) всё равно
// 220. Последняя тренировка без пульса/RPE и с низким каденсом — у неё будут
// карточки советов.
func statusFixture(t *testing.T) (*sql.DB, time.Time) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local) // пятница
	ws := store.WeekStart(now)
	if err := store.AddProfileEntry(db, "ftp", 200, ws.AddDate(0, 0, -70)); err != nil {
		t.Fatal(err)
	}

	zones := map[int][7]int{
		0: {1000, 2600, 0, 0, 0, 0, 0}, // пн
		1: {3600, 0, 0, 0, 0, 0, 0},    // вт
		2: {3600, 0, 0, 0, 0, 0, 0},    // ср
		3: {1000, 1000, 1000, 600, 0, 0, 0},
		4: {1000, 1000, 600, 1000, 0, 0, 0},
	}
	load := []float64{40, 30, 30, 60, 60}
	var lastID int64
	for w := -7; w <= 0; w++ {
		if w == -1 {
			continue // предыдущая неделя пустая
		}
		monday := ws.AddDate(0, 0, 7*w)
		for day, tss := range load {
			trainer := w < 0 || day == 3 // прошлые недели — станок, в текущей станок только чт
			km := 0.0
			if !trainer {
				km = 10
				if day == 4 {
					km = 20
				}
			}
			cadence := 85
			if w == 0 && day == 4 {
				cadence = 60 // последняя тренировка: совет про низкий каденс
			}
			start := monday.AddDate(0, 0, day).Add(12 * time.Hour)
			id, err := store.InsertWorkout(db, &store.Workout{
				StartTime: start, Duration: time.Hour, TSS: tss,
				Trainer: trainer, DistanceKm: km, AvgPower: 100, AvgCadence: cadence,
			}, fmt.Sprintf("hash-status-%d-%d", w, day), filepath.Join(t.TempDir(), "x.fit"), 1)
			if err != nil {
				t.Fatal(err)
			}
			if w == 0 {
				if err := store.SetWorkoutZones(db, id, zones[day], 1); err != nil {
					t.Fatal(err)
				}
			}
			if w == 0 && day == 4 {
				lastID = id
			}
		}
	}
	if lastID == 0 {
		t.Fatal("последняя тренировка не создана")
	}
	return db, now
}

func almostEqual(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("%s = %v, хочу %v", name, got, want)
	}
}

func TestStatusGolden(t *testing.T) {
	db, now := statusFixture(t)
	got, err := buildStatus(db, now, false)
	if err != nil {
		t.Fatal(err)
	}

	if got.AsOf != "2026-10-30" {
		t.Errorf("asOf = %q", got.AsOf)
	}
	if got.State.Label != "norm" {
		t.Errorf("state.label = %q, хочу norm", got.State.Label)
	}
	if got.State.Confidence != "ok" {
		t.Errorf("confidence = %q", got.State.Confidence)
	}
	if got.State.Verdict != "Форма в норме" {
		t.Errorf("verdict = %q", got.State.Verdict)
	}
	almostEqual(t, "tsb", got.State.TSB.Value, -5.3)
	almostEqual(t, "ctl", got.State.CTL.Value, 18.8)
	almostEqual(t, "atl", got.State.ATL.Value, 24.1)
	almostEqual(t, "ctl.trend", got.State.CTL.Trend, 2.9)
	almostEqual(t, "atl.trend", got.State.ATL.Trend, 11.3)
	almostEqual(t, "tsb.trend", got.State.TSB.Trend, 8.9)
	if got.State.CTL.Verdict != "Форма растёт" || got.State.ATL.Verdict != "Усталость умеренная" ||
		got.State.TSB.Verdict != "Свежесть в норме" {
		t.Errorf("метрики: ctl=%q atl=%q tsb=%q", got.State.CTL.Verdict, got.State.ATL.Verdict, got.State.TSB.Verdict)
	}

	w := got.Week
	if w.Start != "2026-10-26" || w.DayOfWeek != 5 {
		t.Errorf("week start/dow = %q/%d", w.Start, w.DayOfWeek)
	}
	almostEqual(t, "week.tss", w.TSS, 220)
	if w.Target == nil {
		t.Fatal("week.target = null, хочу 220")
	}
	almostEqual(t, "week.target", *w.Target, 220)
	if w.TargetSource != store.TargetSourceMedian4 {
		t.Errorf("targetSource = %q, хочу median4", w.TargetSource)
	}
	if w.TargetToDate == nil {
		t.Fatal("targetToDate = null")
	}
	almostEqual(t, "targetToDate", *w.TargetToDate, 157.1)
	if w.Remaining == nil {
		t.Fatal("remaining = null")
	}
	almostEqual(t, "remaining", *w.Remaining, 0)
	if w.RemainingMinZ2Total == nil || *w.RemainingMinZ2Total != 0 {
		t.Errorf("remainingMinZ2Total = %v", w.RemainingMinZ2Total)
	}
	almostEqual(t, "hours", w.Hours, 5)
	almostEqual(t, "km", w.KM, 50)
	if w.Rides != 5 {
		t.Errorf("rides = %d", w.Rides)
	}
	almostEqual(t, "trainerHours", w.TrainerHours, 1)
	almostEqual(t, "outdoorHours", w.OutdoorHours, 4)
	almostEqual(t, "prevWeekTSS", w.PrevWeekTSS, 0)
	if w.DeltaPct != nil {
		t.Errorf("deltaPct = %v, хочу null при пустой прошлой неделе", *w.DeltaPct)
	}
	almostEqual(t, "lowPct", w.Zones.LowPct, 82.2)
	almostEqual(t, "grayPct", w.Zones.GrayPct, 8.9)
	almostEqual(t, "highPct", w.Zones.HighPct, 8.9)

	wantSummary := "Форма в норме. 5 тренировок за неделю, 220 из 220 TSS. Вчера — 60 TSS. Тренируйся по плану, следи за восстановлением."
	if got.Summary != wantSummary {
		t.Errorf("summary = %q\nхочу      %q", got.Summary, wantSummary)
	}

	rec := got.Recommendation
	if rec.RuleID != 7 || rec.Kind != "z2" || rec.Zone != "Z2" {
		t.Errorf("recommendation = %+v", rec)
	}
	if rec.DurationMin != [2]int{60, 75} ||
		rec.Watts == nil || *rec.Watts != [2]int{120, 150} ||
		rec.PctFTP != [2]int{60, 75} || rec.Cadence != [2]int{85, 90} {
		t.Errorf("recommendation ranges = %+v", rec)
	}

	last, ok := got.LastWorkout.(map[string]any)
	if !ok || last == nil {
		t.Fatalf("lastWorkout = %#v", got.LastWorkout)
	}
	if id, _ := last["ID"].(float64); int64(id) == 0 {
		t.Errorf("lastWorkout.ID = %v", last["ID"])
	}
	cards, ok := last["advice"].([]advice.Card)
	if !ok || len(cards) != 2 {
		t.Fatalf("advice = %#v, хочу 2 карточки", last["advice"])
	}
	if cards[0].Title != "Низкий каденс" {
		t.Errorf("первая карточка = %+v", cards[0])
	}

	if len(got.Alerts) != 2 || got.Alerts[0].Code != "no_hr" || got.Alerts[1].Code != "no_rpe" {
		t.Errorf("alerts = %+v", got.Alerts)
	}
	if len(got.AllAlerts) != 2 || got.AlertsAll != nil {
		t.Errorf("alerts/all = %+v / %+v", got.Alerts, got.AlertsAll)
	}
}

func TestStatusGrayZoneFourWeeks(t *testing.T) {
	db, now := statusFixture(t)
	// серая зона в прошлой полной неделе: в текущей её мало, но за 4 недели
	// уже больше 20% — рекомендация должна смениться на правило 6
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: store.WeekStart(now).AddDate(0, 0, -7).Add(12 * time.Hour),
		Duration:  time.Hour,
	}, "hash-status-gray-prev", filepath.Join(t.TempDir(), "g.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkoutZones(db, id, [7]int{0, 0, 3600, 0, 0, 0, 0}, 1); err != nil {
		t.Fatal(err)
	}
	got, err := buildStatus(db, now, false)
	if err != nil {
		t.Fatal(err)
	}
	almostEqual(t, "week.zones.grayPct", got.Week.Zones.GrayPct, 8.9)
	if got.Recommendation.RuleID != 6 {
		t.Errorf("RuleID = %d, хочу 6: серая зона за 4 недели > 20%%", got.Recommendation.RuleID)
	}
}

func TestStatusEmptyDB(t *testing.T) {
	db := openStatusDB(t)
	got, err := buildStatus(db, time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local), false)
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Label != "lowdata" || got.State.Confidence != "low" {
		t.Errorf("state = %+v", got.State)
	}
	if got.Recommendation.RuleID != 0 || got.Recommendation.Kind != "z2" {
		t.Errorf("recommendation = %+v", got.Recommendation)
	}
	if got.Week.Target != nil || got.Week.TargetSource != store.TargetSourceNone {
		t.Errorf("target = %v/%q, хочу null/none", got.Week.Target, got.Week.TargetSource)
	}
	if got.LastWorkout != nil {
		t.Errorf("lastWorkout = %#v, хочу null", got.LastWorkout)
	}
	almostEqual(t, "ctl", got.State.CTL.Value, 0)
	almostEqual(t, "atl", got.State.ATL.Value, 0)
	almostEqual(t, "tsb", got.State.TSB.Value, 0)
	if len(got.AllAlerts) == 0 {
		t.Error("alertsAll пуст, хочу stale и lowdata")
	}
	codes := map[string]bool{}
	for _, a := range got.AllAlerts {
		codes[a.Code] = true
	}
	if !codes["stale"] || !codes["lowdata"] {
		t.Errorf("коды алертов = %v", codes)
	}
	if !strings.Contains(got.Summary, "0 тренировок") {
		t.Errorf("summary = %q", got.Summary)
	}
}

func TestStatusTestWorkoutNotHard(t *testing.T) {
	db := openStatusDB(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	if _, err := store.InsertWorkout(db, &store.Workout{
		StartTime: now.AddDate(0, 0, -5), Duration: time.Hour, TSS: 100,
	}, "hash-hard-old", filepath.Join(t.TempDir(), "h.fit"), 1); err != nil {
		t.Fatal(err)
	}
	testID, err := store.InsertWorkout(db, &store.Workout{
		StartTime: now.AddDate(0, 0, -1), Duration: time.Hour, TSS: 1,
		IF: 0.95, HasIF: true,
	}, "hash-test-yesterday", filepath.Join(t.TempDir(), "t.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkoutTest(db, testID, true); err != nil {
		t.Fatal(err)
	}

	facts, err := loadHistoryFacts(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if facts.daysSinceHard != 5 {
		t.Errorf("daysSinceHard = %d, хочу 5: вчерашний тест не считается", facts.daysSinceHard)
	}
	if facts.daysSinceTest != 1 {
		t.Errorf("daysSinceTest = %d, хочу 1", facts.daysSinceTest)
	}
	if facts.dataDays != 5 {
		t.Errorf("dataDays = %d, хочу 5", facts.dataDays)
	}
	if facts.loadDays42 != 2 {
		t.Errorf("loadDays42 = %d, хочу 2", facts.loadDays42)
	}
}

func TestStatusSeveralWorkoutsPerDay(t *testing.T) {
	db := openStatusDB(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	day := now.AddDate(0, 0, -3)
	var later int64
	for i, tss := range []float64{60, 40} {
		id, err := store.InsertWorkout(db, &store.Workout{
			StartTime: day.Add(time.Duration(8+i) * time.Hour), Duration: time.Hour, TSS: tss,
		}, fmt.Sprintf("hash-multi-day-%d", i), filepath.Join(t.TempDir(), "x.fit"), 1)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			later = id
		}
	}
	facts, err := loadHistoryFacts(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if facts.daysSinceHard != 3 {
		t.Errorf("daysSinceHard = %d, хочу 3: 60+40 за день = 100 TSS", facts.daysSinceHard)
	}
	got, err := buildStatus(db, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Week.Rides != 2 {
		t.Errorf("rides = %d, хочу 2", got.Week.Rides)
	}
	almostEqual(t, "week.tss", got.Week.TSS, 100)
	last, _ := got.LastWorkout.(map[string]any)
	if last == nil {
		t.Fatal("lastWorkout = null")
	}
	if id, _ := last["ID"].(float64); int64(id) != later {
		t.Errorf("lastWorkout.ID = %v, хочу %d (более поздняя стартом)", last["ID"], later)
	}
}

func TestStatusLocalDayBoundary(t *testing.T) {
	db := openStatusDB(t)
	now := time.Date(2026, 10, 31, 0, 30, 0, 0, time.Local)
	for i, start := range []time.Time{
		time.Date(2026, 10, 30, 23, 30, 0, 0, time.Local), // вчера
		time.Date(2026, 10, 31, 0, 10, 0, 0, time.Local),  // сегодня
	} {
		if _, err := store.InsertWorkout(db, &store.Workout{
			StartTime: start, Duration: time.Hour, TSS: float64(30 - i*10),
		}, fmt.Sprintf("hash-boundary-%d", i), filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
			t.Fatal(err)
		}
	}
	got, err := buildStatus(db, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.AsOf != "2026-10-31" {
		t.Errorf("asOf = %q", got.AsOf)
	}
	almostEqual(t, "week.tss", got.Week.TSS, 50)
	if !strings.Contains(got.Summary, "Вчера — 30 TSS") {
		t.Errorf("summary = %q, хочу вчера 30 TSS", got.Summary)
	}
}

func TestStatusAPIAndDebug(t *testing.T) {
	db, _ := statusFixture(t)
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	state, ok := body["state"].(map[string]any)
	if !ok {
		t.Fatalf("state = %#v", body["state"])
	}
	tsb, ok := state["tsb"].(map[string]any)
	if !ok {
		t.Fatalf("state.tsb = %#v, хочу объект с value/verdict/action/trend", state["tsb"])
	}
	if _, ok := tsb["value"].(float64); !ok {
		t.Errorf("state.tsb.value = %#v", tsb["value"])
	}
	if _, ok := body["lastWorkout"].(map[string]any); !ok {
		t.Errorf("lastWorkout = %#v", body["lastWorkout"])
	}
	if _, ok := body["rules"]; ok {
		t.Error("без ?debug=1 поля rules быть не должно")
	}
	if _, ok := body["alertsAll"]; ok {
		t.Error("без ?debug=1 поля alertsAll быть не должно")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status?debug=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("debug: код %d: %s", rec.Code, rec.Body.String())
	}
	var debugBody map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &debugBody); err != nil {
		t.Fatal(err)
	}
	rules, ok := debugBody["rules"].([]any)
	if !ok || len(rules) == 0 {
		t.Errorf("rules = %#v", debugBody["rules"])
	}
	if all, ok := debugBody["alertsAll"].([]any); !ok || len(all) == 0 {
		t.Errorf("debug alertsAll = %#v", debugBody["alertsAll"])
	}
}

func TestStatusDebugAllRules(t *testing.T) {
	db, now := statusFixture(t)
	got, err := buildStatus(db, now, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rules) != 8 {
		t.Fatalf("проверок %d, хочу 8: %+v", len(got.Rules), got.Rules)
	}
	if !got.Rules[7].Applied || got.Rules[7].RuleID != 7 {
		t.Errorf("последняя проверка = %+v", got.Rules[7])
	}
	for _, c := range got.Rules[:7] {
		if c.Applied {
			t.Errorf("правило %d отмечено сработавшим", c.RuleID)
		}
		if c.Reason == "" {
			t.Errorf("правило %d без причины", c.RuleID)
		}
	}
}

func TestPluralRides(t *testing.T) {
	for _, tt := range []struct {
		n    int
		want string
	}{
		{0, "тренировок"}, {1, "тренировка"}, {2, "тренировки"}, {4, "тренировки"},
		{5, "тренировок"}, {10, "тренировок"}, {11, "тренировок"}, {12, "тренировок"},
		{14, "тренировок"}, {21, "тренировка"}, {22, "тренировки"}, {25, "тренировок"},
	} {
		if got := pluralRides(tt.n); got != tt.want {
			t.Errorf("pluralRides(%d) = %q, хочу %q", tt.n, got, tt.want)
		}
	}
}

func TestZ2Minutes(t *testing.T) {
	for _, tt := range []struct {
		tss  float64
		want int
	}{
		{0, 0}, {-10, 0}, {100, 130}, {220, 290}, {10, 15}, {100000, 600},
	} {
		if got := z2Minutes(tt.tss); got != tt.want {
			t.Errorf("z2Minutes(%v) = %d, хочу %d", tt.tss, got, tt.want)
		}
	}
}

func openStatusDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func hasAlertCode(alerts []statusAlert, code string) bool {
	for _, a := range alerts {
		if a.Code == code {
			return true
		}
	}
	return false
}

func TestStatusStaleBoundary(t *testing.T) {
	for _, tt := range []struct {
		days int
		want bool
	}{{7, false}, {8, true}} {
		db := openStatusDB(t)
		now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
		if _, err := store.InsertWorkout(db, &store.Workout{
			StartTime: now.AddDate(0, 0, -tt.days), Duration: time.Hour, TSS: 30,
		}, "hash-stale", filepath.Join(t.TempDir(), "s.fit"), 1); err != nil {
			t.Fatal(err)
		}
		got, err := buildStatus(db, now, false)
		if err != nil {
			t.Fatal(err)
		}
		if hasAlertCode(got.AllAlerts, "stale") != tt.want {
			t.Errorf("%d дн.: stale = %v, хочу %v (%+v)", tt.days, !tt.want, tt.want, got.AllAlerts)
		}
	}
}

func TestStatusMixedHRAndRPE(t *testing.T) {
	db := openStatusDB(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	workouts := []store.Workout{
		{StartTime: now.AddDate(0, 0, -1), Duration: time.Hour, TSS: 30, AvgPower: 100, AvgHR: 120},
		{StartTime: now.AddDate(0, 0, -2), Duration: time.Hour, TSS: 30, AvgPower: 100, RPE: 5},
		{StartTime: now.AddDate(0, 0, -3), Duration: time.Hour, TSS: 30, AvgPower: 100},
	}
	for i := range workouts {
		id, err := store.InsertWorkout(db, &workouts[i], fmt.Sprintf("hash-mixed-%d", i), filepath.Join(t.TempDir(), "m.fit"), 1)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			rpe := 5
			if err := store.SetWorkoutContext(db, id, store.WorkoutContextUpdate{RPE: &rpe}); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := buildStatus(db, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if hasAlertCode(got.AllAlerts, "no_hr") {
		t.Errorf("no_hr при наличии пульса хотя бы в одной тренировке: %+v", got.AllAlerts)
	}
	if hasAlertCode(got.AllAlerts, "no_rpe") {
		t.Errorf("no_rpe при наличии RPE хотя бы в одной тренировке: %+v", got.AllAlerts)
	}
}

func TestStatusAlertsTruncatedAndComplete(t *testing.T) {
	db := openStatusDB(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	for i := 0; i < 3; i++ {
		w := &store.Workout{
			StartTime: now.AddDate(0, 0, -i), Duration: time.Hour, TSS: 30, AvgPower: 100,
		}
		if i == 0 {
			w.NoPowerSec = 600 // > 5% времени — data_gaps
		}
		if _, err := store.InsertWorkout(db, w, fmt.Sprintf("hash-alerts-%d", i), filepath.Join(t.TempDir(), "a.fit"), 1); err != nil {
			t.Fatal(err)
		}
	}
	got, err := buildStatus(db, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Alerts) != 3 {
		t.Errorf("alerts = %d, хочу 3", len(got.Alerts))
	}
	if len(got.AllAlerts) < 4 {
		t.Fatalf("allAlerts = %+v, хочу 4+", got.AllAlerts)
	}
	for _, code := range []string{"data_gaps", "no_hr", "lowdata", "no_rpe"} {
		if !hasAlertCode(got.AllAlerts, code) {
			t.Errorf("нет алерта %s: %+v", code, got.AllAlerts)
		}
	}
	if got.AlertsAll != nil {
		t.Errorf("без debug alertsAll должен быть пуст: %+v", got.AlertsAll)
	}
	debug, err := buildStatus(db, now, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(debug.AlertsAll) != len(debug.AllAlerts) {
		t.Errorf("debug alertsAll = %d, all = %d", len(debug.AlertsAll), len(debug.AllAlerts))
	}
}

func TestStatusLatestWorkoutIgnoresFuture(t *testing.T) {
	db := openStatusDB(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	past, err := store.InsertWorkout(db, &store.Workout{
		StartTime: now.AddDate(0, 0, -1), Duration: time.Hour, TSS: 30,
	}, "hash-latest-past", filepath.Join(t.TempDir(), "p.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertWorkout(db, &store.Workout{
		StartTime: now.AddDate(0, 0, 1), Duration: time.Hour, TSS: 30,
	}, "hash-latest-future", filepath.Join(t.TempDir(), "f.fit"), 1); err != nil {
		t.Fatal(err)
	}
	got, err := buildStatus(db, now, false)
	if err != nil {
		t.Fatal(err)
	}
	last, _ := got.LastWorkout.(map[string]any)
	if last == nil {
		t.Fatal("lastWorkout = null")
	}
	if id, _ := last["ID"].(float64); int64(id) != past {
		t.Errorf("lastWorkout.ID = %v, хочу %d (не будущую)", last["ID"], past)
	}
}

func TestStatePMCOnSameMoment(t *testing.T) {
	db, now := statusFixture(t)
	got, err := buildStatus(db, now, false)
	if err != nil {
		t.Fatal(err)
	}
	almostEqual(t, "tsb", got.State.TSB.Value, got.State.CTL.Value-got.State.ATL.Value)
	if got.PMCAsOf != "2026-10-29" || got.TSBAsOf != "2026-10-29" {
		t.Errorf("asOf = %q/%q, хочу конец предыдущего дня", got.PMCAsOf, got.TSBAsOf)
	}
	if !strings.Contains(got.TSBNote, "до нагрузки сегодняшнего дня") {
		t.Errorf("tsbNote = %q", got.TSBNote)
	}

	// пустой день: TSB всё равно равен CTL − ATL на конец предыдущего дня
	empty := openStatusDB(t)
	got, err = buildStatus(empty, now, false)
	if err != nil {
		t.Fatal(err)
	}
	almostEqual(t, "empty tsb", got.State.TSB.Value, got.State.CTL.Value-got.State.ATL.Value)
}

func TestStatusIncludesBothFTPHints(t *testing.T) {
	db := openStatusDB(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	if err := store.AddProfileEntry(db, "ftp", 200, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.AddProfileEntry(db, "ftp_outdoor", 250, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	addHintWorkout(t, db, true, now.AddDate(0, 0, -2), 250)
	addHintWorkout(t, db, false, now.AddDate(0, 0, -3), 300)

	got, err := buildStatus(db, now, false)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, a := range got.AllAlerts {
		if a.Code == "ftp_hint" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("ftp_hint = %d, хочу обе подсказки: %+v", n, got.AllAlerts)
	}
}

func TestStateTSBBoundaries(t *testing.T) {
	for _, tt := range []struct {
		tsb   float64
		label string
	}{
		{10, "norm"},
		{10.1, "fresh"},
		{-10, "norm"},
		{-10.1, "loaded"},
		{-21, "loaded"},
		{-25, "loaded"},
		{-25.1, "overloaded"},
	} {
		got := buildState(store.Day{}, store.Day{CTL: 0, ATL: -tt.tsb}, historyFacts{dataDays: 100, loadDays42: 30}, nil)
		if got.Label != tt.label {
			t.Errorf("TSB %v: label = %q, хочу %q", tt.tsb, got.Label, tt.label)
		}
	}
}

func TestStatusNullTargetAndWatts(t *testing.T) {
	db := openStatusDB(t)
	got, err := buildStatus(db, time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local), false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Week.Target != nil || got.Week.TargetToDate != nil || got.Week.Remaining != nil ||
		got.Week.RemainingMinZ2Total != nil {
		t.Errorf("week target fields = %+v, хочу null", got.Week)
	}
	if got.Week.TargetSource != store.TargetSourceNone {
		t.Errorf("targetSource = %q, хочу none", got.Week.TargetSource)
	}
	if got.Recommendation.Watts != nil {
		t.Errorf("watts = %v, хочу null без FTP", *got.Recommendation.Watts)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"target":null`, `"targetToDate":null`, `"remaining":null`, `"remainingMinZ2Total":null`, `"watts":null`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("JSON нет %s: %s", want, raw)
		}
	}
}

func TestCTLTextUsesTrend(t *testing.T) {
	if v, _ := ctlText(80, 3); v != "Форма растёт" {
		t.Errorf("растущий тренд при высоком CTL = %q", v)
	}
	if v, _ := ctlText(80, -3); v != "Форма снижается" {
		t.Errorf("падающий тренд при высоком CTL = %q", v)
	}
	if v, _ := ctlText(5, 0); v != "Форма стабильна" {
		t.Errorf("нулевой тренд = %q", v)
	}
}
