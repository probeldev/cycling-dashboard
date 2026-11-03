package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"cycling-dashboard/internal/advice"
	"cycling-dashboard/internal/plan"
	"cycling-dashboard/internal/store"
	"cycling-dashboard/internal/thresholds"
)

// statusResponse — композитный ответ главной: состояние, рекомендация,
// неделя, последняя тренировка, готовность и предупреждения.
type statusResponse struct {
	AsOf           string               `json:"asOf"`
	AsOfText       string               `json:"-"`
	PMCAsOf        string               `json:"pmcAsOf"`
	PMCAsOfText    string               `json:"-"`
	TSBAsOf        string               `json:"tsbAsOf"`
	TSBNote        string               `json:"tsbNote"`
	State          statusState          `json:"state"`
	Summary        string               `json:"summary"`
	Recommendation plan.Recommendation  `json:"recommendation"`
	Week           statusWeek           `json:"week"`
	LastWorkout    any                  `json:"lastWorkout"`
	Readiness      store.ReadinessStats `json:"readiness"`
	Alerts         []statusAlert        `json:"alerts"`
	AlertsAll      []statusAlert        `json:"alertsAll,omitempty"`
	Rules          []plan.RuleCheck     `json:"rules,omitempty"`

	AllAlerts []statusAlert    `json:"-"` // для страницы «Сегодня»: все, без обрезки
	Last      *lastWorkoutView `json:"-"` // типизированное представление для шаблона

	Title  string `json:"-"` // «Сегодня, 1 ноября» — шапка дашборда
	Banner string `json:"-"` // одна строка вывода без повторов чисел из карточек
	Cov7   int    `json:"-"` // дней с нагрузкой за 7 дней — пометка «(N дней из 7)»
	Cov28  int    `json:"-"` // дней с нагрузкой за 28 дней — «(N дней из 28)»
	Cov90  int    `json:"-"` // дней с нагрузкой за 90 дней — «(N дней из 90)»

	// «Почему так»: только причины, которых нет в карточках PMC. Числовые
	// TSB/CTL/ATL уже показаны выше и в раскрывашке не повторяются.
	Factors []plan.Factor `json:"-"`

	// «Внимание» на дашборде без низких данных: о нехватке истории уже
	// говорят баннер и сноска PMC, повторять те же цифры не нужно.
	AlertsView    []statusAlert `json:"-"`
	AlertsAllView []statusAlert `json:"-"`
}

// statusState — общая оценка дня и разбор трёх метрик PMC по отдельности.
// ctl/atl/tsb — объекты, потому что у каждой карточки свои verdict/action/trend.
type statusState struct {
	Label      string       `json:"label"`
	LabelText  string       `json:"labelText"`
	Verdict    string       `json:"verdict"`
	Action     string       `json:"action"`
	Confidence string       `json:"confidence"`
	LowData    bool         `json:"lowData"`
	DataDays   int          `json:"-"` // дней истории с первой нагрузки — сноска «PMC по N дням»
	CTL        statusMetric `json:"ctl"`
	ATL        statusMetric `json:"atl"`
	TSB        statusMetric `json:"tsb"`
}

type statusMetric struct {
	Value   float64 `json:"value"`
	Text    string  `json:"-"` // число для экрана: один знак, типографский минус
	Verdict string  `json:"verdict"`
	Action  string  `json:"action"`
	Trend   float64 `json:"trend"`
	Spark   string  `json:"-"` // готовая строка points для инлайн-SVG
}

// lastWorkoutView — последняя тренировка для карточки на главной. Отдельно от
// JSON-карты: шаблону нужны типы, а API сохраняет прежнюю форму workout.
type lastWorkoutView struct {
	ID        int64
	Date      string
	Name      string
	Kind      string
	DurationH float64
	KM        float64
	AvgPower  int
	NormPower int
	TSS       *float64
	IF        *float64
	VI        *float64
	RPE       int
	Bike      string
	HasPower  bool
	Advice    []advice.Card
}

type statusWeek struct {
	Start               string             `json:"start"`
	DayOfWeek           int                `json:"dayOfWeek"`
	TSS                 float64            `json:"tss"`
	Target              *float64           `json:"target"`
	TargetSource        store.TargetSource `json:"targetSource"`
	TargetToDate        *float64           `json:"targetToDate"`
	Remaining           *float64           `json:"remaining"`
	RemainingMinZ2Total *int               `json:"remainingMinZ2Total"`
	Hours               float64            `json:"hours"`
	KM                  float64            `json:"km"`
	Rides               int                `json:"rides"`
	TrainerHours        float64            `json:"trainerHours"`
	OutdoorHours        float64            `json:"outdoorHours"`
	PrevWeekTSS         float64            `json:"prevWeekTSS"`
	DeltaPct            *float64           `json:"deltaPct"`
	Zones               statusZones        `json:"zones"`
	PolarityVerdict     string             `json:"polarityVerdict"`
	CoverageDays        int                `json:"-"` // дней с нагрузкой в текущей неделе

	rawTSS float64 // неокруглённый TSS для правил, не для JSON
}

type statusZones struct {
	LowPct  float64 `json:"lowPct"`
	GrayPct float64 `json:"grayPct"`
	HighPct float64 `json:"highPct"`
}

type statusAlert struct {
	Level string `json:"level"`
	Code  string `json:"code"`
	Text  string `json:"text"`
}

// historyFacts — то, что нельзя вывести из одного дня: начало истории,
// дни до тяжёлой/тестовой тренировки и доля станка за 4 недели.
type historyFacts struct {
	hasHistory    bool
	firstDay      time.Time
	lastDay       time.Time
	dataDays      int // дней с первой нагрузки в окне 42 дней
	loadDays42    int // различных дней с TSS > 0 за 42 дня
	daysSinceHard int // -1 — тяжёлой тренировки не было
	daysSinceTest int // -1 — тестовой тренировки не было
	trainerShare  float64
	cov7          int // дней с TSS > 0 у рабочих тренировок за 7 дней
	cov28         int // то же за 28 дней
	cov90         int // то же за 90 дней
}

func handleStatus(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		debug := req.URL.Query().Get("debug") == "1"
		out, err := buildStatus(db, time.Now(), debug)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
	}
}

// buildStatus собирает ответ. now передаётся снаружи, чтобы тесты могли
// зафиксировать день и не зависеть от текущего времени.
func buildStatus(db *sql.DB, now time.Time, debug bool) (*statusResponse, error) {
	today := store.StartOfDay(now)
	facts, err := loadHistoryFacts(db, now)
	if err != nil {
		return nil, err
	}
	days, err := store.TimelineUntil(db, today.AddDate(0, 0, -28), today, now)
	if err != nil {
		return nil, err
	}
	if len(days) == 0 {
		return nil, fmt.Errorf("пустой таймлайн")
	}

	week, gray4, err := statusWeekFor(db, now)
	if err != nil {
		return nil, err
	}
	week.CoverageDays = coverageInWindow(days, store.WeekStart(now), today)
	target := 0.0
	if week.Target != nil {
		target = *week.Target
	}
	weekTSS := week.rawTSS

	trainerFTP, _ := store.ProfileAt(db, "ftp", now)
	outdoorFTP, _ := store.ProfileAt(db, "ftp_outdoor", now)

	pmcDay := days[len(days)-1]
	if len(days) >= 2 {
		pmcDay = days[len(days)-2]
	}
	// План видит те же округлённые значения, что карточки: иначе на границе
	// −10.04 рекомендация и вердикт TSB могли бы противоречить друг другу.
	pmcCTL, pmcATL := round1(pmcDay.CTL), round1(pmcDay.ATL)
	in := plan.Input{
		DataDays:      facts.dataDays,
		LoadDays42:    facts.loadDays42,
		TSB:           round1(pmcCTL - pmcATL),
		CTL:           pmcCTL,
		ATL:           pmcATL,
		DaysSinceHard: facts.daysSinceHard,
		DaysSinceTest: facts.daysSinceTest,
		WeekTSS:       weekTSS,
		TargetTSS:     target,
		GrayPct:       gray4,
		FTPTrainer:    trainerFTP,
		FTPOutdoor:    outdoorFTP,
		TrainerShare:  facts.trainerShare,
	}
	var rec plan.Recommendation
	var checks []plan.RuleCheck
	if debug {
		rec, checks = plan.Explain(in)
	} else {
		rec = plan.Recommend(in)
	}

	state := buildState(pmcDay, facts, days)

	latest, err := store.LatestWorkoutBefore(db, now)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	var lastWorkout any
	var lastView *lastWorkoutView
	if latest != nil {
		cards, err := workoutAdvice(db, latest)
		if err != nil {
			return nil, err
		}
		if len(cards) > 2 {
			cards = cards[:2]
		}
		lastWorkout, err = workoutJSONWithAdvice(latest, cards)
		if err != nil {
			return nil, err
		}
		lastView = makeLastWorkoutView(latest, cards)
	}

	readiness, err := store.Readiness(db, now)
	if err != nil {
		return nil, err
	}

	alertsAll, err := statusAlerts(db, now, facts, latest)
	if err != nil {
		return nil, err
	}
	alerts := alertsAll
	if len(alerts) > 3 {
		alerts = alerts[:3]
	}
	var alertsDebug []statusAlert
	if debug {
		alertsDebug = alertsAll
	}

	yesterday := 0.0
	if len(days) >= 2 {
		yesterday = days[len(days)-2].TSS
	}
	summary := statusSummary(state, week, yesterday)
	alertsView := withoutLowData(alertsAll)
	alertsViewTop := alertsView
	if len(alertsViewTop) > 3 {
		alertsViewTop = alertsViewTop[:3]
	}
	out := &statusResponse{
		AsOf:           today.Format("2006-01-02"),
		AsOfText:       today.Format("02.01.2006"),
		PMCAsOf:        pmcDay.Date.Format("2006-01-02"),
		PMCAsOfText:    pmcDay.Date.Format("02.01.2006"),
		TSBAsOf:        pmcDay.Date.Format("2006-01-02"),
		TSBNote:        "TSB — до нагрузки сегодняшнего дня",
		State:          state,
		Summary:        summary,
		Recommendation: rec,
		Week:           week,
		LastWorkout:    lastWorkout,
		Readiness:      readiness,
		Alerts:         alerts,
		AlertsAll:      alertsDebug,
		Rules:          checks,
		AllAlerts:      alertsAll,
		Last:           lastView,
		Title:          todayTitle(today),
		Banner:         bannerText(state.Label),
		Cov7:           facts.cov7,
		Cov28:          facts.cov28,
		Cov90:          facts.cov90,
		Factors:        presentationFactors(rec.Factors),
		AlertsView:     alertsViewTop,
		AlertsAllView:  alertsView,
	}
	return out, nil
}

// withoutLowData убирает из «Внимания» предупреждение о нехватке истории:
// на дашборде её уже объясняют баннер и сноска PMC.
func withoutLowData(in []statusAlert) []statusAlert {
	out := make([]statusAlert, 0, len(in))
	for _, a := range in {
		if a.Code == "lowdata" {
			continue
		}
		out = append(out, a)
	}
	return out
}

// bannerText — одна строка-вывод под шапкой. Вердикт уже показан чипом, а
// цифры недели и PMC — в карточках, поэтому баннер говорит только о смысле
// состояния и не повторяет числа (TODO-UI §4.1).
func bannerText(label string) string {
	switch label {
	case "lowdata":
		return "Истории пока мало — рекомендации станут точнее по мере накопления тренировок"
	case "fresh":
		return "Свежесть выше обычной — подходящий день для качественной работы"
	case "loaded":
		return "Нагрузка накопилась — сегодня лучше без интервалов"
	case "overloaded":
		return "Накопленная усталость высокая — сегодня только отдых или лёгкая работа"
	default:
		return "Форма в рабочем диапазоне — можно тренироваться по плану"
	}
}

// coverageInWindow — различных дней с TSS > 0 в окне [from, to] по таймлайну.
func coverageInWindow(days []store.Day, from, to time.Time) int {
	from = store.StartOfDay(from)
	to = store.StartOfDay(to)
	n := 0
	for _, d := range days {
		if d.TSS > 0 && !d.Date.Before(from) && !d.Date.After(to) {
			n++
		}
	}
	return n
}

// coverageCount — то же для набора локальных дней из истории тренировок.
func coverageCount(days map[time.Time]bool, from, to time.Time) int {
	from = store.StartOfDay(from)
	to = store.StartOfDay(to)
	n := 0
	for d := range days {
		if !d.Before(from) && !d.After(to) {
			n++
		}
	}
	return n
}

// presentationFactors убирает числовые PMC-факторы: TSB/CTL/ATL уже показаны
// в карточках, и в раскрывашке «Почему так» они были бы повтором.
func presentationFactors(in []plan.Factor) []plan.Factor {
	out := make([]plan.Factor, 0, len(in))
	for _, f := range in {
		switch f.Label {
		case "TSB", "CTL", "ATL":
			continue
		default:
			out = append(out, f)
		}
	}
	return out
}

func statusWeekFor(db *sql.DB, now time.Time) (statusWeek, float64, error) {
	weeks, err := store.Weeks(db, 4, now)
	if err != nil {
		return statusWeek{}, 0, err
	}
	cur := store.Week{Start: store.WeekStart(now)}
	if len(weeks) > 0 {
		cur = weeks[len(weeks)-1]
	}
	var prev store.Week
	if len(weeks) > 1 {
		prev = weeks[len(weeks)-2]
	}

	targetInfo, err := store.WeeklyTarget(db, now)
	if err != nil {
		return statusWeek{}, 0, err
	}
	dow := dayOfWeek(now)
	tss := cur.TSS
	out := statusWeek{
		Start:        cur.Start.Format("2006-01-02"),
		DayOfWeek:    dow,
		TSS:          round1(tss),
		TargetSource: targetInfo.Source,
		Hours:        round2(cur.Hours),
		KM:           round1(cur.DistanceKm),
		Rides:        cur.Rides,
		TrainerHours: round2(cur.TrainerHours),
		OutdoorHours: round2(cur.OutdoorHours),
		PrevWeekTSS:  round1(prev.TSS),
		Zones: statusZones{
			LowPct:  cur.LowPct,
			GrayPct: cur.GrayPct,
			HighPct: cur.HighPct,
		},
		PolarityVerdict: polarityVerdict(cur.LowPct, cur.GrayPct, cur.HighPct),
		rawTSS:          tss,
	}
	if targetInfo.Source != store.TargetSourceNone {
		target := round1(targetInfo.TSS)
		toDate := round1(target * float64(dow) / 7)
		remaining := math.Round(math.Max(0, target-tss))
		minutes := z2Minutes(remaining)
		out.Target = &target
		out.TargetToDate = &toDate
		out.Remaining = &remaining
		out.RemainingMinZ2Total = &minutes
	}
	if prev.TSS > 0 {
		v := round1((tss - prev.TSS) / prev.TSS * 100)
		out.DeltaPct = &v
	}
	return out, grayPct4(weeks), nil
}

// polarityVerdict — короткий вывод по распределению времени в зонах.
func polarityVerdict(low, gray, high float64) string {
	switch {
	case gray > 20:
		return "много серой зоны"
	case high > 20:
		return "много интенсивности"
	case low >= 80:
		return "полярность хорошая"
	case low > 0 || gray > 0 || high > 0:
		return "смешанная нагрузка"
	default:
		return "нет данных о зонах"
	}
}

// grayPct4 — доля Z3 за четыре недели: недельная серая зона шумная, правило
// 6 смотрит на месяц.
func grayPct4(weeks []store.Week) float64 {
	var total, gray int
	for _, w := range weeks {
		for _, secs := range w.Zones {
			total += secs
		}
		gray += w.Zones[2]
	}
	if total == 0 {
		return 0
	}
	return round1(float64(gray) * 100 / float64(total))
}

// z2Minutes пересчитывает остаток TSS в минуты ровной Z2: TSS = IF²·часы·100,
// IF — середина Z2 (60–75% FTP). Округляем до 5 минут: точнее планировать
// тренировку всё равно нельзя.
func z2Minutes(tss float64) int {
	if tss <= 0 {
		return 0
	}
	const z2IF = (0.60 + 0.75) / 2
	minutes := tss / (z2IF * z2IF * 100) * 60
	return min(600, int(math.Round(minutes/5)*5))
}

func loadHistoryFacts(db *sql.DB, now time.Time) (historyFacts, error) {
	today := store.StartOfDay(now)
	rows, err := db.Query(`SELECT start_time, COALESCE(tss, 0), COALESCE(if, 0), is_test, trainer
	    FROM workouts WHERE start_time <= ? ORDER BY start_time DESC`,
		now.UTC().Format(time.RFC3339))
	if err != nil {
		return historyFacts{}, err
	}
	defer rows.Close()

	type hardDay struct {
		tss   float64
		maxIF float64
	}
	nonTest := map[time.Time]*hardDay{}
	testDays := map[time.Time]bool{}
	coverageDays := map[time.Time]bool{}
	var first, last time.Time
	var firstLoad time.Time
	loadDays := map[time.Time]bool{}
	var trainer, total int
	shareFrom := store.WeekStart(now).AddDate(0, 0, -21)
	shareTo := store.WeekStart(now).AddDate(0, 0, 7)
	loadFrom := today.AddDate(0, 0, -(thresholds.DataWindowDays - 1))

	for rows.Next() {
		var start string
		var tss, iff float64
		var isTest, isTrainer bool
		if err := rows.Scan(&start, &tss, &iff, &isTest, &isTrainer); err != nil {
			return historyFacts{}, err
		}
		t, err := time.Parse(time.RFC3339, start)
		if err != nil {
			return historyFacts{}, err
		}
		lt := t.Local()
		day := store.StartOfDay(lt)
		if day.After(today) {
			continue
		}
		if first.IsZero() || day.Before(first) {
			first = day
		}
		if last.IsZero() || day.After(last) {
			last = day
		}
		if tss > 0 && !day.Before(loadFrom) && !day.After(today) {
			loadDays[day] = true
			if firstLoad.IsZero() || day.Before(firstLoad) {
				firstLoad = day
			}
		}
		if !day.Before(shareFrom) && day.Before(shareTo) {
			total++
			if isTrainer {
				trainer++
			}
		}
		if tss > 0 && !isTest {
			// Coverage считаем по тому же набору, что Readiness: без тестов.
			coverageDays[day] = true
		}
		if isTest {
			testDays[day] = true
			continue
		}
		d := nonTest[day]
		if d == nil {
			d = &hardDay{}
			nonTest[day] = d
		}
		d.tss += tss
		if iff > d.maxIF {
			d.maxIF = iff
		}
	}
	if err := rows.Err(); err != nil {
		return historyFacts{}, err
	}

	out := historyFacts{
		daysSinceHard: -1,
		daysSinceTest: -1,
	}
	out.cov7 = coverageCount(coverageDays, today.AddDate(0, 0, -6), today)
	out.cov28 = coverageCount(coverageDays, today.AddDate(0, 0, -27), today)
	out.cov90 = coverageCount(coverageDays, today.AddDate(0, 0, -89), today)
	if first.IsZero() {
		return out, nil
	}
	out.hasHistory = true
	out.firstDay = first
	out.lastDay = last
	out.loadDays42 = len(loadDays)
	if !firstLoad.IsZero() {
		out.dataDays = daysBetween(firstLoad, today)
	}
	if total > 0 {
		out.trainerShare = float64(trainer) / float64(total)
	}
	for day, d := range nonTest {
		if d.tss >= 80 || d.maxIF >= 0.85 {
			n := daysBetween(day, today)
			if out.daysSinceHard < 0 || n < out.daysSinceHard {
				out.daysSinceHard = n
			}
		}
	}
	for day := range testDays {
		n := daysBetween(day, today)
		if out.daysSinceTest < 0 || n < out.daysSinceTest {
			out.daysSinceTest = n
		}
	}
	return out, nil
}

// buildState собирает карточки PMC. CTL/ATL берутся на конец предыдущего дня,
// чтобы TSB = CTL − ATL сходился на одном моменте, а не смешивал сегодняшний
// CTL с TSB до сегодняшней нагрузки. Все вердикты считаются от округлённых
// значений, которые видит пользователь: иначе TSB −10.04 показывался бы как
// −10.0, а трактовался как «загружен».
func buildState(pmcDay store.Day, facts historyFacts, days []store.Day) statusState {
	ctl := round1(pmcDay.CTL)
	atl := round1(pmcDay.ATL)
	tsb := round1(ctl - atl)
	lowData := thresholds.LowData(facts.dataDays, facts.loadDays42)
	label := string(thresholds.TSBStateFor(tsb))
	confidence := "ok"
	if lowData {
		// Перегрузку не прячем за «мало данных»: при TSB < −25 это уже
		// важнее неполной истории, а пометку о данных покажет LowData.
		confidence = "low"
		if tsb >= thresholds.TSBLoadedMin {
			label = "lowdata"
		}
	}
	verdict, action := stateTextAt(label, tsb, facts)
	ctlTrend := round1(ctl - round1(valueDaysAgo(days, 28, func(d store.Day) float64 { return d.CTL })))
	ctlVerdict, ctlAction := ctlText(ctl, ctlTrend)
	if tsb < thresholds.TSBLightBelow {
		// Пока TSB ниже −15, совет «набирай объём» противоречит карточке
		// свежести и рекомендации на день: сначала восстановление.
		ctlAction = "Объём не наращивай — сначала восстановление"
	}
	if lowData {
		// На короткой истории тренд от нуля — ещё не рост формы.
		ctlVerdict = "Мало данных"
	}
	atlVerdict, atlAction := atlText(atl, ctl)
	tsbVerdict, tsbAction := tsbText(tsb)
	// Без истории спарклайн — плоская линия из нулей, а не данные.
	spark := func(pick func(store.Day) float64) string {
		if facts.dataDays == 0 {
			return ""
		}
		return sparkPoints(days, pick)
	}
	return statusState{
		Label:      label,
		LabelText:  stateLabelText(label),
		Verdict:    verdict,
		Action:     action,
		Confidence: confidence,
		LowData:    lowData,
		DataDays:   facts.dataDays,
		CTL: statusMetric{
			Value: ctl, Text: signedText(ctl), Verdict: ctlVerdict, Action: ctlAction,
			Trend: ctlTrend,
			Spark: spark(func(d store.Day) float64 { return d.CTL }),
		},
		ATL: statusMetric{
			Value: atl, Text: signedText(atl), Verdict: atlVerdict, Action: atlAction,
			Trend: round1(atl - round1(valueDaysAgo(days, 7, func(d store.Day) float64 { return d.ATL }))),
			Spark: spark(func(d store.Day) float64 { return d.ATL }),
		},
		TSB: statusMetric{
			Value: tsb, Text: signedText(tsb), Verdict: tsbVerdict, Action: tsbAction,
			Trend: round1(tsb - round1(valueDaysAgo(days, 14, func(d store.Day) float64 { return d.TSB }))),
			Spark: spark(func(d store.Day) float64 { return d.TSB }),
		},
	}
}

func valueDaysAgo(days []store.Day, n int, pick func(store.Day) float64) float64 {
	i := len(days) - 1 - n
	if i < 0 {
		return 0
	}
	return pick(days[i])
}

// stateText — текст карточки по классу TSB без точного значения. Верхняя
// строка статуса использует stateTextAt: там тексты совпадают с tsbText на
// тех же шести уровнях свежести.
func stateText(label string) (verdict, action string) {
	switch label {
	case "lowdata":
		return "Мало данных", "Нужно больше истории и дней с нагрузкой"
	case "fresh":
		return tsbText(thresholds.TSBFreshAbove + 11)
	case "loaded":
		return tsbText(thresholds.TSBLoadedMin + 1)
	case "overloaded":
		return tsbText(thresholds.TSBLoadedMin - 1)
	default:
		return tsbText(0)
	}
}

func stateTextAt(label string, tsb float64, facts historyFacts) (verdict, action string) {
	if label == "lowdata" {
		return "Мало данных", lowDataAction(facts)
	}
	return tsbText(tsb)
}

// lowDataAction говорит, чего именно не хватает: истории с первой нагрузки
// или дней с реальным TSS за 42 дня. Тон мягкий (TODO-UI §4.1): нехватка
// истории — не требование, а горизонт, когда рекомендации станут точнее.
func lowDataAction(facts historyFacts) string {
	needData := thresholds.MinDataDays - facts.dataDays
	needLoad := thresholds.MinLoadDays42 - facts.loadDays42
	switch {
	case needData > 0 && needLoad > 0:
		return fmt.Sprintf("Рекомендации станут точнее через ~%s истории и %s с нагрузкой за 42 дня",
			daysText(needData), daysText(needLoad))
	case needData > 0:
		return fmt.Sprintf("Рекомендации станут точнее через ~%s истории", daysText(needData))
	case needLoad > 0:
		return fmt.Sprintf("Рекомендации станут точнее: ещё %s с нагрузкой за 42 дня", daysText(needLoad))
	default:
		return "Данных пока мало для уверенного вердикта"
	}
}

func stateLabelText(label string) string {
	switch label {
	case "fresh":
		return "Свежий"
	case "loaded":
		return "Загружен"
	case "overloaded":
		return "Перегружен"
	case "lowdata":
		return "Мало данных"
	default:
		return "Норма"
	}
}

// makeLastWorkoutView готовит типизированные поля для шаблона главной.
func makeLastWorkoutView(w *store.Workout, cards []advice.Card) *lastWorkoutView {
	v := &lastWorkoutView{
		ID:        w.ID,
		Date:      w.StartTime.Format("02.01.2006 15:04"),
		Name:      w.Name,
		Kind:      "Улица",
		DurationH: round2(w.Duration.Hours()),
		KM:        round1(w.DistanceKm),
		AvgPower:  w.AvgPower,
		NormPower: w.NormPower,
		RPE:       w.RPE,
		Bike:      w.Bike,
		HasPower:  w.HasPower,
		Advice:    cards,
	}
	if w.Trainer {
		v.Kind = "Станок"
	}
	if w.HasTSS {
		// на дашборде дробные TSS округляем до целых (TODO-UI §4.1)
		tss := math.Round(w.TSS)
		v.TSS = &tss
	}
	if w.HasIF {
		v.IF = &w.IF
	}
	if w.HasVI {
		v.VI = &w.VI
	}
	return v
}

// sparkPoints — points для SVG-полилинии за последние 14 дней. Масштаб
// считается по самому ряду, поэтому карточки не зависят от абсолютных величин.
func sparkPoints(days []store.Day, pick func(store.Day) float64) string {
	if len(days) > 14 {
		days = days[len(days)-14:]
	}
	if len(days) == 0 {
		return ""
	}
	values := make([]float64, len(days))
	minV, maxV := math.Inf(1), math.Inf(-1)
	for i, d := range days {
		v := pick(d)
		values[i] = v
		minV = math.Min(minV, v)
		maxV = math.Max(maxV, v)
	}
	var b strings.Builder
	for i, v := range values {
		x := 0.0
		if len(values) > 1 {
			x = float64(i) * 120 / float64(len(values)-1)
		}
		y := 14.0
		if maxV > minV {
			y = 26 - (v-minV)/(maxV-minV)*24
		}
		fmt.Fprintf(&b, "%.1f,%.1f ", x, y)
	}
	return strings.TrimSpace(b.String())
}

// ctlText оценивает не уровень формы, а её тренд: высокий CTL может падать,
// и «форма растёт» на нём было бы неправдой.
func ctlText(ctl, trend float64) (verdict, action string) {
	switch {
	case trend > 2:
		verdict = "Форма растёт"
	case trend < -2:
		verdict = "Форма снижается"
	default:
		verdict = "Форма стабильна"
	}
	switch {
	case ctl < 10:
		action = "Набирай объём постепенно: 3–4 спокойные тренировки в неделю"
	case ctl < 30:
		action = "Держи регулярность и постепенно добавляй объём"
	case ctl < 50:
		action = "Можно добавить длинную Z2 и одну интервальную"
	default:
		action = "Поддерживай объём и следи за восстановлением"
	}
	return verdict, action
}

func atlText(atl, ctl float64) (verdict, action string) {
	if ctl <= 0 {
		if atl > 0 {
			return "Усталость есть, формы пока нет", "Не добавляй нагрузку, дай организму восстановиться"
		}
		return "Усталости почти нет", "Можно начать с лёгкой нагрузки"
	}
	ratio := atl / ctl
	switch {
	case ratio > 1.3:
		return "Усталость выше формы", "Не добавляй нагрузку 1–2 дня"
	case ratio >= 0.8:
		return "В рабочем диапазоне", "Организм справляется"
	default:
		return "Усталости почти нет", "Можно грузить смелее"
	}
}

// tsbText — шесть уровней свежести по ТЗ. Границы согласованы с
// thresholds.TSBStateFor: fresh/norm/loaded/overloaded остаются классами
// цвета, а внутри них текст уточняет, насколько глубоко зашла нагрузка.
func tsbText(tsb float64) (verdict, action string) {
	switch {
	case tsb > 20:
		return "Свежий", "Хороший день для качественной работы"
	case tsb > thresholds.TSBFreshAbove:
		return "Отдохнувший", "Можно тренироваться по плану"
	case tsb >= thresholds.TSBNormMin:
		return "Норма", "Тренируйся по плану, следи за восстановлением"
	case tsb >= -20:
		return "Под нагрузкой", "Лёгкая работа или отдых"
	case tsb >= thresholds.TSBLoadedMin:
		return "Загружен", "Лёгкая работа или отдых"
	default:
		return "Перегружен", "Отдых 1–2 дня, только Z1"
	}
}

func statusSummary(state statusState, week statusWeek, yesterdayTSS float64) string {
	weekText := fmt.Sprintf("%.0f TSS, цель не задана", week.TSS)
	if week.Target != nil {
		weekText = fmt.Sprintf("%.0f из %.0f TSS", week.TSS, *week.Target)
	}
	return fmt.Sprintf("%s. %d %s за неделю, %s. Вчера — %.0f TSS. %s.",
		state.Verdict, week.Rides, pluralRides(week.Rides), weekText,
		yesterdayTSS, state.Action)
}

// daysText — число с правильным склонением: «4 дня», а не «4 дней».
func daysText(n int) string {
	return fmt.Sprintf("%d %s", n, pluralDays(n))
}

// pluralDays — русское склонение для дней: 1 день, 2–4 дня, 5+ дней.
func pluralDays(n int) string {
	n10, n100 := n%10, n%100
	if n10 == 1 && n100 != 11 {
		return "день"
	}
	if n10 >= 2 && n10 <= 4 && !(n100 >= 12 && n100 <= 14) {
		return "дня"
	}
	return "дней"
}

// pluralRides — русское склонение: 1 тренировка, 2–4 тренировки, 5+ тренировок.
func pluralRides(n int) string {
	n10, n100 := n%10, n%100
	if n10 == 1 && n100 != 11 {
		return "тренировка"
	}
	if n10 >= 2 && n10 <= 4 && !(n100 >= 12 && n100 <= 14) {
		return "тренировки"
	}
	return "тренировок"
}

func statusAlerts(db *sql.DB, now time.Time, facts historyFacts, latest *store.Workout) ([]statusAlert, error) {
	today := store.StartOfDay(now)
	recent, err := recentWorkouts(db, 3, now)
	if err != nil {
		return nil, err
	}
	alerts := make([]statusAlert, 0, 6)
	if !facts.hasHistory || daysBetween(facts.lastDay, today) > 7 {
		text := "Тренировок пока нет."
		if facts.hasHistory {
			text = fmt.Sprintf("Нет тренировок %d дн. — пора вернуться в седло.", daysBetween(facts.lastDay, today))
		}
		alerts = append(alerts, statusAlert{Level: "warn", Code: "stale", Text: text})
	}
	if latest != nil && latest.HasDataGaps() {
		alerts = append(alerts, statusAlert{
			Level: "warn",
			Code:  "data_gaps",
			Text: fmt.Sprintf("В последней тренировке %.1f%% времени без достоверной мощности.",
				latest.DataGapsPct()),
		})
	}
	if len(recent) >= 3 && allNoHR(recent) {
		alerts = append(alerts, statusAlert{Level: "info", Code: "no_hr", Text: "В последних 3 тренировках нет пульса."})
	}
	if len(recent) >= 3 && allNoRPE(recent) {
		alerts = append(alerts, statusAlert{Level: "info", Code: "no_rpe", Text: "В последних 3 тренировках не заполнен RPE."})
	}
	hints, err := store.FTPHints(db, now)
	if err != nil {
		return nil, err
	}
	for _, h := range hints {
		kind := "станковый"
		if h.Kind == "outdoor" {
			kind = "уличный"
		}
		alerts = append(alerts, statusAlert{
			Level: "info",
			Code:  "ftp_hint",
			Text: fmt.Sprintf("%s FTP можно поднять: %.1f Вт против %.1f Вт (+%.1f%%).",
				kind, h.Estimate, h.Current, h.DeltaPct),
		})
	}
	if thresholds.LowData(facts.dataDays, facts.loadDays42) {
		alerts = append(alerts, statusAlert{
			Level: "info",
			Code:  "lowdata",
			Text: fmt.Sprintf("Мало данных: %s с первой нагрузки, %s с TSS>0 за 42 дня — рекомендация осторожная.",
				daysText(facts.dataDays), daysText(facts.loadDays42)),
		})
	}
	rank := map[string]int{"stale": 0, "data_gaps": 1, "no_hr": 2, "lowdata": 3, "no_rpe": 4, "ftp_hint": 5}
	sort.SliceStable(alerts, func(i, j int) bool { return rank[alerts[i].Code] < rank[alerts[j].Code] })
	return alerts, nil
}

func recentWorkouts(db *sql.DB, n int, now time.Time) ([]*store.Workout, error) {
	rows, err := db.Query(`SELECT id FROM workouts WHERE start_time <= ?
	    ORDER BY start_time DESC, id DESC LIMIT ?`,
		now.UTC().Format(time.RFC3339), n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*store.Workout, 0, len(ids))
	for _, id := range ids {
		w, err := store.GetWorkout(db, id)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

func allNoHR(list []*store.Workout) bool {
	for _, w := range list {
		if w.AvgHR > 0 || w.MaxHR > 0 {
			return false
		}
	}
	return true
}

func allNoRPE(list []*store.Workout) bool {
	for _, w := range list {
		if w.RPE > 0 {
			return false
		}
	}
	return true
}

// workoutJSONWithAdvice — тренировка как есть плюс карточки советов. Через map,
// а не embedding: Workout.MarshalJSON иначе перекрыл бы поле advice.
func workoutJSONWithAdvice(w *store.Workout, cards []advice.Card) (map[string]any, error) {
	raw, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if cards == nil {
		cards = []advice.Card{}
	}
	out["advice"] = cards
	return out, nil
}

func dayOfWeek(t time.Time) int {
	wd := int(t.Weekday())
	if wd == 0 {
		return 7
	}
	return wd
}

func daysBetween(from, to time.Time) int {
	from, to = store.StartOfDay(from), store.StartOfDay(to)
	n := 0
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		n++
	}
	return n
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// signedText — число с одним знаком после запятой и типографским минусом:
// так минус на «Сегодня» совпадает с прогрессом, не меняя хранение данных.
func signedText(v float64) string {
	if v == 0 {
		v = 0 // снимает отрицательный нуль
	}
	return strings.ReplaceAll(strconv.FormatFloat(v, 'f', 1, 64), "-", "−")
}
