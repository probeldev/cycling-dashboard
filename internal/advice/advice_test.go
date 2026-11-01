package advice

import (
	"math"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// baseWorkout — тренировка без единого сработавшего правила: дальше тест
// меняет только нужное поле, чтобы проверять правило изолированно.
func baseWorkout() (store.Workout, Context) {
	return store.Workout{
		Duration:   time.Hour,
		AvgCadence: 85,
		HasPower:   true,
		AvgPower:   150,
		HasVI:      true,
		VI:         1.0,
		HasTSS:     true,
		TSS:        50,
		AvgHR:      130,
		MaxHR:      160,
		RPE:        5,
	}, Context{}
}

func hasCard(cards []Card, title string) bool {
	for _, c := range cards {
		if c.Title == title {
			return true
		}
	}
	return false
}

func TestRuleLowCadence(t *testing.T) {
	for _, tt := range []struct {
		name    string
		cadence int
		trainer bool
		want    bool
	}{
		{"69 на улице", 69, false, true},
		{"70 на улице", 70, false, false},
		{"60 на станке", 60, true, false},
		{"каденса нет", 0, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, ctx := baseWorkout()
			w.AvgCadence, w.Trainer = tt.cadence, tt.trainer
			if got := hasCard(For(w, ctx), "Низкий каденс"); got != tt.want {
				t.Errorf("Низкий каденс = %v, хочу %v", got, tt.want)
			}
		})
	}
}

func TestRuleHighVI(t *testing.T) {
	for _, tt := range []struct {
		name  string
		vi    float64
		hasVI bool
		want  bool
	}{
		{"1.15 ровно", 1.15, true, false},
		{"1.151", 1.151, true, true},
		{"HasVI false", 2, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, ctx := baseWorkout()
			w.VI, w.HasVI = tt.vi, tt.hasVI
			if got := hasCard(For(w, ctx), "Рваная езда"); got != tt.want {
				t.Errorf("Рваная езда = %v, хочу %v", got, tt.want)
			}
		})
	}
}

func TestRuleCoasting(t *testing.T) {
	for _, tt := range []struct {
		name  string
		coast int
		dur   time.Duration
		want  bool
	}{
		{"20% ровно", 200, 1000 * time.Second, false},
		{"20.1%", 201, 1000 * time.Second, true},
		{"нет времени", 0, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, ctx := baseWorkout()
			w.CoastingSec, w.Duration = tt.coast, tt.dur
			if got := hasCard(For(w, ctx), "Много наката"); got != tt.want {
				t.Errorf("Много наката = %v, хочу %v", got, tt.want)
			}
		})
	}
}

func TestRuleDataGaps(t *testing.T) {
	for _, tt := range []struct {
		name   string
		noPwr  int
		susp   int
		hasPwr bool
		want   bool
	}{
		{"5% ровно", 40, 10, true, false},
		{"5.1%", 41, 10, true, true},
		{"нет поверметра", 1000, 0, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, ctx := baseWorkout()
			w.Duration = 1000 * time.Second
			w.NoPowerSec, w.SuspectDropoutSec, w.HasPower = tt.noPwr, tt.susp, tt.hasPwr
			if got := hasCard(For(w, ctx), "Пропуски данных"); got != tt.want {
				t.Errorf("Пропуски данных = %v, хочу %v", got, tt.want)
			}
		})
	}
}

func TestRuleNoHR(t *testing.T) {
	for _, tt := range []struct {
		name  string
		avgHR int
		maxHR int
		dur   time.Duration
		want  bool
	}{
		{"пульса нет", 0, 0, time.Hour, true},
		{"пульс есть", 120, 150, time.Hour, false},
		{"пустая тренировка", 0, 0, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, ctx := baseWorkout()
			w.AvgHR, w.MaxHR, w.Duration = tt.avgHR, tt.maxHR, tt.dur
			if got := hasCard(For(w, ctx), "Пульса нет"); got != tt.want {
				t.Errorf("Пульса нет = %v, хочу %v", got, tt.want)
			}
		})
	}
}

func TestRuleHighZones(t *testing.T) {
	for _, tt := range []struct {
		name  string
		zones [7]int
		want  bool
	}{
		{"40% ровно", [7]int{600, 0, 0, 400, 0, 0, 0}, false},
		{"40.1%", [7]int{599, 0, 0, 401, 0, 0, 0}, true},
		{"Z5", [7]int{0, 0, 0, 0, 1000, 0, 0}, true},
		{"Z6", [7]int{0, 0, 0, 0, 0, 1000, 0}, true},
		{"Z7", [7]int{0, 0, 0, 0, 0, 0, 1000}, true},
		{"Z3 не порог", [7]int{0, 0, 1000, 0, 0, 0, 0}, false},
		{"зон нет", [7]int{}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, ctx := baseWorkout()
			ctx.Zones = tt.zones
			if got := hasCard(For(w, ctx), "Много пороговой работы"); got != tt.want {
				t.Errorf("Много пороговой работы = %v, хочу %v", got, tt.want)
			}
		})
	}
}

func TestRuleTwoHeavyDays(t *testing.T) {
	for _, tt := range []struct {
		name      string
		tss       float64
		yesterday float64
		hasTSS    bool
		want      bool
	}{
		{"TSS 100 ровно", 100, 90, true, false},
		{"вчера 80 ровно", 100.1, 80, true, false},
		{"оба выше", 100.1, 80.1, true, true},
		{"вчера мало", 100.1, 0, true, false},
		{"HasTSS false", 200, 100, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, ctx := baseWorkout()
			w.TSS, ctx.YesterdayTSS, w.HasTSS = tt.tss, tt.yesterday, tt.hasTSS
			if got := hasCard(For(w, ctx), "Два тяжёлых дня подряд"); got != tt.want {
				t.Errorf("Два тяжёлых дня подряд = %v, хочу %v", got, tt.want)
			}
		})
	}
}

func TestRuleLowTSB(t *testing.T) {
	for _, tt := range []struct {
		tsb  float64
		want bool
	}{
		{-20, false},
		{-20.1, true},
		{0, false},
	} {
		w, ctx := baseWorkout()
		ctx.TSB = tt.tsb
		if got := hasCard(For(w, ctx), "Накопленная усталость"); got != tt.want {
			t.Errorf("TSB=%v: Накопленная усталость = %v, хочу %v", tt.tsb, got, tt.want)
		}
	}
}

func TestRuleEasyRPE(t *testing.T) {
	for _, tt := range []struct {
		name   string
		rpe    int
		tss    float64
		hasTSS bool
		want   bool
	}{
		{"RPE 4 при TSS 100.1", 4, 100.1, true, true},
		{"RPE 5", 5, 200, true, false},
		{"TSS 100 ровно", 4, 100, true, false},
		{"RPE не задан — правило 10", 0, 200, true, false},
		{"HasTSS false", 4, 200, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, ctx := baseWorkout()
			w.RPE, w.TSS, w.HasTSS = tt.rpe, tt.tss, tt.hasTSS
			if got := hasCard(For(w, ctx), "Субъективно легко"); got != tt.want {
				t.Errorf("Субъективно легко = %v, хочу %v", got, tt.want)
			}
		})
	}
}

func TestRuleNoRPE(t *testing.T) {
	for _, tt := range []struct {
		rpe  int
		want bool
	}{
		{0, true},
		{1, false},
	} {
		w, ctx := baseWorkout()
		w.RPE = tt.rpe
		if got := hasCard(For(w, ctx), "Нет RPE"); got != tt.want {
			t.Errorf("RPE=%d: Нет RPE = %v, хочу %v", tt.rpe, got, tt.want)
		}
	}
}

func TestRuleGrayZone(t *testing.T) {
	for _, tt := range []struct {
		gray float64
		want bool
	}{
		{20, false},
		{20.1, true},
		{0, false},
	} {
		w, ctx := baseWorkout()
		ctx.WeekGrayPct = tt.gray
		if got := hasCard(For(w, ctx), "Много серой зоны"); got != tt.want {
			t.Errorf("серая зона %v: %v, хочу %v", tt.gray, got, tt.want)
		}
	}
}

func TestRuleGoodDecoupling(t *testing.T) {
	for _, tt := range []struct {
		name string
		dec  *float64
		vi   float64
		want bool
	}{
		{"4.9% и VI 1.07", ptr(4.9), 1.07, true},
		{"5% ровно", ptr(5.0), 1.0, false},
		{"VI 1.08 ровно", ptr(4.9), 1.08, false},
		{"пульса нет", nil, 1.0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, ctx := baseWorkout()
			ctx.Decoupling, w.VI = tt.dec, tt.vi
			if got := hasCard(For(w, ctx), "Ровная работа"); got != tt.want {
				t.Errorf("Ровная работа = %v, хочу %v", got, tt.want)
			}
		})
	}
}

func ptr(v float64) *float64 { return &v }

// TestPriorityAndLimit — warn идёт первым, лимит 5 отрезает хвост: шестая
// карточка «Нет RPE» отброшена.
func TestPriorityAndLimit(t *testing.T) {
	w, ctx := baseWorkout()
	w.AvgCadence = 60                               // info
	w.VI = 1.2                                      // warn
	w.CoastingSec = int(w.Duration.Seconds() * 0.3) // info
	w.NoPowerSec = int(w.Duration.Seconds() * 0.1)  // warn
	w.AvgHR, w.MaxHR = 0, 0                         // info
	w.RPE = 0                                       // info
	cards := For(w, ctx)
	if len(cards) != 5 {
		t.Fatalf("карточек %d, хочу 5 (MaxCards=%d)", len(cards), MaxCards)
	}
	if hasCard(cards, "Нет RPE") {
		t.Error("шестая карточка «Нет RPE» не отброшена")
	}
	wantLevels := []string{LevelWarn, LevelWarn, LevelInfo, LevelInfo, LevelInfo}
	wantTitles := []string{"Рваная езда", "Пропуски данных", "Низкий каденс", "Много наката", "Пульса нет"}
	for i := range cards {
		if cards[i].Level != wantLevels[i] || cards[i].Title != wantTitles[i] {
			t.Errorf("карточка %d = %s/%s, хочу %s/%s", i, cards[i].Level, cards[i].Title, wantLevels[i], wantTitles[i])
		}
	}
}

// TestSortWarnInfoGood — все три уровня идут в порядке warn → info → good.
func TestSortWarnInfoGood(t *testing.T) {
	w, ctx := baseWorkout()
	w.AvgCadence = 60
	w.NoPowerSec = 400
	ctx.Decoupling = ptr(4.0)
	cards := For(w, ctx)
	want := []string{"Пропуски данных", "Низкий каденс", "Ровная работа"}
	if len(cards) != len(want) {
		t.Fatalf("карточек %d, хочу %d: %+v", len(cards), len(want), cards)
	}
	for i := range cards {
		if cards[i].Title != want[i] {
			t.Errorf("карточка %d = %q, хочу %q", i, cards[i].Title, want[i])
		}
	}
}

// TestEmptyData — у пустой тренировки единственное осмысленное напоминание:
// поставить RPE. Пакет не паникует и не выдумывает «пульса нет».
func TestEmptyData(t *testing.T) {
	cards := For(store.Workout{}, Context{})
	if len(cards) != 1 || cards[0].Title != "Нет RPE" || cards[0].Level != LevelInfo {
		t.Errorf("пустая тренировка: %+v", cards)
	}
}

func TestDecoupling(t *testing.T) {
	t.Run("пусто", func(t *testing.T) {
		if _, ok := Decoupling(nil); ok {
			t.Error("decoupling посчитан без сэмплов")
		}
	})

	t.Run("ровно", func(t *testing.T) {
		var samples []store.Sample
		for i := 0; i < 600; i++ {
			samples = append(samples, store.Sample{Offset: int64(i), Power: 200, HasPwr: true, HR: 140})
		}
		dec, ok := Decoupling(samples)
		if !ok || math.Abs(dec) > 1e-9 {
			t.Errorf("ровные данные: dec=%v ok=%v", dec, ok)
		}
	})

	t.Run("пульс вырос", func(t *testing.T) {
		var samples []store.Sample
		for i := 0; i < 600; i++ {
			hr := 140
			if i >= 300 {
				hr = 160
			}
			samples = append(samples, store.Sample{Offset: int64(i), Power: 200, HasPwr: true, HR: hr})
		}
		dec, ok := Decoupling(samples)
		if !ok || math.Abs(dec-12.5) > 1e-9 {
			t.Errorf("дрейф пульса: dec=%v ok=%v, хочу 12.5", dec, ok)
		}
	})

	t.Run("нет пульса", func(t *testing.T) {
		samples := []store.Sample{{Offset: 0, Power: 200, HasPwr: true}, {Offset: 600, Power: 200, HasPwr: true}}
		if _, ok := Decoupling(samples); ok {
			t.Error("decoupling посчитан без пульса")
		}
	})

	t.Run("мало данных", func(t *testing.T) {
		var samples []store.Sample
		for i := 0; i < 200; i++ {
			samples = append(samples, store.Sample{Offset: int64(i), Power: 200, HasPwr: true, HR: 140})
		}
		if _, ok := Decoupling(samples); ok {
			t.Error("decoupling посчитан по 200 секундам")
		}
	})

	t.Run("запись пересекает середину", func(t *testing.T) {
		var samples []store.Sample
		for off := 0; off <= 600; off += 5 {
			samples = append(samples, store.Sample{Offset: int64(off), Power: 200, HasPwr: true, HR: 140})
		}
		if _, ok := Decoupling(samples); !ok {
			t.Error("decoupling не посчитан, хотя по 5 минут на половину есть")
		}
	})

	t.Run("нулевая мощность", func(t *testing.T) {
		var samples []store.Sample
		for i := 0; i < 600; i++ {
			samples = append(samples, store.Sample{Offset: int64(i), Power: 0, HasPwr: true, HR: 140})
		}
		if _, ok := Decoupling(samples); ok {
			t.Error("decoupling посчитан по нулевой мощности")
		}
	})

	t.Run("накат не входит в EF", func(t *testing.T) {
		// 300 с наката только в первой половине, дальше 1200 с ровного
		// педалирования: при учёте наката EF первой половины падает и
		// decoupling уезжает в минус, хотя дрейфа нет.
		var samples []store.Sample
		for i := 0; i < 300; i++ {
			samples = append(samples, store.Sample{Offset: int64(i), HasPwr: true, HR: 140})
		}
		for i := 0; i < 1200; i++ {
			samples = append(samples, store.Sample{
				Offset: int64(300 + i), Power: 200, HasPwr: true, HR: 140,
			})
		}
		dec, ok := Decoupling(samples)
		if !ok || math.Abs(dec) > 1e-9 {
			t.Errorf("накат сломал EF: dec=%v ok=%v", dec, ok)
		}
	})

	t.Run("дропаут без пульса пропускается", func(t *testing.T) {
		var samples []store.Sample
		for i := 0; i < 600; i++ {
			samples = append(samples, store.Sample{Offset: int64(i), Power: 200, HasPwr: true, HR: 140})
		}
		samples = append(samples, store.Sample{Offset: 600, HasPwr: false})
		dec, ok := Decoupling(samples)
		if !ok || math.Abs(dec) > 1e-9 {
			t.Errorf("дропаут сломал расчёт: dec=%v ok=%v", dec, ok)
		}
	})
}

func TestPctZeroTotal(t *testing.T) {
	if got := pct(5, 0); got != 0 {
		t.Errorf("pct(5, 0) = %v, хочу 0", got)
	}
}
