package store

import (
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"
)

// withLocal делает тест независимым от зоны машины: time.Local подменяется на
// явную зону, чтобы мутация Local→UTC ловилась.
func withLocal(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	old := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = old })
	return loc
}

func dayOf(t *testing.T, d int) time.Time {
	t.Helper()
	return time.Date(2026, 10, d, 12, 0, 0, 0, time.Local)
}

// insertTimelineWorkout — тренировка с заданным TSS и длительностью.
func insertTimelineWorkout(t *testing.T, db *sql.DB, hash string, start time.Time, tss float64, durMin int, distKm float64, trainer bool) int64 {
	t.Helper()
	id, err := InsertWorkout(db, &Workout{
		StartTime: start, Duration: time.Duration(durMin) * time.Minute,
		DistanceKm: distKm, Trainer: trainer, TSS: tss,
	}, hash, filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestTimelinePMC — один день с TSS 100: CTL=100/42, ATL=100/7, TSB=0.
func TestTimelinePMC(t *testing.T) {
	withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	insertTimelineWorkout(t, db, "hash-tl-1", dayOf(t, 1), 100, 60, 20, false)

	days, err := Timeline(db, dayOf(t, 1), dayOf(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 {
		t.Fatalf("%d дней, хочу 1", len(days))
	}
	d := days[0]
	if !near(d.TSS, 100) {
		t.Errorf("TSS = %v, хочу 100", d.TSS)
	}
	if !near(d.CTL, 100.0/42) || !near(d.ATL, 100.0/7) {
		t.Errorf("CTL=%v ATL=%v, хочу %v и %v", d.CTL, d.ATL, 100.0/42, 100.0/7)
	}
	if d.TSB != 0 {
		t.Errorf("TSB = %v, хочу 0", d.TSB)
	}
	if d.Rides != 1 || d.OutdoorRides != 1 || d.TrainerRides != 0 {
		t.Errorf("поездки: %+v", d)
	}
	if !near(d.Hours, 1) || !near(d.DistanceKm, 20) {
		t.Errorf("часы/км: %v / %v", d.Hours, d.DistanceKm)
	}
}

// TestTimelinePMCChain — рекурсия и TSB второго дня = CTL_1 − ATL_1.
func TestTimelinePMCChain(t *testing.T) {
	withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	insertTimelineWorkout(t, db, "hash-tl-c1", dayOf(t, 1), 100, 60, 20, true)
	insertTimelineWorkout(t, db, "hash-tl-c2", dayOf(t, 2), 50, 30, 10, false)

	days, err := Timeline(db, dayOf(t, 1), dayOf(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	ctl1, atl1 := 100.0/42, 100.0/7
	ctl2 := ctl1 + (50-ctl1)/42
	atl2 := atl1 + (50-atl1)/7
	if !near(days[1].TSB, ctl1-atl1) {
		t.Errorf("TSB второго дня = %v, хочу %v", days[1].TSB, ctl1-atl1)
	}
	if !near(days[1].CTL, ctl2) || !near(days[1].ATL, atl2) {
		t.Errorf("CTL/ATL второго дня = %v/%v, хочу %v/%v", days[1].CTL, days[1].ATL, ctl2, atl2)
	}
	if days[0].TrainerRides != 1 || days[1].OutdoorRides != 1 {
		t.Errorf("тип поездок: %+v / %+v", days[0], days[1])
	}
}

// TestTimelineEmptyDayKeepsCurve — день без тренировок не разрывает EWMA.
func TestTimelineEmptyDayKeepsCurve(t *testing.T) {
	withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	insertTimelineWorkout(t, db, "hash-tl-e1", dayOf(t, 1), 100, 60, 20, false)
	insertTimelineWorkout(t, db, "hash-tl-e3", dayOf(t, 3), 100, 60, 20, false)

	days, err := Timeline(db, dayOf(t, 1), dayOf(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 3 {
		t.Fatalf("%d дней, хочу 3", len(days))
	}
	ctl1, atl1 := 100.0/42, 100.0/7
	ctl2 := ctl1 + (0-ctl1)/42
	atl2 := atl1 + (0-atl1)/7
	if days[1].TSS != 0 || days[1].Rides != 0 {
		t.Errorf("пустой день: %+v", days[1])
	}
	if !near(days[1].CTL, ctl2) || !near(days[1].ATL, atl2) {
		t.Errorf("CTL/ATL пустого дня = %v/%v, хочу %v/%v", days[1].CTL, days[1].ATL, ctl2, atl2)
	}
	if !near(days[2].TSB, ctl2-atl2) {
		t.Errorf("TSB третьего дня = %v, хочу %v", days[2].TSB, ctl2-atl2)
	}
}

// TestTimelineBoundsAndSort — границы включительно, порядок по дате.
func TestTimelineBoundsAndSort(t *testing.T) {
	withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	insertTimelineWorkout(t, db, "hash-tl-b1", dayOf(t, 1), 10, 10, 5, false)
	insertTimelineWorkout(t, db, "hash-tl-b3", dayOf(t, 3), 20, 20, 10, false)

	days, err := Timeline(db, dayOf(t, 1), dayOf(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 3 {
		t.Fatalf("%d дней, хочу 3", len(days))
	}
	for i := 1; i < len(days); i++ {
		if !days[i].Date.After(days[i-1].Date) {
			t.Fatalf("дата %v не позже %v", days[i].Date, days[i-1].Date)
		}
	}
	if !days[0].Date.Equal(StartOfDay(dayOf(t, 1))) || !days[2].Date.Equal(StartOfDay(dayOf(t, 3))) {
		t.Errorf("границы диапазона: %v..%v", days[0].Date, days[2].Date)
	}
}

// TestTimelineEmptyRange — пустой/обратный диапазон и диапазон без тренировок.
func TestTimelineEmptyRange(t *testing.T) {
	withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	if days, err := Timeline(db, dayOf(t, 5), dayOf(t, 1)); err != nil || days != nil {
		t.Errorf("обратный диапазон: %v, %v; хочу nil", days, err)
	}
	days, err := Timeline(db, dayOf(t, 10), dayOf(t, 12))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 3 {
		t.Fatalf("%d дней, хочу 3", len(days))
	}
	for _, d := range days {
		if d.CTL != 0 || d.ATL != 0 || d.TSB != 0 || d.Rides != 0 {
			t.Errorf("пустой диапазон дал данные: %+v", d)
		}
	}
}

// TestTimelineLocalDay — день тренировки определяется по локальной дате,
// даже если в UTC этот момент другого дня. Зоны фиксированные, без скипов.
func TestTimelineLocalDay(t *testing.T) {
	cases := []struct {
		zone      string
		hour, min int
	}{
		{"Europe/Minsk", 0, 30},      // 00:30 MSK = 21:30 UTC предыдущего дня
		{"America/New_York", 20, 30}, // 20:30 EDT = 00:30 UTC следующего дня
	}
	for _, c := range cases {
		t.Run(c.zone, func(t *testing.T) {
			loc := withLocal(t, c.zone)
			local := time.Date(2026, 10, 31, c.hour, c.min, 0, 0, loc)
			if local.UTC().Day() == local.Day() {
				t.Fatalf("момент %v не пересекает границу UTC-даты", local)
			}
			db := openTestDB(t)
			insertTimelineWorkout(t, db, "hash-tl-tz", local, 40, 30, 10, false)

			days, err := Timeline(db, local, local)
			if err != nil {
				t.Fatal(err)
			}
			if len(days) != 1 || days[0].Rides != 1 || !days[0].Date.Equal(StartOfDay(local)) {
				t.Fatalf("тренировка попала не в локальный день: %+v", days)
			}
			prev, err := Timeline(db, local.AddDate(0, 0, -1), local.AddDate(0, 0, -1))
			if err != nil {
				t.Fatal(err)
			}
			if prev[0].Rides != 0 {
				t.Errorf("поездка засчитана и в предыдущий день")
			}
		})
	}
}

// TestWeekStart — понедельник в явной зоне; мутация Local→UTC должна ловиться.
func TestWeekStart(t *testing.T) {
	for _, zone := range []string{"Europe/Minsk", "America/New_York"} {
		t.Run(zone, func(t *testing.T) {
			loc := withLocal(t, zone)
			ws := WeekStart(time.Date(2026, 10, 15, 12, 0, 0, 0, loc)) // четверг
			want := time.Date(2026, 10, 12, 0, 0, 0, 0, loc)           // понедельник
			if !ws.Equal(want) || ws.Weekday() != time.Monday {
				t.Fatalf("WeekStart = %v (%v), хочу %v (понедельник)", ws, ws.Weekday(), want)
			}
			if !WeekStart(ws).Equal(ws) {
				t.Errorf("WeekStart не идемпотентен")
			}
			sun := ws.AddDate(0, 0, 6).Add(23 * time.Hour)
			if !WeekStart(sun).Equal(ws) {
				t.Errorf("воскресенье %v попало не в свою неделю", sun)
			}
			next := ws.AddDate(0, 0, 7).Add(30 * time.Minute)
			if !WeekStart(next).Equal(ws.AddDate(0, 0, 7)) {
				t.Errorf("понедельник %v попал не в следующую неделю", next)
			}
		})
	}
}

// TestTimelineWarmUp — CTL/ATL/TSB на общую дату не зависят от окна: EWMA
// греется с самой ранней тренировки, а не с from.
func TestTimelineWarmUp(t *testing.T) {
	withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	insertTimelineWorkout(t, db, "hash-wu-1", now.AddDate(0, 0, -100), 80, 60, 20, false)
	insertTimelineWorkout(t, db, "hash-wu-2", now.AddDate(0, 0, -10), 120, 60, 20, false)

	short, err := Timeline(db, now.AddDate(0, 0, -29), now)
	if err != nil {
		t.Fatal(err)
	}
	long, err := Timeline(db, now.AddDate(0, 0, -364), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(short) != 30 || len(long) != 365 {
		t.Fatalf("длины окон: %d и %d, хочу 30 и 365", len(short), len(long))
	}
	a, b := short[len(short)-1], long[len(long)-1]
	if math.Abs(a.CTL-b.CTL) > 0.01 || math.Abs(a.ATL-b.ATL) > 0.01 || math.Abs(a.TSB-b.TSB) > 0.01 {
		t.Errorf("CTL/ATL/TSB зависят от окна: %.4f/%.4f/%.4f vs %.4f/%.4f/%.4f",
			a.CTL, a.ATL, a.TSB, b.CTL, b.ATL, b.TSB)
	}
	// и на общей дате в середине окна
	mid := long[len(long)-20]
	if i := len(short) - 20; i >= 0 {
		if math.Abs(short[i].CTL-mid.CTL) > 0.01 || math.Abs(short[i].TSB-mid.TSB) > 0.01 {
			t.Errorf("середина окна: %.4f/%.4f vs %.4f/%.4f",
				short[i].CTL, short[i].TSB, mid.CTL, mid.TSB)
		}
	}
}

// TestTimelineUntilIgnoresFuture — будущая тренировка сегодня не попадает
// в день, даже если его дата входит в запрошенный диапазон.
func TestTimelineUntilIgnoresFuture(t *testing.T) {
	withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	insertTimelineWorkout(t, db, "hash-tu-past", now.Add(-2*time.Hour), 30, 60, 20, false)
	insertTimelineWorkout(t, db, "hash-tu-future", now.Add(2*time.Hour), 1000, 240, 200, false)

	days, err := TimelineUntil(db, now.AddDate(0, 0, -1), now, now)
	if err != nil {
		t.Fatal(err)
	}
	today := days[len(days)-1]
	if !near(today.TSS, 30) || today.Rides != 1 {
		t.Errorf("день = %+v, хочу 30 TSS / 1 поездку без будущей", today)
	}
}
