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

func ImportDir(db *sql.DB, dir string) (imported, skipped int, err error) {
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
		if !changed {
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

	// калории: если в файле нет — считаем из работы (кДж ≈ ккал)
	if w.Calories == 0 && w.AvgPower > 0 && w.Duration > 0 {
		w.Calories = int(w.AvgPower * int(w.Duration.Seconds()) / 1000)
	}
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
			Power:   validUint16(r.Power),
			Cadence: validUint8(r.Cadence),
			HR:      validUint8(r.HeartRate),
		}
		if r.EnhancedSpeed != math.MaxUint32 {
			smp.SpeedMs = float64(r.EnhancedSpeed) / 1000 // scale 1000
		} else if r.Speed != math.MaxUint16 {
			smp.SpeedMs = float64(r.Speed) / 1000 // scale 1000
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
