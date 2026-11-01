package plan

import (
	"math"
	"strings"
	"testing"
)

func baseInput() Input {
	return Input{
		DataDays:      100,
		LoadDays42:    30,
		TSB:           0,
		CTL:           40,
		ATL:           40,
		DaysSinceHard: -1,
		DaysSinceTest: 40,
		WeekTSS:       200,
		TargetTSS:     220,
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
			in:   func() Input { in := baseInput(); in.DataDays = 10; return in }(),
			rule: 0, kind: "z2", zone: "Z2", dur: [2]int{60, 60}, pct: [2]int{60, 75},
			want: "10 дн.",
		},
		{
			name: "0 мало дней с нагрузкой",
			in: func() Input {
				in := baseInput()
				in.DataDays = 100
				in.LoadDays42 = 8
				return in
			}(),
			rule: 0, kind: "z2", zone: "Z2", dur: [2]int{60, 60}, pct: [2]int{60, 75},
			want: "8",
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
			name: "4 теста не было",
			in: func() Input {
				in := baseInput()
				in.TSB = 20
				in.DaysSinceTest = -1
				return in
			}(),
			rule: 4, kind: "ftp_test", zone: "", dur: [2]int{20, 20}, pct: [2]int{0, 0},
			want: "теста не было",
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
			if len(rec.Factors) == 0 || len(rec.Factors) > 5 {
				t.Errorf("Factors = %d, хочу 1..5", len(rec.Factors))
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
		{"мало данных не отменяет безопасность", func() Input {
			in := baseInput()
			in.DataDays = 4
			in.LoadDays42 = 4
			in.TSB = -22
			in.DaysSinceHard = 1
			return in
		}(), 2},
		{"мало данных важнее теста и объёма", func() Input {
			in := baseInput()
			in.DataDays = 4
			in.LoadDays42 = 4
			in.TSB = 20
			in.DaysSinceTest = 30
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
		{"данных ровно 21 день и 9 дней с нагрузкой — уже не lowdata", func() Input {
			in := baseInput()
			in.DataDays = 21
			in.LoadDays42 = 9
			in.TSB = -30
			return in
		}(), 1},
		{"20 дней истории — lowdata", func() Input {
			in := baseInput()
			in.DataDays = 20
			in.LoadDays42 = 30
			in.TSB = 0
			return in
		}(), 0},
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
		{"TSB −15.1 — правило 2", func() Input {
			in := baseInput()
			in.TSB = -15.1
			return in
		}(), 2},
		{"TSB −20 — правило 2", func() Input {
			in := baseInput()
			in.TSB = -20
			return in
		}(), 2},
		{"TSB −21 — правило 2", func() Input {
			in := baseInput()
			in.TSB = -21
			return in
		}(), 2},
		{"TSB ровно +10 — норма", func() Input {
			in := baseInput()
			in.TSB = 10
			in.DaysSinceTest = 30
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
		{"тест 22 дня назад — уже можно", func() Input {
			in := baseInput()
			in.TSB = 20
			in.DaysSinceTest = 22
			return in
		}(), 4},
		{"тяжёлая вчера блокирует тест", func() Input {
			in := baseInput()
			in.TSB = 20
			in.DaysSinceTest = 30
			in.DaysSinceHard = 1
			return in
		}(), 7},
		{"тяжёлая 2 дня назад не блокирует тест", func() Input {
			in := baseInput()
			in.TSB = 20
			in.DaysSinceTest = 30
			in.DaysSinceHard = 2
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
		want         *[2]int
	}{
		{"станок", 0.5, 200, 300, &[2]int{120, 150}},
		{"улица", 0.49, 200, 300, &[2]int{180, 225}},
		{"нет уличного FTP — станковый", 0.2, 200, 0, &[2]int{120, 150}},
		{"станковый FTP 0 — уличный", 0.5, 0, 300, &[2]int{180, 225}},
		{"нулевой FTP", 0.7, 0, 0, nil},
		{"NaN FTP", 0.5, math.NaN(), 200, &[2]int{120, 150}},
		{"Inf FTP", 0.5, math.Inf(1), 0, nil},
		{"дробный FTP округляется", 0.7, 157, 0, &[2]int{94, 118}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := baseInput()
			in.TrainerShare = tt.trainerShare
			in.FTPTrainer = tt.trainerFTP
			in.FTPOutdoor = tt.outdoorFTP
			rec := Recommend(in)
			if tt.want == nil {
				if rec.Watts != nil {
					t.Fatalf("Watts = %v, хочу null", *rec.Watts)
				}
				return
			}
			if rec.Watts == nil {
				t.Fatalf("Watts = null, хочу %v", *tt.want)
			}
			if *rec.Watts != *tt.want {
				t.Errorf("Watts = %v, хочу %v", *rec.Watts, *tt.want)
			}
			for _, w := range rec.Watts {
				if math.IsNaN(float64(w)) || math.IsInf(float64(w), 0) {
					t.Errorf("Watts = %v: NaN/Inf", *rec.Watts)
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
		t.Fatalf("проверок %d, хочу 8 (правила 1,2,3,0,4,5,6,7): %+v", len(checks), checks)
	}
	wantOrder := []int{1, 2, 3, 0, 4, 5, 6, 7}
	for i, c := range checks {
		if c.RuleID != wantOrder[i] {
			t.Errorf("проверка %d: RuleID = %d, хочу %d", i, c.RuleID, wantOrder[i])
		}
		if c.Reason == "" {
			t.Errorf("правило %d: пустая причина", c.RuleID)
		}
		if c.Applied != (i == len(checks)-1) {
			t.Errorf("правило %d: Applied = %v", c.RuleID, c.Applied)
		}
	}
	if !strings.Contains(checks[0].Reason, "нужно < −25") {
		t.Errorf("причина правила 1 = %q", checks[0].Reason)
	}

	rec, checks = Explain(func() Input {
		in := baseInput()
		in.TSB = -30
		return in
	}())
	if rec.RuleID != 1 || len(checks) != 1 || !checks[0].Applied {
		t.Errorf("ранний выход: rec=%+v checks=%+v", rec, checks)
	}
}

func TestSafetyBeforeLowData(t *testing.T) {
	in := baseInput()
	in.DataDays = 4
	in.LoadDays42 = 4
	in.TSB = -22
	in.DaysSinceHard = 1
	rec := Recommend(in)
	if rec.RuleID != 2 || rec.Kind != "z1z2" || rec.DurationMin != [2]int{40, 60} {
		t.Fatalf("rec = %+v, хочу правило 2 Z1–Z2 40–60, а не 60 мин Z2", rec)
	}
}

func TestFTPFactorSeparateFromZone(t *testing.T) {
	in := baseInput()
	in.TSB = 20
	in.DaysSinceTest = 30
	in.TrainerShare = 0.7
	in.FTPTrainer = 200
	in.FTPOutdoor = 300
	rec := Recommend(in)
	got := map[string]string{}
	for _, f := range rec.Factors {
		got[f.Label] = f.Value
	}
	if got["FTP"] != "200 Вт" {
		t.Errorf("FTP = %q, хочу действующий 200 Вт", got["FTP"])
	}
	if got["Z2"] != "120–150 Вт" {
		t.Errorf("Z2 = %q, хочу диапазон 120–150 Вт", got["Z2"])
	}
}

func TestRuleSevenDoesNotClaimUncheckedVolume(t *testing.T) {
	in := baseInput()
	in.TargetTSS = 0
	in.WeekTSS = 50
	rec := Recommend(in)
	if rec.RuleID != 7 {
		t.Fatalf("RuleID = %d, хочу 7", rec.RuleID)
	}
	if strings.Contains(rec.Rationale, "выполняется") {
		t.Errorf("Rationale = %q: объём не проверен, нельзя утверждать выполнение", rec.Rationale)
	}
	got := map[string]string{}
	for _, f := range rec.Factors {
		got[f.Label] = f.Value
	}
	if got["Неделя"] != "50 / — TSS" {
		t.Errorf("Неделя = %q, хочу «50 / — TSS»", got["Неделя"])
	}
}

func TestRuleSevenChecksSeventyPercent(t *testing.T) {
	in := baseInput()
	in.WeekTSS = 154 // ровно 70% от 220
	rec := Recommend(in)
	if rec.RuleID != 7 || !strings.Contains(rec.Rationale, "70%") {
		t.Errorf("rec = %+v, хочу нейтральную Z2 с проверкой 70%%", rec)
	}
}
