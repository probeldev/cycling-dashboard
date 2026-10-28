package importer

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tormoder/fit"

	"cycling-dashboard/internal/store"
)

func ImportDir(db *sql.DB, dir string, force ...bool) (imported, skipped int, err error) {
	forceAll := len(force) > 0 && force[0]
	err = fs.WalkDir(os.DirFS(dir), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !isFIT(path) {
			return nil
		}
		full := filepath.Join(dir, path)
		fi, err := d.Info()
		if err != nil {
			return err
		}
		hash, err := fileHash(full)
		if err != nil {
			return fmt.Errorf("%s: %w", full, err)
		}
		changed, err := store.Changed(db, hash, fi.ModTime().Unix())
		if err != nil {
			return err
		}
		if !changed && !forceAll {
			skipped++
			return nil
		}
		w, samples, err := ParseFile(full)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", full, err)
			return nil
		}
		id, err := store.InsertWorkout(db, w, hash, full, fi.ModTime().Unix())
		if err != nil {
			return err
		}
		if err := store.InsertSamples(db, id, samples); err != nil {
			return err
		}
		imported++
		return nil
	})
	return imported, skipped, err
}

func isFIT(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".fit"
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:32], nil
}

func ParseFile(path string) (*store.Workout, []store.Sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	fitf, err := fit.Decode(f)
	if err != nil {
		return nil, nil, err
	}
	activity, err := fitf.Activity()
	if err != nil {
		return nil, nil, err
	}
	if len(activity.Sessions) == 0 {
		return nil, nil, fmt.Errorf("no sessions")
	}
	s := activity.Sessions[0]

	w := &store.Workout{
		StartTime:  s.StartTime,
		Duration:   time.Duration(s.TotalTimerTime/1000) * time.Second, // scale 1000
		HasGPS:     false,
		AvgCadence: validUint8(s.AvgCadence),
		MaxCadence: validUint8(s.MaxCadence),
		AvgPower:   validUint16(s.AvgPower),
		MaxPower:   validUint16(s.MaxPower),
		NormPower:  validUint16(s.NormalizedPower),
		AvgHR:      validUint8(s.AvgHeartRate),
		MaxHR:      validUint8(s.MaxHeartRate),
		Calories:   validUint16(s.TotalCalories),
	}
	if w.Duration == 0 {
		w.Duration = time.Duration(s.TotalElapsedTime/1000) * time.Second
	}
	if s.TotalDistance != math.MaxUint32 {
		w.DistanceKm = float64(s.TotalDistance) / 100 / 1000 // scale 100, м → км
	}
	if s.AvgSpeed != math.MaxUint16 {
		w.AvgSpeedKmh = float64(s.AvgSpeed) / 1000 * 3.6 // scale 1000, м/с → км/ч
	}
	if s.EnhancedMaxSpeed != math.MaxUint32 {
		w.MaxSpeedKmh = float64(s.EnhancedMaxSpeed) / 1000 * 3.6
	} else if s.MaxSpeed != math.MaxUint16 {
		w.MaxSpeedKmh = float64(s.MaxSpeed) / 1000 * 3.6
	}
	w.AscentM = validUint16(s.TotalAscent)
	w.DescentM = validUint16(s.TotalDescent)

	w.HasPower = w.AvgPower > 0

	samples := make([]store.Sample, 0, len(activity.Records))
	var start time.Time
	minLat, maxLat := 90.0, -90.0
	minLon, maxLon := 180.0, -180.0
	for i, r := range activity.Records {
		if i == 0 || r.Timestamp.Before(start) {
			start = r.Timestamp
		}
		smp := store.Sample{
			HR: validUint8(r.HeartRate),
		}
		// дропаут датчика (нет поля) отличаем от честного 0
		smp.HasPwr = r.Power != math.MaxUint16
		if smp.HasPwr {
			smp.Power = int(r.Power)
		}
		smp.HasCad = r.Cadence != math.MaxUint8
		if smp.HasCad {
			smp.Cadence = int(r.Cadence)
		}
		// Cycplus пишет legacy-invalid 0xFFFF даже в enhanced-поля,
		// где invalid по профилю 0xFFFFFFFF — фильтруем оба маркера
		if r.EnhancedSpeed != math.MaxUint32 && r.EnhancedSpeed != math.MaxUint16 {
			smp.SpeedMs = float64(r.EnhancedSpeed) / 1000 // scale 1000
		} else if r.Speed != math.MaxUint16 {
			smp.SpeedMs = float64(r.Speed) / 1000 // scale 1000
		}
		if r.EnhancedAltitude != math.MaxUint32 && r.EnhancedAltitude != math.MaxUint16 {
			smp.AltitudeM = float64(r.EnhancedAltitude)/5 - 500 // scale 5, offset 500
			smp.HasAlt = true
		} else if r.Altitude != math.MaxUint16 {
			smp.AltitudeM = float64(r.Altitude)/5 - 500
			smp.HasAlt = true
		}
		if r.Distance != math.MaxUint32 {
			smp.DistanceM = float64(r.Distance) / 100 // scale 100
			smp.HasDist = true
		}
		if !r.PositionLat.Invalid() && !r.PositionLong.Invalid() {
			smp.Lat = r.PositionLat.Degrees()
			smp.Lon = r.PositionLong.Degrees()
			smp.HasPos = true
			w.HasGPS = true
			minLat = min(minLat, smp.Lat)
			maxLat = max(maxLat, smp.Lat)
			minLon = min(minLon, smp.Lon)
			maxLon = max(maxLon, smp.Lon)
		}
		samples = append(samples, smp)
	}
	for i := range samples {
		samples[i].Offset = int64(activity.Records[i].Timestamp.Sub(start).Seconds())
	}

	if w.HasPower {
		// среднюю считаем сами по валидным сэмплам: устройство включает
		// дропауты как нули и занижает avg
		if avg, ok := avgValid(samples, func(s store.Sample) (int, bool) { return s.Power, s.HasPwr }); ok {
			w.AvgPower = avg
		}
		// NP: Cycplus не пишет — считаем сами (Когган: скользящее 30 с → x⁴ → avg → ⁴√)
		if w.NormPower == 0 {
			w.NormPower = normalizedPower(samples)
		}
		// калории: если в файле нет — считаем из работы (кДж ≈ ккал)
		if w.Calories == 0 && w.AvgPower > 0 && w.Duration > 0 {
			w.Calories = w.AvgPower * int(w.Duration.Seconds()) / 1000
		}
	}
	if avg, ok := avgValid(samples, func(s store.Sample) (int, bool) { return s.Cadence, s.HasCad }); ok {
		w.AvgCadence = avg
	}

	// станок: нет GPS, FIT помечен как indoor cycling, либо трек меньше 1 км —
	// C2 пишет дрейфующие координаты даже на станке
	indoor := s.SubSport == fit.SubSportIndoorCycling
	stationary := w.HasGPS && trackSpan(maxLat, minLat, maxLon, minLon) < 1000
	w.Trainer = indoor || !w.HasGPS || stationary
	if w.Trainer {
		w.DistanceKm = 0
	}
	return w, samples, nil
}

// avgValid — среднее по сэмплам с валидным значением (дропауты не участвуют).
func avgValid(samples []store.Sample, get func(store.Sample) (int, bool)) (int, bool) {
	sum, n := 0, 0
	for _, s := range samples {
		if v, ok := get(s); ok {
			sum += v
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return (sum + n/2) / n, true
}

// normalizedPower считает NP по посекундным сэмплам.
// Дропауты датчика заполняем последним значением (fill-forward) —
// иначе провалы связи занижают и avg, и NP.
func normalizedPower(samples []store.Sample) int {
	const window = 30 // секунд при записи 1 Гц
	if len(samples) == 0 {
		return 0
	}
	last := 0
	for i := range samples {
		if samples[i].HasPwr {
			last = samples[i].Power
		} else {
			samples[i].Power = last
		}
	}
	var roll float64
	var sum4 float64
	for i, s := range samples {
		p := float64(s.Power)
		roll += p
		if i >= window {
			roll -= float64(samples[i-window].Power)
		}
		avg := roll / float64(min(i+1, window))
		avg2 := avg * avg
		sum4 += avg2 * avg2
	}
	return int(math.Pow(sum4/float64(len(samples)), 0.25) + 0.5)
}

// trackSpan — диагональ bounding box трека в метрах.
func trackSpan(maxLat, minLat, maxLon, minLon float64) float64 {
	if maxLat < minLat {
		return 0
	}
	midLat := (maxLat + minLat) / 2
	dx := (maxLon - minLon) * 111320 * cos(midLat*math.Pi/180)
	dy := (maxLat - minLat) * 111320
	return math.Hypot(dx, dy)
}

func cos(v float64) float64 { return math.Cos(v) }

func validUint8(v uint8) int {
	if v == math.MaxUint8 {
		return 0
	}
	return int(v)
}

func validUint16(v uint16) int {
	if v == math.MaxUint16 {
		return 0
	}
	return int(v)
}
