package web

import (
	"database/sql"
	"net/http"
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
		today := days[len(days)-1]

		// текущая неделя пн–вс и предыдущие 4 полные недели
		ws := store.WeekStart(now)
		var weekTSS, prev4 float64
		for _, d := range days {
			if !d.Date.Before(ws) {
				weekTSS += d.TSS
			}
			if !d.Date.Before(ws.AddDate(0, 0, -28)) && d.Date.Before(ws) {
				prev4 += d.TSS
			}
		}
		render(w, "progress.html", map[string]any{
			"Today":     today,
			"WeekTSS":   weekTSS,
			"Prev4TSS":  prev4,
			"TSBLabel":  tsbLabel(today.TSB),
			"TodayDate": now.Format("02.01.2006"),
		})
	})
}

// tsbLabel — словесная трактовка свежести.
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
