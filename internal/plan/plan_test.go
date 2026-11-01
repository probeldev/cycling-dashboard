package plan

import (
	"math"
	"strings"
	"testing"
)

func baseInput() Input {
	return Input{
		DataDays:      100,
		TSB:           0,
		CTL:           40,
		ATL:           40,
		DaysSinceHard: -1,
		DaysSinceTest: 40,
		WeekTSS:       200,
		TargetTSS:     220,
		DayOfWeek:     3,
		GrayPct:       10,
		FTPTrainer:    200,
		FTPOutdoor:    210,
		TrainerShare:  0.7,
	}
}

func TestRules(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   Input
		rule int
		kind string
		zone string
		dur  [2]int
		pct  [2]int
		want string // подстрока rationale
	}{
		{
			name: "0 мало данных",
			in:   func() Input { in := baseInput(); in.DataDays = 10; in.TSB = -40; return in }(),
			rule: 0, kind: "z2", zone: "Z2", dur: [2]int{60, 60}, pct: [2]int{60, 75},
			want: "10 дн.",
		},
		{
			name: "1 перегрузка",
			in:   func() Input { in := baseInput(); in.TSB = -30; return in }(),
			rule: 1, kind: "rest", zone: "", dur: [2]int{0, 0}, pct: [2]int{0, 0},
			want: "−30.0",
		},
		{
			name: "2 просевшая форма",
			in:   func() Input { in := baseInput(); in.TSB = -20; return in }(),
			rule: 2, kind: "z1z2", zone: "Z1-Z2", dur: [2]int{40, 60}, pct: [2]int{55, 75},
			want: "−20.0",
		},
		{
			name: "3 тяжёлая вчера",
			in:   func() Input { in := baseInput(); in.TSB = -10; in.DaysSinceHard = 1; return in }(),
			rule: 3, kind: "z1z2", zone: "Z1-Z2", dur: [2]int{45, 60}, pct: [2]int{55, 75},
			want: "вчера",
		},
		{
			name: "4 FTP-тест на станке",
			in:   func() Input { in := baseInput(); in.TSB = 20; in.DaysSinceTest = 30; in.TrainerShare = 0.7; return in }(),
			rule: 4, kind: "ftp_test", zone: "", dur: [2]int{20, 20}, pct: [2]int{0, 0},
			want: "ramp",
		},
		{
			name: "4 FTP-тест на улице",
			in:   func() Input { in := baseInput(); in.TSB = 20; in.DaysSinceTest = 30; in.TrainerShare = 0.3; return in }(),
			rule: 4, kind: "ftp_test", zone: "", dur: [2]int{20, 20}, pct: [2]int{0, 0},
			want: "улиц",
		},
		{
			name: "5 недобор недели",
			in:   func() Input { in := baseInput(); in.TSB = 8; in.WeekTSS = 100; in.DaysSinceTest = 10; return in }(),
			rule: 5, kind: "z2long", zone: "Z2", dur: [2]int{90, 120}, pct: [2]int{60, 75},
			want: "100",
		},
		{
			name: "6 серая зона",
			in:   func() Input { in := baseInput(); in.GrayPct = 25; return in }(),
			rule: 6, kind: "z2", zone: "Z2", dur: [2]int{60, 75}, pct: [2]int{60, 75},
			want: "25.0",
		},
		{
			name: "7 норма",
			in:   baseInput(),
			rule: 7, kind: "z2", zone: "Z2", dur: [2]int{60, 75}, pct: [2]int{60, 75},
			want: "Форма в норме",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := Recommend(tt.in)
			if rec.RuleID != tt.rule {
				t.Fatalf("RuleID = %d, хочу %d (%+v)", rec.RuleID, tt.rule, rec)
			}
			if rec.Kind != tt.kind {
				t.Errorf("Kind = %q, хочу %q", rec.Kind, tt.kind)
			}
			if rec.Zone != tt.zone {
				t.Errorf("Zone = %q, хочу %q", rec.Zone, tt.zone)
			}
			if rec.DurationMin != tt.dur {
				t.Errorf("DurationMin = %v, хочу %v", rec.DurationMin, tt.dur)
			}
			if rec.PctFTP != tt.pct {
				t.Errorf("PctFTP = %v, хочу %v", rec.PctFTP, tt.pct)
			}
			if !strings.Contains(rec.Rationale, tt.want) {
				t.Errorf("Rationale = %q, хочу подстроку %q", rec.Rationale, tt.want)
			}
			if len(rec.Factors) == 0 || len(rec.Factors) > 4 {
				t.Errorf("Factors = %d, хочу 1..4", len(rec.Factors))
			}
		})
	}
}

func TestRulePriority(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   Input
		want int
	}{
		{"TSB −30 и тяжёлая вчера — отдых", func() Input {
			in := baseInput()
			in.TSB = -30
			in.DaysSinceHard = 1
			return in
		}(), 1},
		{"TSB −20 и тяжёлая вчера — сначала правило 2", func() Input {
			in := baseInput()
			in.TSB = -20
			in.DaysSinceHard = 1
			return in
		}(), 2},
		{"TSB −10 и тяжёлая вчера — правило 3", func() Input {
			in := baseInput()
			in.TSB = -10
			in.DaysSinceHard = 1
			return in
		}(), 3},
		{"мало данных важнее перегрузки", func() Input {
			in := baseInput()
			in.DataDays = 20
			in.TSB = -40
			return in
		}(), 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Recommend(tt.in).RuleID; got != tt.want {
				t.Errorf("RuleID = %d, хочу %d", got, tt.want)
			}
		})
	}
}

func TestBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   Input
		want int
	}{
		{"данных ровно 21 день — уже не lowdata", func() Input {
			in := baseInput()
			in.DataDays = 21
			in.TSB = -30
			return in
		}(), 1},
		{"TSB ровно −25 — ещё не отдых", func() Input {
			in := baseInput()
			in.TSB = -25
			return in
		}(), 2},
		{"TSB чуть ниже −25 — отдых", func() Input {
			in := baseInput()
			in.TSB = -25.001
			return in
		}(), 1},
		{"TSB ровно −15 — не правило 2", func() Input {
			in := baseInput()
			in.TSB = -15
			return in
		}(), 7},
		{"TSB ровно +15 — не тест", func() Input {
			in := baseInput()
			in.TSB = 15
			in.DaysSinceTest = 30
			return in
		}(), 7},
		{"TSB чуть выше +15 — тест", func() Input {
			in := baseInput()
			in.TSB = 15.001
			in.DaysSinceTest = 30
			return in
		}(), 4},
		{"тест ровно 21 день назад — рано", func() Input {
			in := baseInput()
			in.TSB = 20
			in.DaysSinceTest = 21
			return in
		}(), 7},
		{"TSB ровно +5 — не длинная Z2", func() Input {
			in := baseInput()
			in.TSB = 5
			in.WeekTSS = 100
			in.DaysSinceTest = 10
			return in
		}(), 7},
		{"TSB чуть выше +5 — длинная Z2", func() Input {
			in := baseInput()
			in.TSB = 5.001
			in.WeekTSS = 100
			in.DaysSinceTest = 10
			return in
		}(), 5},
		{"ровно 70% цели — ещё не недобор", func() Input {
			in := baseInput()
			in.TSB = 8
			in.WeekTSS = 154
			in.DaysSinceTest = 10
			return in
		}(), 7},
		{"серая зона ровно 20% — не срабатывает", func() Input {
			in := baseInput()
			in.GrayPct = 20
			return in
		}(), 7},
		{"серая зона 20.1% — срабатывает", func() Input {
			in := baseInput()
			in.GrayPct = 20.1
			return in
		}(), 6},
		{"тяжёлая 2 дня назад — правило 3 не срабатывает", func() Input {
			in := baseInput()
			in.TSB = -10
			in.DaysSinceHard = 2
			return in
		}(), 7},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Recommend(tt.in).RuleID; got != tt.want {
				t.Errorf("RuleID = %d, хочу %d", got, tt.want)
			}
		})
	}
}

func TestWattsFromActiveFTP(t *testing.T) {
	for _, tt := range []struct {
		name         string
		trainerShare float64
		trainerFTP   float64
		outdoorFTP   float64
		want         [2]int
	}{
		{"станок", 0.5, 200, 300, [2]int{120, 150}},
		{"улица", 0.49, 200, 300, [2]int{180, 225}},
		{"нет уличного FTP — станковый", 0.2, 200, 0, [2]int{120, 150}},
		{"нулевой FTP", 0.7, 0, 0, [2]int{0, 0}},
		{"дробный FTP округляется", 0.7, 157, 0, [2]int{94, 118}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := baseInput()
			in.TrainerShare = tt.trainerShare
			in.FTPTrainer = tt.trainerFTP
			in.FTPOutdoor = tt.outdoorFTP
			rec := Recommend(in)
			if rec.Watts != tt.want {
				t.Errorf("Watts = %v, хочу %v", rec.Watts, tt.want)
			}
			for _, w := range rec.Watts {
				if math.IsNaN(float64(w)) || math.IsInf(float64(w), 0) {
					t.Errorf("Watts = %v: NaN/Inf", rec.Watts)
				}
			}
		})
	}
}

func TestExplain(t *testing.T) {
	rec, checks := Explain(baseInput())
	if rec.RuleID != 7 {
		t.Fatalf("RuleID = %d, хочу 7", rec.RuleID)
	}
	if len(checks) != 8 {
		t.Fatalf("проверок %d, хочу 8 (правила 0..7): %+v", len(checks), checks)
	}
	for i, c := range checks {
		if c.RuleID != i {
			t.Errorf("проверка %d: RuleID = %d", i, c.RuleID)
		}
		if c.Reason == "" {
			t.Errorf("правило %d: пустая причина", i)
		}
		if c.Applied != (i == 7) {
			t.Errorf("правило %d: Applied = %v", i, c.Applied)
		}
	}
	if !strings.Contains(checks[1].Reason, "нужно < −25") {
		t.Errorf("причина правила 1 = %q", checks[1].Reason)
	}

	rec, checks = Explain(func() Input {
		in := baseInput()
		in.TSB = -30
		return in
	}())
	if rec.RuleID != 1 || len(checks) != 2 || !checks[1].Applied {
		t.Errorf("ранний выход: rec=%+v checks=%+v", rec, checks)
	}
}
