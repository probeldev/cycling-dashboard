package store

import "time"

// ProfilePoint — действующие значения профиля на календарную дату.
// Указатели: null значит «значения ещё не было» (или вес/FTP не задан),
// а не ноль. W/kg считается только когда есть и FTP, и положительный вес.
type ProfilePoint struct {
	Date          time.Time `json:"date"` // локальная полночь
	Weight        *float64  `json:"weight"`
	FTPTrainer    *float64  `json:"ftpTrainer"`
	FTPOutdoor    *float64  `json:"ftpOutdoor"`
	WPerKgTrainer *float64  `json:"wPerKgTrainer"`
	WPerKgOutdoor *float64  `json:"wPerKgOutdoor"`
}

// ProfileSeries строит дневной ряд [from, to] из profile_history: значение
// меняется только при новой записи (шаг), а не между записями. Ряд начинается
// с более поздней из дат from и первой записи истории; до первой записи
// конкретного вида значение — null (не подставляем самое раннее: так график
// не врёт о прошлом). Пустая история — пустой массив, а не ряд из null.
func ProfileSeries(db DBTX, from, to time.Time) ([]ProfilePoint, error) {
	from, to = StartOfDay(from), StartOfDay(to)
	out := []ProfilePoint{}
	if to.Before(from) {
		return out, nil
	}
	rows, err := db.Query(`SELECT kind, value, recorded_at FROM profile_history
		WHERE kind IN ('weight', 'ftp', 'ftp_outdoor')
		ORDER BY recorded_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type entry struct {
		day   time.Time
		value float64
	}
	byKind := map[string][]entry{}
	var earliest time.Time
	for rows.Next() {
		var kind, at string
		var value float64
		if err := rows.Scan(&kind, &value, &at); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return nil, err
		}
		day := StartOfDay(t.Local())
		byKind[kind] = append(byKind[kind], entry{day: day, value: value})
		if earliest.IsZero() || day.Before(earliest) {
			earliest = day
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if earliest.IsZero() {
		return out, nil
	}
	start := from
	if earliest.After(start) {
		start = earliest
	}
	if start.After(to) {
		return out, nil
	}

	kinds := []string{"weight", "ftp", "ftp_outdoor"}
	idx := map[string]int{}
	cur := map[string]*float64{}
	for d := start; !d.After(to); d = d.AddDate(0, 0, 1) {
		for _, kind := range kinds {
			entries := byKind[kind]
			for idx[kind] < len(entries) && !entries[idx[kind]].day.After(d) {
				v := entries[idx[kind]].value
				cur[kind] = &v
				idx[kind]++
			}
		}
		p := ProfilePoint{
			Date:          d,
			Weight:        cur["weight"],
			FTPTrainer:    cur["ftp"],
			FTPOutdoor:    cur["ftp_outdoor"],
			WPerKgTrainer: wPerKg(cur["ftp"], cur["weight"]),
			WPerKgOutdoor: wPerKg(cur["ftp_outdoor"], cur["weight"]),
		}
		out = append(out, p)
	}
	return out, nil
}

// wPerKg — Вт/кг; null без FTP или при нулевом/отрицательном весе.
func wPerKg(ftp, weight *float64) *float64 {
	if ftp == nil || weight == nil || *weight <= 0 {
		return nil
	}
	v := *ftp / *weight
	return &v
}
