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
)

// statusResponse — композитный ответ главной: состояние, рекомендация,
// неделя, последняя тренировка, готовность и предупреждения.
type statusResponse struct {
	AsOf           string               `json:"asOf"`
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
	Start          string      `json:"start"`
	DayOfWeek      int         `json:"dayOfWeek"`
	TSS            float64     `json:"tss"`
	Target         float64     `json:"target"`
	TargetToDate   float64     `json:"targetToDate"`
	Remaining      float64     `json:"remaining"`
	RemainingMinZ2 int         `json:"remainingMinZ2"`
	Hours          float64     `json:"hours"`
	KM             float64     `json:"km"`
	Rides          int         `json:"rides"`
	TrainerHours   float64     `json:"trainerHours"`
	OutdoorHours   float64     `json:"outdoorHours"`
	PrevWeekTSS    float64     `json:"prevWeekTSS"`
	DeltaPct       *float64    `json:"deltaPct"`
	Zones          statusZones `json:"zones"`

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
	dataDays      int
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
	target := week.Target
	weekTSS := week.rawTSS

	trainerFTP, _ := store.ProfileAt(db, "ftp", now)
	outdoorFTP, _ := store.ProfileAt(db, "ftp_outdoor", now)

	in := plan.Input{
		DataDays:      facts.dataDays,
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
	if in.DaysSinceTest < 0 {
		// теста не было: для правила 4 это «очень давно», а не «сегодня».
		in.DaysSinceTest = facts.dataDays
	}

	var rec plan.Recommendation
	var checks []plan.RuleCheck
	if debug {
		rec, checks = plan.Explain(in)
	} else {
		rec = plan.Recommend(in)
	}

	state := buildState(cur, facts, days)

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
	cur := weeks[len(weeks)-1]
	prev := weeks[len(weeks)-2]

	target, err := store.WeeklyTarget(db, now)
	if err != nil {
		return statusWeek{}, 0, err
	}
	dow := dayOfWeek(now)
	tss := cur.TSS
	remaining := math.Max(0, target-tss)
	var delta *float64
	if prev.TSS > 0 {
		v := round1((tss - prev.TSS) / prev.TSS * 100)
		delta = &v
	}
	return statusWeek{
		Start:          cur.Start.Format("2006-01-02"),
		DayOfWeek:      dow,
		TSS:            round1(tss),
		Target:         target,
		TargetToDate:   round1(target * float64(dow-1) / 7),
		Remaining:      math.Round(remaining),
		RemainingMinZ2: z2Minutes(remaining),
		Hours:          round2(cur.Hours),
		KM:             round1(cur.DistanceKm),
		Rides:          cur.Rides,
		TrainerHours:   round2(cur.TrainerHours),
		OutdoorHours:   round2(cur.OutdoorHours),
		PrevWeekTSS:    round1(prev.TSS),
		DeltaPct:       delta,
		Zones: statusZones{
			LowPct:  cur.LowPct,
			GrayPct: cur.GrayPct,
			HighPct: cur.HighPct,
		},
		rawTSS: tss,
	}, grayPct4(weeks), nil
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
	return int(math.Round(minutes/5) * 5)
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
	var trainer, total int
	shareFrom := store.WeekStart(now).AddDate(0, 0, -21)
	shareTo := store.WeekStart(now).AddDate(0, 0, 7)

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
	out.dataDays = daysBetween(first, today)
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

func buildState(cur store.Day, facts historyFacts, days []store.Day) statusState {
	label := "norm"
	confidence := "ok"
	if facts.dataDays < 21 {
		label = "lowdata"
		confidence = "low"
	} else {
		switch {
		case cur.TSB > 10:
			label = "fresh"
		case cur.TSB >= -10:
			label = "norm"
		case cur.TSB >= -20:
			label = "loaded"
		default:
			label = "overloaded"
		}
	}
	verdict, action := stateText(label)
	ctlVerdict, ctlAction := ctlText(cur.CTL, cur.ATL)
	atlVerdict, atlAction := atlText(cur.ATL, cur.CTL)
	tsbVerdict, tsbAction := tsbText(cur.TSB)
	return statusState{
		Label:      label,
		Verdict:    verdict,
		Action:     action,
		Confidence: confidence,
		CTL: statusMetric{
			Value: cur.CTL, Verdict: ctlVerdict, Action: ctlAction,
			Trend: round1(cur.CTL - valueDaysAgo(days, 28, func(d store.Day) float64 { return d.CTL })),
		},
		ATL: statusMetric{
			Value: cur.ATL, Verdict: atlVerdict, Action: atlAction,
			Trend: round1(cur.ATL - valueDaysAgo(days, 7, func(d store.Day) float64 { return d.ATL })),
		},
		TSB: statusMetric{
			Value: cur.TSB, Verdict: tsbVerdict, Action: tsbAction,
			Trend: round1(cur.TSB - valueDaysAgo(days, 14, func(d store.Day) float64 { return d.TSB })),
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

func ctlText(ctl, atl float64) (verdict, action string) {
	switch {
	case ctl < 10:
		return "Форма низкая", "Набирай объём постепенно: 3–4 спокойные тренировки в неделю"
	case ctl < 30:
		return "Форма растёт", "Держи регулярность и постепенно добавляй объём"
	case ctl < 50:
		return "Форма хорошая", "Можно добавить длинную Z2 и одну интервальную"
	default:
		return "Форма высокая", "Поддерживай объём и следи за восстановлением"
	}
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
	switch {
	case tsb > 10:
		return "Свежесть хорошая", "Хороший день для качественной работы"
	case tsb >= -10:
		return "Свежесть в норме", "Тренируйся по плану"
	case tsb >= -20:
		return "Загруженность", "Лёгкая работа или отдых"
	default:
		return "Перегрузка", "Отдых 1–2 дня, только Z1"
	}
}

func statusSummary(state statusState, week statusWeek, yesterdayTSS float64) string {
	return fmt.Sprintf("%s. %d %s за неделю, %.0f из %.0f. Вчера — %.0f TSS. %s.",
		state.Verdict, week.Rides, pluralRides(week.Rides), week.TSS, week.Target,
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
	if facts.dataDays < 21 {
		alerts = append(alerts, statusAlert{
			Level: "info",
			Code:  "lowdata",
			Text:  fmt.Sprintf("Мало данных: %d дн. истории, рекомендация осторожная.", facts.dataDays),
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
