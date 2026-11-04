package web

import (
	"fmt"
	"math"
	"strconv"

	"cycling-dashboard/internal/advice"
	"cycling-dashboard/internal/ridedata"
	"cycling-dashboard/internal/store"
)

// Презентационные хелперы деталки тренировки (TODO-UI §4.3): микро-выводы
// смысла у аббревиатур и сборка данных для сводки. Расчётов здесь нет —
// только перевод уже посчитанных чисел в подписи.

// ifVerdict — микро-вывод смысла Intensity Factor (NP / FTP).
func ifVerdict(v float64) string {
	switch {
	case v >= 1.0:
		return "выше FTP — гоночная интенсивность"
	case v >= 0.9:
		return "почти гоночная интенсивность"
	case v >= 0.8:
		return "твёрдая аэробная работа"
	case v >= 0.7:
		return "умеренная нагрузка"
	default:
		return "лёгкая езда"
	}
}

// viVerdict — микро-вывод смысла Variability Index (NP / средняя).
func viVerdict(v float64) string {
	switch {
	case v > advice.HighVI:
		return "очень рвано, много разгонов"
	case v > 1.10:
		return "рвано, много разгонов"
	case v > 1.05:
		return "умеренно рваная езда"
	default:
		return "ровная работа"
	}
}

// tssVerdict — микро-вывод смысла TSS; граница «тяжёлой» — та же, что
// в правилах советов (advice.HeavyTSS).
func tssVerdict(v float64) string {
	switch {
	case v > 150:
		return "очень тяжёлая тренировка"
	case v > advice.HeavyTSS:
		return "тяжёлая тренировка"
	case v >= 50:
		return "средняя нагрузка"
	default:
		return "лёгкая нагрузка"
	}
}

// pauseVerdict — чем объясняются паузы: на улице это остановки в городе,
// на станке — перерывы между интервалами.
func pauseVerdict(trainer bool) string {
	if trainer {
		return "остановки между интервалами"
	}
	return "светофоры и перекрёстки"
}

// insightLevel — уровень карточки advice в модификатор .insight (§3).
func insightLevel(level string) string {
	switch level {
	case advice.LevelWarn:
		return "warn"
	case advice.LevelGood:
		return "ok"
	default:
		return "info"
	}
}

// recordFacts — рекордные отрезки тренировки для движка сводки §6. Берёт
// готовые PR-флаги из карточек MMP: новых расчётов не появляется.
func recordFacts(cards []mmpCard) []advice.RecordFact {
	var out []advice.RecordFact
	for _, c := range cards {
		if c.PRPeriod == "" {
			continue
		}
		out = append(out, advice.RecordFact{
			Window: c.Label,
			Period: c.PRPeriod,
			Watts:  int(math.Round(c.Watts)),
		})
	}
	return out
}

// attachRecordTrends проставляет карточкам тренд: разницу между значением
// ЭТОЙ тренировки (cards[i].Watts) и лучшим значением того же окна за
// предыдущий период до даты тренировки (запрос тот же, что у
// /api/records?period=30d, но относительно её даты, а не now). Карточки
// без данных (Watts <= 0) и окна без прошлого периода тренда не получают.
// Тренд — подпись к готовым числам, а не новый расчёт; GPS в рекордах
// мощности не участвует.
func attachRecordTrends(cards []mmpCard, prev []store.Record) {
	prevByWindow := make(map[int]float64, len(prev))
	for _, r := range prev {
		prevByWindow[r.WindowSec] = r.Watts
	}
	for i := range cards {
		if cards[i].Watts <= 0 {
			continue
		}
		p, ok := prevByWindow[cards[i].Secs]
		if !ok {
			continue
		}
		if d := int(math.Round(cards[i].Watts - p)); d != 0 {
			cards[i].Trend = d
		}
	}
}

// zoneEdgesW — границы зон в ваттах для фоновых полос графика мощности.
// Те же доли FTP, что в подписях зон (ridedata.ZoneBounds).
func zoneEdgesW(ftp int) []float64 {
	if ftp <= 0 {
		return nil
	}
	bounds := ridedata.ZoneBounds()
	out := make([]float64, len(bounds))
	for i, f := range bounds {
		// 0.56*200 в float64 — 112.00000000000001; для полос на графике
		// хватает десятых
		out[i] = math.Round(f*float64(ftp)*10) / 10
	}
	return out
}

// metricSegment — сегмент полоски в строке сводки: единый вид для зон
// мощности и распределений пульса/каденса/скорости. Детализация при
// наведении — в Title.
type metricSegment struct {
	Class  string  // цвет сегмента: .zN у зон, .dist-* у распределений
	Pct    string
	PctNum float64 // тот же процент числом — для ширины бара
	Title  string
}

// workoutMetricRow — строка сводки «значения слева, полоска справа» (§4.3):
// среднее и максимум метрики плюс полоска распределения времени.
type workoutMetricRow struct {
	Label     string // «Мощность», «Пульс», «Каденс», «Скорость»
	AvgName   string // «NP», «Средний», «Средняя»
	AvgVal    string
	AvgNote   string // мелкая подпись у среднего («66 с накатом»)
	MaxName   string
	MaxVal    string
	StripAria string          // aria-label полоски
	Strip     []metricSegment // сегменты; без полоски строка остаётся со значениями
	Footer    string          // мелкая подпись под значениями (FTP у мощности)
}

// workoutMetricRows собирает строки сводки: мощность — от зон Коггана,
// пульс/каденс/скорость — от распределений времени. Значения переезжают из
// деталей, полоски — из секций «Зоны мощности» и «Распределение времени»;
// строка без значений и полоски не показывается, скорость — только улица.
func workoutMetricRows(wo *store.Workout, zones []workoutZoneRow, strips []metricStrip) []workoutMetricRow {
	distStrip := func(label string) []metricSegment {
		for _, s := range strips {
			if s.Label == label {
				return stripSegments(s)
			}
		}
		return nil
	}

	var rows []workoutMetricRow

	// мощность: NP или средняя (если NP не считался) + максимум; полоска —
	// зоны от FTP, подпись — сам FTP
	if wo.NormPower > 0 || wo.AvgPower > 0 {
		row := workoutMetricRow{
			Label:     "Мощность",
			MaxName:   "Макс",
			StripAria: "распределение времени по зонам мощности",
			Strip:     zoneSegments(zones),
		}
		if wo.NormPower > 0 {
			row.AvgName, row.AvgVal = "NP", fmt.Sprintf("%d Вт", wo.NormPower)
			row.AvgNote = "средняя с весом пиков"
		} else {
			row.AvgName, row.AvgVal = "Средняя", fmt.Sprintf("%d Вт", wo.AvgPower)
		}
		if wo.MaxPower > 0 {
			row.MaxVal = fmt.Sprintf("%d Вт", wo.MaxPower)
		}
		if wo.FTP > 0 {
			row.Footer = fmt.Sprintf("FTP %d Вт — %s; %% — от времени педалирования", wo.FTP, ftpKindLabel(wo))
		}
		rows = append(rows, row)
	}

	// пульс: значения остаются и без полоски, если сэмплов не нашлось
	if hr := distStrip("Пульс"); wo.AvgHR > 0 || wo.MaxHR > 0 || len(hr) > 0 {
		row := workoutMetricRow{
			Label: "Пульс", AvgName: "Средний", MaxName: "Макс",
			StripAria: "распределение времени: Пульс", Strip: hr,
		}
		if wo.AvgHR > 0 {
			row.AvgVal = strconv.Itoa(wo.AvgHR)
		}
		if wo.MaxHR > 0 {
			row.MaxVal = strconv.Itoa(wo.MaxHR)
		}
		rows = append(rows, row)
	}

	// каденс: основной средний — «в работе» (без наката), общий — мелкой
	// подписью; если «в работе» не считался, общий и есть основной
	if cad := distStrip("Каденс"); wo.AvgCadence > 0 || wo.AvgCadenceActive > 0 || wo.MaxCadence > 0 || len(cad) > 0 {
		row := workoutMetricRow{
			Label: "Каденс", AvgName: "Средний", MaxName: "Макс",
			StripAria: "распределение времени: Каденс", Strip: cad,
		}
		if wo.AvgCadenceActive > 0 {
			row.AvgVal = strconv.Itoa(wo.AvgCadenceActive)
			if wo.AvgCadence > 0 {
				row.AvgNote = fmt.Sprintf("%d с накатом", wo.AvgCadence)
			}
		} else {
			row.AvgVal = strconv.Itoa(wo.AvgCadence)
		}
		if wo.MaxCadence > 0 {
			row.MaxVal = strconv.Itoa(wo.MaxCadence)
		}
		rows = append(rows, row)
	}

	// скорость: только улица — у станка её нет ни в метриках, ни в полосках
	if !wo.Trainer {
		if spd := distStrip("Скорость"); wo.AvgSpeedKmh > 0 || wo.MaxSpeedKmh > 0 || len(spd) > 0 {
			row := workoutMetricRow{
				Label: "Скорость", AvgName: "Средняя", MaxName: "Макс",
				StripAria: "распределение времени: Скорость", Strip: spd,
			}
			if wo.AvgSpeedKmh > 0 {
				row.AvgVal = trimFloat(wo.AvgSpeedKmh) + " км/ч"
			}
			if wo.MaxSpeedKmh > 0 {
				row.MaxVal = trimFloat(wo.MaxSpeedKmh) + " км/ч"
			}
			rows = append(rows, row)
		}
	}

	return rows
}

// zoneSegments переводит строки зон Коггана в сегменты полоски.
func zoneSegments(zones []workoutZoneRow) []metricSegment {
	out := make([]metricSegment, 0, len(zones))
	for _, z := range zones {
		out = append(out, metricSegment{
			Class:  fmt.Sprintf("z%d", z.N),
			Pct:    z.Pct,
			PctNum: z.PctNum,
			Title:  fmt.Sprintf("%s — %s (%s%%)", z.Label, z.Time, z.Pct),
		})
	}
	return out
}

// stripSegments переводит распределение времени в сегменты полоски.
func stripSegments(s metricStrip) []metricSegment {
	out := make([]metricSegment, 0, len(s.Entries))
	for _, e := range s.Entries {
		out = append(out, metricSegment{
			Class:  s.Class,
			Pct:    e.Pct,
			PctNum: e.PctNum,
			Title:  fmt.Sprintf("%s %s — %s (%s%%)", e.Label, s.Unit, e.Time, e.Pct),
		})
	}
	return out
}

// ftpKindLabel — источник FTP для подписи у полоски зон: тот же текст, что
// был у подписи секции «Зоны мощности».
func ftpKindLabel(wo *store.Workout) string {
	switch {
	case wo.FTPFallback():
		return "станок, уличный не задан"
	case wo.FTPKind == "outdoor":
		return "улица"
	default:
		return "станок"
	}
}
