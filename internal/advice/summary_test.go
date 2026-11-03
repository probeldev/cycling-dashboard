package advice

import (
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/thresholds"
)

// zonesPct собирает массив зон из процентов (условно — 100 секунд всего).
func zonesPct(pcts ...int) [7]int {
	var z [7]int
	copy(z[:], pcts)
	return z
}

func joined(phrases []string) string { return strings.Join(phrases, " ") }

func TestSummaryAssessThresholdWork(t *testing.T) {
	w, ctx := baseWorkout()
	w.IF, w.HasIF = 0.97, true
	ctx.Zones = zonesPct(10, 20, 30, 25, 10, 3, 2) // 40% в Z4+
	out := Summary(w, ctx)
	if !strings.Contains(joined(out), "Пороговая работа") {
		t.Errorf("нет оценки пороговой работы: %v", out)
	}
}

func TestSummaryAssessThresholdNeedsHighIF(t *testing.T) {
	w, ctx := baseWorkout()
	w.IF, w.HasIF = 0.90, true // много Z4+, но IF недотягивает
	ctx.Zones = zonesPct(10, 20, 30, 25, 10, 3, 2)
	if strings.Contains(joined(Summary(w, ctx)), "Пороговая работа") {
		t.Errorf("пороговая без IF>0.95: %v", Summary(w, ctx))
	}
}

func TestSummaryAssessRagged(t *testing.T) {
	w, ctx := baseWorkout()
	w.VI = 1.2
	out := Summary(w, ctx)
	if !strings.Contains(joined(out), "Рваная езда") || !strings.Contains(joined(out), "1.20") {
		t.Errorf("нет оценки рваной езды: %v", out)
	}
}

func TestSummaryAssessSteadyZ2(t *testing.T) {
	w, ctx := baseWorkout()
	w.IF, w.HasIF = 0.72, true
	ctx.Zones = zonesPct(20, 65, 10, 5, 0, 0, 0)
	out := Summary(w, ctx)
	if !strings.Contains(joined(out), "Ровная аэробная работа") {
		t.Errorf("нет оценки ровной Z2: %v", out)
	}
}

func TestSummaryAssessRecovery(t *testing.T) {
	w, ctx := baseWorkout()
	w.IF, w.HasIF = 0.55, true
	out := Summary(w, ctx)
	if !strings.Contains(joined(out), "восстановительн") {
		t.Errorf("нет оценки восстановительной: %v", out)
	}
}

func TestSummaryAssessFallbackWithoutPower(t *testing.T) {
	w, ctx := baseWorkout()
	w.HasPower, w.AvgPower, w.HasVI = false, 0, false
	out := Summary(w, ctx)
	if len(out) == 0 || !strings.Contains(out[0], "Поездка 60 мин") {
		t.Errorf("нет нейтральной оценки: %v", out)
	}
}

func TestSummaryRecordPhrase(t *testing.T) {
	w, ctx := baseWorkout()
	ctx.Records = []RecordFact{{Window: "5 мин", Period: "за месяц", Watts: 312}}
	out := Summary(w, ctx)
	if !strings.Contains(joined(out), "Рекордные отрезки: 5 мин — 312 Вт (за месяц).") {
		t.Errorf("нет фразы рекорда: %v", out)
	}
}

func TestSummaryRecordPhraseGrouping(t *testing.T) {
	recs := []RecordFact{
		{Window: "5 с", Period: "за месяц"},
		{Window: "1 мин", Period: "за месяц"},
		{Window: "20 мин", Period: "за год"},
	}
	got := recordPhrase(recs)
	want := "Рекордные отрезки: 5 с и 1 мин (за месяц); 20 мин (за год)."
	if got != want {
		t.Errorf("recordPhrase = %q, хочу %q", got, want)
	}
}

func TestSummaryTrendHeavyDays(t *testing.T) {
	w, ctx := baseWorkout()
	w.TSS = 120
	ctx.YesterdayTSS = 90
	out := joined(Summary(w, ctx))
	if !strings.Contains(out, "Второй тяжёлый день подряд") {
		t.Errorf("нет тренда двух тяжёлых дней: %v", out)
	}
	if !strings.Contains(out, "Завтра — лёгкий день") {
		t.Errorf("нет рекомендации лёгкого дня: %v", out)
	}
}

func TestSummaryTrendTSBLoaded(t *testing.T) {
	w, ctx := baseWorkout()
	ctx.TSB = thresholds.TSBLoadedMin - 1
	out := joined(Summary(w, ctx))
	if !strings.Contains(out, "накопленная усталость") {
		t.Errorf("нет тренда усталости: %v", out)
	}
	if !strings.Contains(out, "Z1–Z2") {
		t.Errorf("нет рекомендации при перегрузке: %v", out)
	}
}

func TestSummaryTrendDecoupling(t *testing.T) {
	w, ctx := baseWorkout()
	d := 2.5
	ctx.Decoupling = &d
	out := joined(Summary(w, ctx))
	if !strings.Contains(out, "стабильны всю поездку") {
		t.Errorf("нет тренда стабильности: %v", out)
	}
}

func TestSummaryRecommendNoRPE(t *testing.T) {
	w, ctx := baseWorkout()
	w.RPE = 0
	out := joined(Summary(w, ctx))
	if !strings.Contains(out, "Отметь RPE") {
		t.Errorf("нет рекомендации про RPE: %v", out)
	}
}

func TestSummaryPriorityAndCap(t *testing.T) {
	// все правила сразу: оценка, рекорд, тренд, рекомендация — 4 кандидата,
	// в вывод попадают первые три по приоритету §6
	w, ctx := baseWorkout()
	w.IF, w.HasIF = 0.97, true
	w.TSS = 120
	ctx.Zones = zonesPct(10, 20, 30, 25, 10, 3, 2)
	ctx.Records = []RecordFact{{Window: "5 мин", Period: "за месяц", Watts: 312}}
	ctx.YesterdayTSS = 90
	out := Summary(w, ctx)
	if len(out) != 3 {
		t.Fatalf("фраз %d, хочу 3: %v", len(out), out)
	}
	if !strings.Contains(out[0], "Пороговая работа") ||
		!strings.Contains(out[1], "Рекордные отрезки") ||
		!strings.Contains(out[2], "Второй тяжёлый день") {
		t.Errorf("порядок фраз не оценка→рекорд→тренд: %v", out)
	}
}

func TestSummaryToneNoExclamations(t *testing.T) {
	// тон — сдержанный тренер: без восклицаний ни в одном сценарии
	w, ctx := baseWorkout()
	w.IF, w.HasIF = 0.97, true
	w.VI = 1.2
	ctx.Zones = zonesPct(10, 20, 30, 25, 10, 3, 2)
	ctx.Records = []RecordFact{{Window: "5 с", Period: "за месяц", Watts: 640}}
	d := 2.0
	ctx.Decoupling = &d
	for _, p := range Summary(w, ctx) {
		if strings.ContainsAny(p, "!🔥") {
			t.Errorf("фраза нарушает тон: %q", p)
		}
		if !strings.HasSuffix(p, ".") {
			t.Errorf("фраза не заканчивается точкой: %q", p)
		}
	}
}

func TestSummaryDurationFallbackMinutes(t *testing.T) {
	w, ctx := baseWorkout()
	w.Duration = 95 * time.Minute
	w.HasPower, w.AvgPower, w.HasVI = false, 0, false
	out := Summary(w, ctx)
	if !strings.Contains(out[0], "95 мин") {
		t.Errorf("в fallback нет длительности: %v", out)
	}
}
