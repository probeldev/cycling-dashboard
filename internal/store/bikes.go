package store

// Bike — велосипед пользователя. Станок — не велосипед: тип тренировки уже
// отмечен флагом trainer, а bike_id указывает на конкретную раму (или NULL).
type Bike struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"` // road | fixed | mtb | gravel | other
	Active bool   `json:"active"`
}

// Bikes — активные велосипеды по имени.
func Bikes(db DBTX) ([]Bike, error) {
	rows, err := db.Query(`SELECT id, name, kind, active FROM bikes WHERE active = 1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Bike{}
	for rows.Next() {
		var b Bike
		if err := rows.Scan(&b.ID, &b.Name, &b.Kind, &b.Active); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BikeByID — велосипед по id (sql.ErrNoRows, если нет).
func BikeByID(db DBTX, id int64) (*Bike, error) {
	var b Bike
	err := db.QueryRow(`SELECT id, name, kind, active FROM bikes WHERE id = ?`, id).
		Scan(&b.ID, &b.Name, &b.Kind, &b.Active)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// SetWorkoutBike назначает велосипед тренировке; nil — сбросить в NULL.
// Это пользовательское поле: ре-импорт (upsert) его не трогает.
func SetWorkoutBike(db DBTX, id int64, bikeID *int64) error {
	var v any
	if bikeID != nil {
		v = *bikeID
	}
	_, err := db.Exec(`UPDATE workouts SET bike_id = ? WHERE id = ?`, v, id)
	return err
}

// BikeStat — итог по велосипеду.
type BikeStat struct {
	Name  string  `json:"name"`
	Rides int     `json:"rides"`
	Hours float64 `json:"hours"`
	KM    float64 `json:"km"`
}

// BikeStats — пробег/часы/поездки по велосипедам (только назначенные).
func BikeStats(db DBTX) ([]BikeStat, error) {
	rows, err := db.Query(`SELECT b.name, COUNT(*), SUM(w.duration_sec), SUM(COALESCE(w.distance_m, 0))
	    FROM workouts w JOIN bikes b ON b.id = w.bike_id
	    GROUP BY b.id ORDER BY SUM(COALESCE(w.distance_m, 0)) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BikeStat{}
	for rows.Next() {
		var s BikeStat
		var secs int64
		var dist float64
		if err := rows.Scan(&s.Name, &s.Rides, &secs, &dist); err != nil {
			return nil, err
		}
		s.Hours = float64(secs) / 3600
		s.KM = dist / 1000
		out = append(out, s)
	}
	return out, rows.Err()
}
