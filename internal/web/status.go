package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
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
	PMCAsOf        string               `json:"pmcAsOf"`
	TSBAsOf        string               `json:"tsbAsOf"`
	TSBNote        string               `json:"tsbNote"`
	State          statusState          `json:"state"`
	Summary        string               `json:"summary"`
	Recommendation plan.Recommendation  `json:"recommendation"`
	Week           statusWeek           `json:"week"`
	LastWorkout    any                  `json:"lastWorkout"`
	Readiness      store.ReadinessStats `json:"readiness"`
	Alerts         []statusAlert        `json:"alerts"`
	AlertsAll      []statusAlert        `json:"alertsAll"`
	Rules          []plan.RuleCheck     `json:"rules,omitempty"`
}

// statusState — общая оценка дня и разбор трёх метрик PMC по отдельности.
// ctl/atl/tsb — объекты, потому что у каждой карточки свои verdict/action/trend.
type statusState struct {
	Label      string       `json:"label"`
	Verdict    string       `json:"verdict"`
	Action     string       `json:"action"`
	Confidence string       `json:"confidence"`
	CTL        statusMetric `json:"ctl"`
	ATL        statusMetric `json:"atl"`
	TSB        statusMetric `json:"tsb"`
}

type statusMetric struct {
	Value   float64 `json:"value"`
	Verdict string  `json:"verdict"`
	Action  string  `json:"action"`
	Trend   float64 `json:"trend"`
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
	dataDays      int // дней от первой нагрузки в окне 42 дней
	loadDays42    int // различных дней с TSS > 0 за 42 дня
	daysSinceHard int // -1 — тяжёлой тренировки не было
	daysSinceTest int // -1 — тестовой тренировки не было
	trainerShare  float64
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
	days, err := store.Timeline(db, today.AddDate(0, 0, -28), today)
	if err != nil {
		return nil, err
	}
	if len(days) == 0 {
		return nil, fmt.Errorf("пустой таймлайн")
	}
	cur := days[len(days)-1]

	week, gray4, err := statusWeekFor(db, now)
	if err != nil {
		return nil, err
	}
	target := 0.0
	if week.Target != nil {
		target = *week.Target
	}
	weekTSS := week.rawTSS

	trainerFTP, _ := store.ProfileAt(db, "ftp", now)
	outdoorFTP, _ := store.ProfileAt(db, "ftp_outdoor", now)

	in := plan.Input{
		DataDays:      facts.dataDays,
		LoadDays42:    facts.loadDays42,
		TSB:           cur.TSB,
		CTL:           cur.CTL,
		ATL:           cur.ATL,
		DaysSinceHard: facts.daysSinceHard,
		DaysSinceTest: facts.daysSinceTest,
		WeekTSS:       weekTSS,
		TargetTSS:     target,
		DayOfWeek:     dayOfWeek(now),
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

	pmcDay := cur
	if len(days) >= 2 {
		pmcDay = days[len(days)-2]
	}
	state := buildState(cur, pmcDay, facts, days)

	latest, err := store.LatestWorkout(db)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	var lastWorkout any
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

	yesterday := 0.0
	if len(days) >= 2 {
		yesterday = days[len(days)-2].TSS
	}
	out := &statusResponse{
		AsOf:           today.Format("2006-01-02"),
		PMCAsOf:        pmcDay.Date.Format("2006-01-02"),
		TSBAsOf:        pmcDay.Date.Format("2006-01-02"),
		TSBNote:        "TSB — до нагрузки сегодняшнего дня",
		State:          state,
		Summary:        statusSummary(state, week, yesterday),
		Recommendation: rec,
		Week:           week,
		LastWorkout:    lastWorkout,
		Readiness:      readiness,
		Alerts:         alerts,
		AlertsAll:      alertsAll,
		Rules:          checks,
	}
	return out, nil
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
// CTL с TSB до сегодняшней нагрузки.
func buildState(cur, pmcDay store.Day, facts historyFacts, days []store.Day) statusState {
	ctl, atl := pmcDay.CTL, pmcDay.ATL
	tsb := ctl - atl
	label := string(thresholds.TSBStateFor(tsb))
	confidence := "ok"
	if thresholds.LowData(facts.dataDays, facts.loadDays42) {
		label = "lowdata"
		confidence = "low"
	}
	verdict, action := stateText(label)
	ctlTrend := round1(ctl - valueDaysAgo(days, 28, func(d store.Day) float64 { return d.CTL }))
	ctlVerdict, ctlAction := ctlText(ctl, ctlTrend)
	atlVerdict, atlAction := atlText(atl, ctl)
	tsbVerdict, tsbAction := tsbText(tsb)
	return statusState{
		Label:      label,
		Verdict:    verdict,
		Action:     action,
		Confidence: confidence,
		CTL: statusMetric{
			Value: round1(ctl), Verdict: ctlVerdict, Action: ctlAction,
			Trend: ctlTrend,
		},
		ATL: statusMetric{
			Value: round1(atl), Verdict: atlVerdict, Action: atlAction,
			Trend: round1(atl - valueDaysAgo(days, 7, func(d store.Day) float64 { return d.ATL })),
		},
		TSB: statusMetric{
			Value: round1(tsb), Verdict: tsbVerdict, Action: tsbAction,
			Trend: round1(tsb - valueDaysAgo(days, 14, func(d store.Day) float64 { return d.TSB })),
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

func stateText(label string) (verdict, action string) {
	switch label {
	case "fresh":
		return "Свежий", "Хороший день для качественной работы"
	case "loaded":
		return "Загружен", "Лёгкая работа или отдых"
	case "overloaded":
		return "Перегружен", "Отдых 1–2 дня, только Z1"
	case "lowdata":
		return "Мало данных", "Собери минимум 3 недели истории"
	default:
		return "Форма в норме", "Тренируйся по плану, следи за восстановлением"
	}
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
	switch {
	case atl < 10:
		return "Усталость низкая", "Можно тренироваться по плану"
	case atl < 30:
		return "Усталость умеренная", "Следи за сном и не добавляй лишнюю интенсивность"
	case atl < 50:
		return "Усталость высокая", "Следующие 1–2 дня — лёгкие"
	default:
		return "Усталость очень высокая", "Нужен отдых или только Z1"
	}
}

func tsbText(tsb float64) (verdict, action string) {
	switch thresholds.TSBStateFor(tsb) {
	case thresholds.Fresh:
		return "Свежесть хорошая", "Хороший день для качественной работы"
	case thresholds.Norm:
		return "Свежесть в норме", "Тренируйся по плану"
	case thresholds.Loaded:
		return "Загруженность", "Лёгкая работа или отдых"
	default:
		return "Перегрузка", "Отдых 1–2 дня, только Z1"
	}
}

func statusSummary(state statusState, week statusWeek, yesterdayTSS float64) string {
	weekText := fmt.Sprintf("%.0f TSS", week.TSS)
	if week.Target != nil {
		weekText = fmt.Sprintf("%.0f из %.0f TSS", week.TSS, *week.Target)
	}
	return fmt.Sprintf("%s. %d %s за неделю, %s. Вчера — %.0f TSS. %s.",
		state.Verdict, week.Rides, pluralRides(week.Rides), weekText,
		yesterdayTSS, state.Action)
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
	recent, err := recentWorkouts(db, 3)
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
	if len(hints) > 0 {
		h := hints[0]
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
			Text: fmt.Sprintf("Мало данных: %d дн. с нагрузкой, %d дней с TSS>0 за 42 дня — рекомендация осторожная.",
				facts.dataDays, facts.loadDays42),
		})
	}
	rank := map[string]int{"stale": 0, "data_gaps": 1, "no_hr": 2, "no_rpe": 3, "ftp_hint": 4, "lowdata": 5}
	sort.SliceStable(alerts, func(i, j int) bool { return rank[alerts[i].Code] < rank[alerts[j].Code] })
	return alerts, nil
}

func recentWorkouts(db *sql.DB, n int) ([]*store.Workout, error) {
	rows, err := db.Query(`SELECT id FROM workouts ORDER BY start_time DESC, id DESC LIMIT ?`, n)
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
