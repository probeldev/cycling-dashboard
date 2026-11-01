package web

import (
	"database/sql"
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

		// текущая неделя пн–вс и 4 недели вместе с ней (текущая + 3 полные)
		ws := store.WeekStart(now)
		var weekTSS, last4 float64
		for _, d := range days {
			if !d.Date.Before(ws) {
				weekTSS += d.TSS
			}
			if !d.Date.Before(ws.AddDate(0, 0, -21)) {
				last4 += d.TSS
			}
		}
		// TSB считаем от округлённых CTL/ATL — тех же чисел, что в карточках:
		// иначе на границе −10.04 число и подпись расходились бы со статусом.
		tsb := round1(round1(today.CTL) - round1(today.ATL))
		// CP/W' по всем данным: окна MMP за всё время
		allRecords, err := store.BestRecords(db, time.Time{}, to.AddDate(0, 0, 1), false)
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
		render(w, "progress.html", map[string]any{
			"Bikes":     bikes,
			"CP":        cpView(cp),
			"Wkg":       buildWkgTrend(series, to),
			"Today":     today,
			"WeekTSS":   weekTSS,
			"Last4TSS":  last4,
			"TSBText":   format1(tsb),
			"TSBLabel":  tsbLabel(tsb),
			"TodayDate": now.Format("02.01.2006"),
			"Readiness": readiness,
			"FTPHint":   ftpHint,
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
