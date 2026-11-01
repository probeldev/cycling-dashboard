// Package advice — человеческие советы по данным тренировки. Пакет чистый:
// не ходит в БД, не смотрит на текущее время — тренировка и весь контекст
// (вчерашний TSS, TSB, полярность недели, decoupling) приходят снаружи.
package advice

import (
	"math"
	"sort"

	"cycling-dashboard/internal/ridedata"
	"cycling-dashboard/internal/store"
)

// Пороги правил. Границы строгие там, где так написано в правиле:
// «> 20%» не срабатывает на ровно 20%, «< 70» — на 70.
const (
	LowCadenceRPM     = 70   // ниже — низкий каденс на улице
	HighVI            = 1.15 // выше — рваная езда, TSS завышен
	CoastingPct       = 20   // больше — много наката
	DataGapsPct       = 5    // больше — поверметр терял связь
	HighZonePct       = 40   // больше — много времени в Z4+
	HeavyTSS          = 100  // выше — тяжёлая тренировка
	PreviousHeavyTSS  = 80   // выше — вчера тоже была тяжёлая
	LowTSB            = -20  // ниже — накопленная усталость
	LowRPE            = 4    // не выше — субъективно легко
	GrayZonePct       = 20   // больше — много серой зоны за неделю
	GoodDecouplingPct = 5    // ниже — мощность не просела относительно пульса
	GoodVI            = 1.08 // ниже — ровная работа
	MaxCards          = 5    // больше карточек на деталке не показываем

	// decouplingMinHalfSecs — минимум времени в каждой половине: короче
	// 5 минут сравнение EF слишком шумное.
	decouplingMinHalfSecs = 300
)

// Уровни карточек: warn — на что обратить внимание, info — пояснение,
// good — сработавшая хорошая практика.
const (
	LevelInfo = "info"
	LevelWarn = "warn"
	LevelGood = "good"
)

// Card — одна рекомендация для блока «Что говорят данные».
type Card struct {
	Level string `json:"level"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

// Context — всё, что не выводится из самой тренировки: зоны текущей
// тренировки, вчерашний TSS, TSB на день тренировки, доля серой зоны за
// неделю и decoupling (nil — пульса или мощности нет).
type Context struct {
	Zones        [7]int
	YesterdayTSS float64
	TSB          float64
	WeekGrayPct  float64
	Decoupling   *float64
}

// For применяет все правила и возвращает не больше MaxCards карточек,
// отсортированных по важности: warn → info → good. Внутри уровня порядок
// правил сохраняется; лишние карточки отбрасываются с конца.
func For(w store.Workout, ctx Context) []Card {
	cards := make([]Card, 0, 12)
	add := func(level, title, text string) {
		cards = append(cards, Card{Level: level, Title: title, Text: text})
	}

	if !w.Trainer && w.AvgCadence > 0 && w.AvgCadence < LowCadenceRPM {
		add(LevelInfo, "Низкий каденс", "попробуй передачу полегче, 80–90 об/мин разгружает ноги")
	}
	if w.HasVI && w.VI > HighVI {
		add(LevelWarn, "Рваная езда", "TSS завышен, для ровной Z2 нужен участок без светофоров")
	}
	if w.Duration > 0 && pct(float64(w.CoastingSec), w.Duration.Seconds()) > CoastingPct {
		add(LevelInfo, "Много наката", "на шоссе это норма, но сравнивай среднюю «в работе», а не общую")
	}
	if w.HasPower && w.Duration > 0 && pct(float64(w.GapsSec()), w.Duration.Seconds()) > DataGapsPct {
		add(LevelWarn, "Пропуски данных", "поверметр терял связь: проверь батарейку/крепление, цифры могут быть занижены")
	}
	if w.Duration > 0 && w.AvgHR == 0 && w.MaxHR == 0 {
		add(LevelInfo, "Пульса нет", "нагрудный датчик в C2 дал бы пульсовые зоны и decoupling")
	}
	if total, high := zoneTotals(ctx.Zones); total > 0 && pct(float64(high), float64(total)) > HighZonePct {
		add(LevelWarn, "Много пороговой работы", "не больше 2 таких тренировок в неделю, иначе форма просядет")
	}
	if w.HasTSS && w.TSS > HeavyTSS && ctx.YesterdayTSS > PreviousHeavyTSS {
		add(LevelWarn, "Два тяжёлых дня подряд", "следующий день — лёгкий или отдых")
	}
	if ctx.TSB < LowTSB {
		add(LevelWarn, "Накопленная усталость", "1–2 дня Z1 или отдых")
	}
	if w.RPE > 0 && w.RPE <= LowRPE && w.HasTSS && w.TSS > HeavyTSS {
		add(LevelWarn, "Субъективно легко", "нагрузка высокая — возможно, FTP занижен")
	}
	if w.RPE == 0 {
		add(LevelInfo, "Нет RPE", "поставь RPE: единственная метрика, которая не врёт на рваной городской езде")
	}
	if ctx.WeekGrayPct > GrayZonePct {
		add(LevelWarn, "Много серой зоны", "добавь лёгкого станка или убери темповые")
	}
	if ctx.Decoupling != nil && *ctx.Decoupling < GoodDecouplingPct && w.HasVI && w.VI < GoodVI {
		add(LevelGood, "Ровная работа", "хорошая выносливость")
	}

	sort.SliceStable(cards, func(i, j int) bool { return levelRank(cards[i].Level) < levelRank(cards[j].Level) })
	if len(cards) > MaxCards {
		cards = cards[:MaxCards]
	}
	return cards
}

// zoneTotals — всё время в зонах и время в Z4–Z7.
func zoneTotals(zones [7]int) (total, high int) {
	for _, secs := range zones {
		total += secs
	}
	for _, secs := range zones[3:] {
		high += secs
	}
	return total, high
}

func levelRank(level string) int {
	switch level {
	case LevelWarn:
		return 0
	case LevelInfo:
		return 1
	default:
		return 2
	}
}

func pct(part, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return part * 100 / total
}

// Decoupling — аэробный дрейф, %: насколько упал efficiency factor
// (мощность / пульс) во второй половине относительно первой. Считается по
// времени (ridedata.Timings), берутся только записи с мощностью и пульсом.
// Положительное значение — пульс рос при той же мощности. false — данных
// меньше чем по 5 минут на половину или пульса/мощности нет.
func Decoupling(samples []store.Sample) (float64, bool) {
	if len(samples) == 0 {
		return 0, false
	}
	times := ridedata.Timings(samples)
	var total float64
	for i, s := range samples {
		if s.HasPwr && s.HR > 0 {
			total += times[i].Secs
		}
	}
	if total < 2*decouplingMinHalfSecs {
		return 0, false
	}

	half := total / 2
	var secs, power, hr [2]float64
	var elapsed float64
	add := func(h int, dt float64, s store.Sample) {
		secs[h] += dt
		power[h] += float64(s.Power) * dt
		hr[h] += float64(s.HR) * dt
	}
	for i, s := range samples {
		if !s.HasPwr || s.HR <= 0 {
			continue
		}
		dt := times[i].Secs
		if elapsed < half {
			first := math.Min(dt, half-elapsed)
			add(0, first, s)
			if dt > first {
				add(1, dt-first, s)
			}
		} else {
			add(1, dt, s)
		}
		elapsed += dt
	}
	// средние сокращаются: EF = (Σpower·dt / Σdt) / (Σhr·dt / Σdt)
	ef1 := power[0] / hr[0]
	ef2 := power[1] / hr[1]
	if ef1 <= 0 || ef2 <= 0 {
		return 0, false
	}
	return (ef1 - ef2) / ef1 * 100, true
}
