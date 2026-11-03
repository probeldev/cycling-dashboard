package web

import (
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"cycling-dashboard/internal/store"
	"cycling-dashboard/internal/thresholds"
)

func progressRoutes(r chi.Router, db *sql.DB) {
	r.Get("/progress", func(w http.ResponseWriter, req *http.Request) {
		now := time.Now()
		to := store.StartOfDay(now)
		days, err := store.TimelineUntil(db, to.AddDate(0, 0, -364), to, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(days) == 0 {
			http.Error(w, "пустой таймлайн", http.StatusInternalServerError)
			return
		}
		today := days[len(days)-1]
		// PMC показываем на конец предыдущего дня — как на главной: иначе
		// сегодняшние CTL/ATL смешивались бы с TSB до нагрузки сегодняшнего дня.
		pmcDay := today
		if len(days) >= 2 {
			pmcDay = days[len(days)-2]
		}

		// текущая неделя пн–вс и ровно 28-дневное окно, включая сегодня:
		// подпись «из 28» должна совпадать с фактическим числом дней окна.
		ws := store.WeekStart(now)
		last4From := to.AddDate(0, 0, -27)
		var weekTSS, last4 float64
		for _, d := range days {
			if !d.Date.Before(ws) {
				weekTSS += d.TSS
			}
			if !d.Date.Before(last4From) {
				last4 += d.TSS
			}
		}
		// TSB считаем от округлённых CTL/ATL — тех же чисел, что в карточках:
		// иначе на границе −10.04 число и подпись расходились бы со статусом.
		tsb := round1(round1(pmcDay.CTL) - round1(pmcDay.ATL))
		// CP/W' по всем данным: окна MMP за всё время, но не позже now —
		// будущая тренировка не должна менять фит.
		allRecords, err := store.BestRecordsUntil(db, time.Time{}, now, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cp := fitCP(powerPoints(allRecords))
		bikes, err := store.BikeStats(db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// 366 дней, чтобы у изменения за 365 дней была точка отсчёта
		series, err := store.ProfileSeries(db, to.AddDate(0, 0, -365), to)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		readiness, err := store.Readiness(db, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		hints, err := store.FTPHints(db, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var ftpHint *store.FTPHint
		if len(hints) > 0 {
			ftpHint = &hints[0]
		}
		// тренды PMC — изменение за 7 дней от даты карточек
		weekAgo := pmcDay.Date.AddDate(0, 0, -7)
		var prevPMC *store.Day
		for i := range days {
			if days[i].Date.Equal(weekAgo) {
				prevPMC = &days[i]
				break
			}
		}
		var ctlTrend, atlTrend, tsbTrend *pmcTrend
		if prevPMC != nil {
			ctlTrend = buildPMCTrend(pmcDay.CTL, prevPMC.CTL, true)
			atlTrend = buildPMCTrend(pmcDay.ATL, prevPMC.ATL, false)
			tsbTrend = buildPMCTrend(tsb, round1(prevPMC.TSB), true)
		}
		// Покрытие окон: в скольких днях были тренировки. Для PMC отдельно
		// считаем историю и дни с реальной нагрузкой за 42 дня — те же
		// границы «мало данных», что на главной.
		day7From := to.AddDate(0, 0, -6)
		day90From := to.AddDate(0, 0, -89)
		load42From := to.AddDate(0, 0, -41)
		var weekRideDays, last4RideDays, cov7, cov28, cov90, loadDays42 int
		var firstLoad time.Time
		for _, d := range days {
			if d.TSS > 0 {
				if firstLoad.IsZero() || d.Date.Before(firstLoad) {
					firstLoad = d.Date
				}
				if !d.Date.Before(load42From) {
					loadDays42++
				}
			}
			if d.Rides == 0 {
				continue
			}
			if !d.Date.Before(ws) {
				weekRideDays++
			}
			if !d.Date.Before(last4From) {
				last4RideDays++
			}
			if !d.Date.Before(day7From) {
				cov7++
			}
			if !d.Date.Before(last4From) {
				cov28++
			}
			if !d.Date.Before(day90From) {
				cov90++
			}
		}
		dataDays := 0
		if !firstLoad.IsZero() {
			dataDays = daysBetween(firstLoad, to) + 1
		}
		// Скорость для инсайта «Первая сотня» — только по уличным поездкам:
		// Readiness.Rolling28.Hours включает станок, а KM — нет.
		outdoorKM, outdoorHours, err := outdoorRolling28(db, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// текущий FTP станка — пунктирный референс на MMP-кривой
		var currentFTP any
		if ftp, ok := store.ProfileAt(db, "ftp", now); ok {
			currentFTP = ftp
		}
		render(w, "progress.html", map[string]any{
			"Bikes":          bikes,
			"CP":             cpView(cp),
			"Wkg":            buildWkgTrend(series, to),
			"PMC":            pmcDay,
			"PMCDate":        pmcDay.Date.Format("02.01.2006"),
			"WeekTSS":        weekTSS,
			"Last4TSS":       last4,
			"TSBText":        format1(tsb),
			"TSBLabel":       tsbLabel(tsb),
			"TodayDate":      now.Format("02.01.2006"),
			"Readiness":      readiness,
			"FTPHint":        ftpHint,
			"CTLTrend":       ctlTrend,
			"ATLTrend":       atlTrend,
			"TSBTrend":       tsbTrend,
			"WeekCoverage":   fmt.Sprintf("%s из 7", daysText(weekRideDays)),
			"Last4Coverage":  fmt.Sprintf("%s из 28", daysText(last4RideDays)),
			"Cov7":           cov7,
			"Cov28":          cov28,
			"Cov90":          cov90,
			"PMCLowData":     thresholds.LowData(dataDays, loadDays42),
			"PMCLoadDays":    loadDays42,
			"CurrentFTP":     currentFTP,
			"CenturyInsight": centuryInsight(readiness, outdoorKM, outdoorHours),
		})
	})
}

// tsbLabel — словесная трактовка свежести. Считается от того же округлённого
// значения, что показывается в карточке, чтобы подпись не спорила с числом.
func tsbLabel(tsb float64) string {
	switch thresholds.TSBStateFor(tsb) {
	case thresholds.Fresh:
		return "свежий"
	case thresholds.Loaded:
		return "загружен"
	case thresholds.Overloaded:
		return "перегружен"
	default:
		return "норма"
	}
}

// format1 — число с одним знаком после запятой, без «-0.0».
func format1(v float64) string {
	if v == 0 {
		v = 0 // снимает отрицательный нуль
	}
	return strings.ReplaceAll(strconv.FormatFloat(v, 'f', 1, 64), "-", "−")
}

// pmcTrend — изменение показателя за 7 дней: текст со стрелкой и класс цвета
// (.trend.up — зелёный, .trend.down — красный). Класс отражает пользу
// направления: рост ATL (усталости) — красный, рост CTL — зелёный.
type pmcTrend struct {
	Text  string
	Class string
}

// buildPMCTrend — nil, если изменение меньше десятой: «без изменений»
// не рисуем, чтобы карточки не шумели нулевыми трендами.
func buildPMCTrend(cur, prev float64, upIsGood bool) *pmcTrend {
	d := round1(cur) - round1(prev)
	if math.Abs(d) < 0.05 {
		return nil
	}
	arrow := "▲"
	if d < 0 {
		arrow = "▼"
	}
	class := "up"
	if (d > 0) != upIsGood {
		class = "down"
	}
	return &pmcTrend{Text: fmt.Sprintf("%s %s за 7 дн", arrow, format1(math.Abs(d))), Class: class}
}

// outdoorRolling28 — километры и часы уличных поездок за 28 дней.
// Станок в темп не входит: у него нет дистанции, и его часы занижали бы
// скорость в инсайте «Первая сотня».
func outdoorRolling28(db *sql.DB, now time.Time) (km, hours float64, err error) {
	from := store.StartOfDay(now).AddDate(0, 0, -27)
	var distM float64
	var secs int64
	err = db.QueryRow(`SELECT COALESCE(SUM(distance_m), 0), COALESCE(SUM(duration_sec), 0)
	    FROM workouts
	    WHERE trainer = 0 AND is_test = 0 AND distance_m > 0 AND duration_sec > 0
	      AND start_time >= ? AND start_time <= ?`,
		from.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339)).Scan(&distM, &secs)
	if err != nil {
		return 0, 0, err
	}
	return distM / 1000, float64(secs) / 3600, nil
}

// centuryInsight — инсайт к блоку готовности, когда сотенных поездок ещё не
// было: сколько километров не хватает до первой и сколько это часов спокойной
// езды при текущем уличном темпе (окно 28 дней; запасной темп — 25 км/ч).
func centuryInsight(r store.ReadinessStats, outdoorKM, outdoorHours float64) string {
	if r.CenturyCount > 0 {
		return ""
	}
	var longest float64
	if r.LongestEver != nil {
		longest = r.LongestEver.KM
	}
	if longest <= 0 {
		return "Сотенных поездок пока не было — база строится регулярными выездами 2–3 ч в Z2."
	}
	gap := math.Max(1, math.Round(100-longest))
	speed := 25.0
	if outdoorHours > 0 && outdoorKM > 0 {
		speed = outdoorKM / outdoorHours
	}
	return fmt.Sprintf("До первой сотни не хватает ~%.0f км — это примерно %s спокойной езды в Z2 при текущем темпе.",
		gap, formatHours(gap/speed))
}
