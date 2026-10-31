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
	"unicode"

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
		// штампуем вес и FTP на дату тренировки — зоны считаются от них
		if ftp, ok := store.ProfileAt(db, "ftp", w.StartTime); ok {
			w.FTP = int(ftp)
		}
		if kg, ok := store.ProfileAt(db, "weight", w.StartTime); ok {
			w.WeightKg = kg
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

// BackfillSubSport дочитывает subsport из FIT-файлов тем тренировкам, у которых
// он пустой: колонка была в схеме с самого начала, но не заполнялась.
// Идемпотентен — повторный запуск не находит таких строк и файлы не перечитывает.
// Пользовательские поля (name, trainer_override) не трогает, ре-импорт не нужен.
func BackfillSubSport(db *sql.DB) (int, error) {
	refs, err := store.MissingSubSport(db)
	if err != nil {
		return 0, err
	}
	var filled int
	for _, ref := range refs {
		activity, err := readActivity(ref.Path)
		if err != nil {
			// файла могло не остаться на диске — старт сервера из-за этого не падаем
			fmt.Fprintf(os.Stderr, "бэкфилл subsport %s: %v\n", ref.Path, err)
			continue
		}
		// пишем даже пустое значение: subsport_checked = 1, повторно файл не читаем
		sub := subSportName(activity.Sessions[0].SubSport)
		if err := store.SetSubSport(db, ref.ID, sub); err != nil {
			return filled, err
		}
		if sub != "" {
			filled++
		}
	}
	return filled, nil
}

// BackfillNormalizedPower пересчитывает NP у тренировок, посчитанных старым
// алгоритмом. Сэмплы уже в БД — FIT не перечитываем, запись идёт одной
// транзакцией. Тренировки без сэмплов или без валидной мощности не трогаем:
// device-NP затирать нулём нельзя, а посчитать нечего. Они остаются с
// np_version=0 и попадут в следующий прогон, когда появятся сэмплы.
func BackfillNormalizedPower(db *sql.DB) (done, skipped int, err error) {
	ids, err := store.WorkoutsNeedingNP(db, impNPVersion)
	if err != nil {
		return 0, 0, err
	}
	var updates []store.NormPowerUpdate
	for _, id := range ids {
		samples, err := store.RawSamples(db, id)
		if err != nil {
			return done, skipped, err
		}
		if !hasPowerData(samples) {
			skipped++
			continue
		}
		updates = append(updates, store.NormPowerUpdate{
			ID: id, NP: normalizedPower(samples), Version: impNPVersion,
		})
	}
	if err := store.SetNormPowers(db, updates); err != nil {
		return done, skipped, err
	}
	return len(updates), skipped, nil
}

// hasPowerData — есть ли в сэмплах хоть одна валидная мощность.
func hasPowerData(samples []store.Sample) bool {
	for _, s := range samples {
		if s.HasPwr {
			return true
		}
	}
	return false
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

// readActivity — активность из FIT-файла. Отдельной функцией, чтобы subsport
// можно было дочитать без полного разбора записей.
func readActivity(path string) (*fit.ActivityFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fitf, err := fit.Decode(f)
	if err != nil {
		return nil, err
	}
	activity, err := fitf.Activity()
	if err != nil {
		return nil, err
	}
	if len(activity.Sessions) == 0 {
		return nil, fmt.Errorf("no sessions")
	}
	return activity, nil
}

func ParseFile(path string) (*store.Workout, []store.Sample, error) {
	activity, err := readActivity(path)
	if err != nil {
		return nil, nil, err
	}
	s := activity.Sessions[0]

	w := &store.Workout{
		StartTime:  s.StartTime,
		Duration:   time.Duration(s.TotalTimerTime/1000) * time.Second, // scale 1000
		HasGPS:     false,
		SubSport:   subSportName(s.SubSport),
		AvgCadence: validUint8(s.AvgCadence),
		MaxCadence: validUint8(s.MaxCadence),
		AvgPower:   validUint16(s.AvgPower),
		MaxPower:   validUint16(s.MaxPower),
		NormPower:  validUint16(s.NormalizedPower),
		NPVersion:  impNPVersion,
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

	for _, s := range samples {
		if s.HasPwr && s.Power > 0 {
			w.HasPower = true
			break
		}
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
	if sampleMax, ok := maxValid(samples, func(s store.Sample) (int, bool) { return s.Power, s.HasPwr }); ok && sampleMax > 0 {
		if w.MaxPower <= 0 || sampleMax <= w.MaxPower {
			w.MaxPower = sampleMax
		}
		// else: sampleMax exceeds device-reported max — keep device value
	}
	if sampleMax, ok := maxValid(samples, func(s store.Sample) (int, bool) { return s.Cadence, s.HasCad }); ok && sampleMax > 0 {
		if w.MaxCadence <= 0 || sampleMax <= w.MaxCadence {
			w.MaxCadence = sampleMax
		}
		// else: sampleMax exceeds device-reported max — keep device value
	}
	if avg, ok := avgValid(samples, func(s store.Sample) (int, bool) { return s.HR, s.HR > 0 }); ok {
		w.AvgHR = avg
	}
	if max, ok := maxValid(samples, func(s store.Sample) (int, bool) { return s.HR, s.HR > 0 }); ok && max > 0 {
		w.MaxHR = max
	}
	if sampleMaxMs, ok := maxValidFloat(samples, func(s store.Sample) (float64, bool) { return s.SpeedMs, s.SpeedMs > 0 }); ok && sampleMaxMs > 0 {
		sampleMaxKmh := sampleMaxMs * 3.6
		if w.MaxSpeedKmh <= 0 || sampleMaxKmh <= w.MaxSpeedKmh {
			w.MaxSpeedKmh = sampleMaxKmh
		}
		// else: sample max exceeds device-reported max — keep device value
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

// maxValid — максимум по сэмплам с валидным значением.
func maxValid(samples []store.Sample, get func(store.Sample) (int, bool)) (int, bool) {
	m, found := 0, false
	for _, s := range samples {
		if v, ok := get(s); ok {
			if !found || v > m {
				m = v
				found = true
			}
		}
	}
	return m, found
}

// maxValidFloat — максимум по сэмплам с валидным значением (float64).
func maxValidFloat(samples []store.Sample, get func(store.Sample) (float64, bool)) (float64, bool) {
	m, found := 0.0, false
	for _, s := range samples {
		if v, ok := get(s); ok {
			if !found || v > m {
				m = v
				found = true
			}
		}
	}
	return m, found
}

const (
	npWindowSec = 30 // скользящее окно Коггана, секунд
	npPauseSec  = 5  // разрыв между сэмплами больше — авто-пауза, окно через неё не тянем

	// impNPVersion — версия алгоритма NP. Поднимать при изменении normalizedPower:
	// тогда бэкфилл при старте пересчитает тренировки с np_version < impNPVersion.
	impNPVersion = 1
)

// normalizedPower считает NP по Коггану: скользящее среднее мощности,
// четвёртая степень, среднее по тренировке, корень четвёртой степени.
// Окно — 30 СЕКУНД по ts_offset, а не 30 сэмплов: при записи не 1 Гц
// (и после пауз) окно в сэмплах плывёт. Сэмплы должны идти по возрастанию Offset.
// Дропауты датчика заполняем последним значением (fill-forward) —
// иначе провалы связи занижают и avg, и NP.
func normalizedPower(samples []store.Sample) int {
	if len(samples) == 0 {
		return 0
	}
	// fill-forward в отдельный срез: входные сэмплы менять нельзя,
	// они ещё пойдут в базу, где дропаут обязан остаться NULL
	pwr := make([]int, len(samples))
	last := 0
	for i, s := range samples {
		if s.HasPwr {
			last = s.Power
		}
		pwr[i] = last
	}

	var sum4, roll float64
	var n int  // сэмплов в окне
	start := 0 // первый сэмпл окна
	for i := range samples {
		if i > 0 && samples[i].Offset-samples[i-1].Offset > npPauseSec {
			start, roll, n = i, 0, 0 // авто-пауза: данные до неё в окно не тянем
		}
		for start < i && samples[i].Offset-samples[start].Offset >= npWindowSec {
			roll -= float64(pwr[start])
			start++
			n--
		}
		roll += float64(pwr[i])
		n++
		avg := roll / float64(n)
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

// subSportName — строковый subsport как в FIT-профиле: IndoorCycling → indoor_cycling.
// Для invalid и неизвестных устройству значений возвращает пустую строку.
func subSportName(s fit.SubSport) string {
	if s == fit.SubSportInvalid {
		return ""
	}
	name := s.String()
	if strings.HasPrefix(name, "SubSport(") { // stringer такое значение не знает
		return ""
	}
	var b strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func validUint16(v uint16) int {
	if v == math.MaxUint16 {
		return 0
	}
	return int(v)
}
