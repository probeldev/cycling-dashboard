package web

import (
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"cycling-dashboard/internal/store"
)

func progressRoutes(r chi.Router, db *sql.DB) {
	r.Get("/progress", func(w http.ResponseWriter, req *http.Request) {
		now := time.Now()
		to := store.StartOfDay(now)
		days, err := store.Timeline(db, to.AddDate(0, 0, -364), to)
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
		tsb := round1(today.TSB)
		// CP/W' по всем данным: окна MMP за всё время
		allRecords, err := store.BestRecords(db, time.Time{}, to.AddDate(0, 0, 1))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cp := fitCP(powerPoints(allRecords))
		render(w, "progress.html", map[string]any{
			"CP":        cpView(cp),
			"Today":     today,
			"WeekTSS":   weekTSS,
			"Last4TSS":  last4,
			"TSBText":   format1(tsb),
			"TSBLabel":  tsbLabel(tsb),
			"TodayDate": now.Format("02.01.2006"),
		})
	})
}

// tsbLabel — словесная трактовка свежести. Считается от того же округлённого
// значения, что показывается в карточке, чтобы подпись не спорила с числом.
func tsbLabel(tsb float64) string {
	switch {
	case tsb < -10:
		return "загружен"
	case tsb > 5:
		return "свежий"
	default:
		return "норма"
	}
}

// format1 — число с одним знаком после запятой, без «-0.0».
func format1(v float64) string {
	if v == 0 {
		v = 0 // снимает отрицательный нуль
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}
