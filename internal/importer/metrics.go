package importer

import (
	"database/sql"
	"math"

	"cycling-dashboard/internal/ridedata"
	"cycling-dashboard/internal/store"
)

// metricsVersion — версия расчёта метрик нагрузки. Поднимать при изменении
// формул: бэкфилл при старте пересчитает строки с metrics_version < metricsVersion.
const metricsVersion = 1

// sampleMetrics — метрики, которые выводятся из сэмплов и общего правила времени.
type sampleMetrics struct {
	AvgPowerActive    int     // средняя по записям с power > 0
	CoastingSec       int     // время с power == 0
	NoPowerSec        int     // время без данных поверметра (power IS NULL)
	SuspectDropoutSec int     // power == 0 при cadence > 0 — похоже на отвал датчика
	WorkKJ            float64 // Σ(power·dt)/1000
}

// sampleMetricsOf считает метрики по сэмплам. Время берём из ridedata.Timings —
// того же источника, что NP и распределения, чтобы не появилось третьей логики.
func sampleMetricsOf(samples []store.Sample) sampleMetrics {
	times := ridedata.Timings(samples)
	var sumActive, secsActive, coast, noPower, suspect, workJ float64
	for i, s := range samples {
		dt := times[i].Secs
		switch {
		case !s.HasPwr:
			noPower += dt
		case s.Power == 0:
			coast += dt
			if s.Cadence > 0 {
				suspect += dt
			}
		default:
			sumActive += float64(s.Power) * dt
			secsActive += dt
			workJ += float64(s.Power) * dt
		}
	}
	sm := sampleMetrics{
		CoastingSec:       int(math.Round(coast)),
		NoPowerSec:        int(math.Round(noPower)),
		SuspectDropoutSec: int(math.Round(suspect)),
		WorkKJ:            workJ / 1000,
	}
	if secsActive > 0 {
		sm.AvgPowerActive = int(math.Round(sumActive / secsActive))
	}
	return sm
}

// applyMetrics заполняет метрики нагрузки тренировки. Вызывается после штампов
// профиля (IF/TSS зависят от FTP). elapsed_sec и work_kj уже стоят из FIT-сессии
// (0 — неизвестно); работу при отсутствии данных считаем по сэмплам.
// Делим только на положительные знаменатели — NaN/Inf в базу и JSON не попадут.
func applyMetrics(w *store.Workout, samples []store.Sample) {
	sm := sampleMetricsOf(samples)
	w.AvgPowerActive = sm.AvgPowerActive
	w.CoastingSec = sm.CoastingSec
	w.NoPowerSec = sm.NoPowerSec
	w.SuspectDropoutSec = sm.SuspectDropoutSec
	if w.WorkKJ <= 0 {
		w.WorkKJ = sm.WorkKJ
	}
	// паузы — разница между общим временем и временем в движении; отрицательную не пишем
	if pause := w.ElapsedSec - int(w.Duration.Seconds()); w.ElapsedSec > 0 && pause > 0 {
		w.PauseSec = pause
	}
	if w.NormPower > 0 && w.FTP > 0 {
		w.IF = float64(w.NormPower) / float64(w.FTP)
		if secs := w.Duration.Seconds(); secs > 0 {
			w.TSS = secs * float64(w.NormPower) * w.IF / (float64(w.FTP) * 3600) * 100
		}
	}
	// VI не зависит от FTP: это разброс мощности относительно средней
	if w.NormPower > 0 && w.AvgPower > 0 {
		w.VI = float64(w.NormPower) / float64(w.AvgPower)
	}
	w.MetricsVersion = metricsVersion
}

// BackfillMetrics пересчитывает метрики нагрузки у тренировок со старой версией.
// Сэмплы и NP/FTP/duration уже в БД; elapsed_sec и work_kj есть только в
// FIT-сессии, поэтому для них дочитываем сессию — если файла нет, останутся NULL.
// TSS считается от duration_sec, поэтому заполняется и без сэмплов (когда NP и
// FTP известны). Идемпотентен: после прогона metrics_version == metricsVersion.
func BackfillMetrics(db *sql.DB) (int, error) {
	refs, err := store.WorkoutsNeedingMetrics(db, metricsVersion)
	if err != nil {
		return 0, err
	}
	updates := make([]*store.Workout, 0, len(refs))
	for _, ref := range refs {
		w, err := store.GetWorkout(db, ref.ID)
		if err != nil {
			return 0, err
		}
		// elapsed и работу из сэмплов не восстановить — берём из сессии, если файл на месте
		if w.ElapsedSec == 0 || w.WorkKJ == 0 {
			if activity, err := readActivity(ref.Path); err == nil {
				s := activity.Sessions[0]
				if w.ElapsedSec == 0 && s.TotalElapsedTime != math.MaxUint32 {
					w.ElapsedSec = int(s.TotalElapsedTime / 1000)
				}
				if w.WorkKJ == 0 && s.TotalWork != math.MaxUint32 {
					w.WorkKJ = float64(s.TotalWork) / 1e6
				}
			}
			// файла нет — не страшно, elapsed/work останутся NULL
		}
		samples, err := store.RawSamples(db, ref.ID)
		if err != nil {
			return 0, err
		}
		applyMetrics(w, samples)
		updates = append(updates, w)
	}
	if err := store.SetWorkoutMetricsBatch(db, updates); err != nil {
		return 0, err
	}
	return len(updates), nil
}
