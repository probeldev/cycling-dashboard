package importer

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
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

	"cycling-dashboard/internal/ridedata"
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
		// штампуем вес и FTP на дату тренировки — зоны считаются от них.
		// FTP зависит от типа, а тип — от пользовательского override: при
		// ре-импорте FIT не должен перебивать выбор, сделанный пользователем.
		trainer := w.Trainer
		if ov, err := store.TrainerOverrideByHash(db, hash); err != nil {
			return err
		} else if ov != nil {
			trainer = *ov
		}
		if ftp, kind, ok := store.FTPAt(db, trainer, w.StartTime); ok {
			w.FTP = int(ftp)
			w.FTPKind = kind
		}
		if kg, ok := store.ProfileAt(db, "weight", w.StartTime); ok {
			w.WeightKg = kg
		}
		applyMetrics(w, samples, w.ElapsedSec, w.WorkKJ)
		if err := importWorkout(db, w, samples, hash, full, fi.ModTime().Unix()); err != nil {
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
// Если файла нет на диске, строку всё равно помечаем проверенной: иначе
// предупреждение и попытка чтения повторялись бы каждый старт. Вернувшийся
// файл подхватит `-reimport`.
func BackfillSubSport(db *sql.DB) (int, error) {
	refs, err := store.MissingSubSport(db)
	if err != nil {
		return 0, err
	}
	var filled int
	for _, ref := range refs {
		activity, err := readActivity(ref.Path)
		if err != nil {
			// старт сервера из-за одного файла не падаем
			fmt.Fprintf(os.Stderr, "бэкфилл subsport %s: %v\n", ref.Path, err)
			if errors.Is(err, fs.ErrNotExist) {
				// Файла нет — помечаем проверенным, чтобы не читать каждый старт,
				// но значение subsport не трогаем. Если нет и каталога, это похоже
				// на неверный -dir: тогда не помечаем, пусть попробует в следующий раз.
				if _, dirErr := os.Stat(filepath.Dir(ref.Path)); dirErr == nil {
					if err := store.MarkSubSportChecked(db, ref.ID); err != nil {
						return filled, err
					}
				}
			}
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
// device-NP затирать нулём нельзя, а посчитать нечего. Отдельно пропускаем
// случай «все мощности нулевые, а устройство отдало NP» — это похоже на
// залипший датчик, и стирать device-NP нулём нельзя. Такие строки остаются
// с прежней np_version и попадут в следующий прогон.
func BackfillNormalizedPower(db *sql.DB) (done, skipped int, err error) {
	works, err := store.WorkoutsNeedingNP(db, impNPVersion)
	if err != nil {
		return 0, 0, err
	}
	var updates []store.NormPowerUpdate
	for _, w := range works {
		total, nonZero, err := store.PowerSampleStats(db, w.ID)
		if err != nil {
			return done, skipped, err
		}
		if total == 0 || (nonZero == 0 && w.NormPower > 0) {
			skipped++
			continue
		}
		samples, err := store.RawSamples(db, w.ID)
		if err != nil {
			return done, skipped, err
		}
		updates = append(updates, store.NormPowerUpdate{
			ID: w.ID, NP: normalizedPower(samples), Version: impNPVersion,
		})
	}
	if err := store.SetNormPowers(db, updates); err != nil {
		return done, skipped, err
	}
	return len(updates), skipped, nil
}

// importWorkout пишет тренировку, сэмплы, зоны и MMP одной транзакцией:
// сбой на любом шаге не оставляет старые рекорды/зоны рядом с новыми сэмплами.
func importWorkout(db *sql.DB, w *store.Workout, samples []store.Sample, hash, path string, mtime int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id, err := store.InsertWorkout(tx, w, hash, path, mtime)
	if err != nil {
		return err
	}
	if err := store.InsertSamples(tx, id, samples); err != nil {
		return err
	}
	if err := store.SetWorkoutZones(tx, id, zonesOf(samples, w.FTP), zonesVersion); err != nil {
		return err
	}
	if err := store.SetWorkoutMMP(tx, id, mmpRecords(samples), mmpVersion); err != nil {
		return err
	}
	return tx.Commit()
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

// sessionTotals — общее время и механическая работа из FIT-сессии.
// elapsedSec=0 — TotalElapsedTime не записан (invalid-маркер).
// workKJ=0 — работа не записана или действительно нулевая; отдавать ли ноль
// или NULL, решает вызывающий по сэмплам (HasWork).
func sessionTotals(s *fit.SessionMsg) (elapsedSec int, workKJ float64) {
	if s.TotalElapsedTime != math.MaxUint32 {
		elapsedSec = int(s.TotalElapsedTime / 1000) // scale 1000, мс → с
	}
	// FIT total_work — в джоулях; в кДж
	if s.TotalWork != math.MaxUint32 && s.TotalWork > 0 {
		workKJ = float64(s.TotalWork) / 1000
	}
	return elapsedSec, workKJ
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
	// 0xFFFFFFFF в таймерах — invalid; без проверки duration стал бы ~49.7 дней
	// и TSS взорвался бы
	if s.TotalTimerTime != math.MaxUint32 {
		w.Duration = time.Duration(s.TotalTimerTime/1000) * time.Second // scale 1000
	}
	if w.Duration == 0 && s.TotalElapsedTime != math.MaxUint32 {
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
	w.ElapsedSec, w.WorkKJ = sessionTotals(s)

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
		// температура: invalid в FIT — 0x7F
		if r.Temperature != math.MaxInt8 {
			smp.TempC = float64(r.Temperature)
			smp.HasTemp = true
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

	// impNPVersion — версия алгоритма NP. Поднимать при изменении normalizedPower:
	// тогда бэкфилл при старте пересчитает тренировки с np_version < impNPVersion.
	// 2 — паузы определяются общим правилом ridedata (дистанция/скорость/педали),
	// а не любым разрывом больше 5 с; 3 — окна взвешиваются по длительности записи;
	// 4 — среднее внутри окна тоже по времени (sum(power*Secs)/sum(Secs)).
	impNPVersion = 4
)

// normalizedPower считает NP по Коггану: скользящее среднее мощности,
// четвёртая степень, среднее по тренировке, корень четвёртой степени.
// Окно — 30 СЕКУНД по ts_offset, а не 30 сэмплов: при записи не 1 Гц
// (и после пауз) окно в сэмплах плывёт. Сэмплы должны идти по возрастанию Offset.
// Дропауты датчика заполняем последним значением (fill-forward) —
// иначе провалы связи занижают и avg, и NP. Паузы (и только они) обрывают
// окно; определяет их общее правило ridedata — то же, что у распределений.
// И окно, и итоговое среднее взвешены по длительности записи (Timings.Secs):
// при неравномерной записи среднее по сэмплам смещает NP в сторону плотно
// записанных участков.
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

	timings := ridedata.Timings(samples)
	var sum4, weight, roll, wsum float64
	start := 0 // первый сэмпл окна
	for i := range samples {
		if i > 0 && timings[i-1].Pause {
			start, roll, wsum = i, 0, 0 // авто-пауза: данные до неё в окно не тянем
		}
		for start < i && samples[i].Offset-samples[start].Offset >= npWindowSec {
			roll -= float64(pwr[start]) * timings[start].Secs
			wsum -= timings[start].Secs
			start++
		}
		roll += float64(pwr[i]) * timings[i].Secs
		wsum += timings[i].Secs
		avg := roll / wsum // средняя по времени, а не по числу сэмплов
		avg2 := avg * avg
		sum4 += avg2 * avg2 * timings[i].Secs
		weight += timings[i].Secs
	}
	return int(math.Pow(sum4/weight, 0.25) + 0.5)
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
