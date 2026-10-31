package importer

import (
	"database/sql"
	"fmt"
	"math"
	"os"

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
	AvgTemp           float64 // средняя по записям с температурой
	HasTemp           bool
	HasPowerSample    bool // есть хоть одна запись с валидной мощностью
}

// sampleMetricsOf считает метрики по сэмплам. Время берём из ridedata.Timings —
// того же источника, что NP и распределения, чтобы не появилось третьей логики.
func sampleMetricsOf(samples []store.Sample) sampleMetrics {
	times := ridedata.Timings(samples)
	var sumActive, secsActive, coast, noPower, suspect, workJ, tempSum float64
	var tempN int
	var hasPower bool
	for i, s := range samples {
		if s.HasPwr {
			hasPower = true
		}
		if s.HasTemp {
			tempSum += s.TempC
			tempN++
		}
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
		HasPowerSample:    hasPower,
		CoastingSec:       int(math.Round(coast)),
		NoPowerSec:        int(math.Round(noPower)),
		SuspectDropoutSec: int(math.Round(suspect)),
		WorkKJ:            workJ / 1000,
	}
	if tempN > 0 {
		sm.AvgTemp = tempSum / float64(tempN)
		sm.HasTemp = true
	}
	if secsActive > 0 {
		sm.AvgPowerActive = int(math.Round(sumActive / secsActive))
	}
	return sm
}

// applyMetrics пересчитывает метрики нагрузки. elapsedSec/workKJ — входные
// данные из FIT-сессии (0 — неизвестны), а не результат прошлого расчёта.
// Все пересчитываемые поля сначала обнуляются: если FTP/NP сняли, значения
// прошлого расчёта не должны остаться. Делим только на положительные
// знаменатели — NaN/Inf в базу и JSON не попадут.
func applyMetrics(w *store.Workout, samples []store.Sample, elapsedSec int, workKJ float64) {
	w.IF, w.TSS, w.VI = 0, 0, 0
	w.HasIF, w.HasTSS, w.HasVI, w.HasWork = false, false, false, false
	w.AvgPowerActive, w.CoastingSec, w.NoPowerSec, w.SuspectDropoutSec = 0, 0, 0, 0
	w.ElapsedSec, w.PauseSec, w.WorkKJ = 0, 0, 0
	w.AvgTemp, w.HasTemp = 0, false
	w.MetricsVersion = 0

	sm := sampleMetricsOf(samples)
	w.AvgPowerActive = sm.AvgPowerActive
	w.CoastingSec = sm.CoastingSec
	w.NoPowerSec = sm.NoPowerSec
	w.SuspectDropoutSec = sm.SuspectDropoutSec
	if workKJ > 0 {
		w.WorkKJ = workKJ
	} else {
		w.WorkKJ = sm.WorkKJ
	}
	w.HasWork = workKJ > 0 || sm.HasPowerSample
	if sm.HasTemp {
		w.AvgTemp = math.Round(sm.AvgTemp*10) / 10
		w.HasTemp = true
	}
	// паузы — разница между общим временем и временем в движении; отрицательную не пишем
	if elapsedSec > 0 {
		w.ElapsedSec = elapsedSec
		if pause := elapsedSec - int(w.Duration.Seconds()); pause > 0 {
			w.PauseSec = pause
		}
	}
	if w.NormPower > 0 && w.FTP > 0 {
		w.IF = float64(w.NormPower) / float64(w.FTP)
		w.HasIF = true
		if secs := w.Duration.Seconds(); secs > 0 {
			w.TSS = secs * float64(w.NormPower) * w.IF / (float64(w.FTP) * 3600) * 100
			w.HasTSS = true
		}
	}
	// VI не зависит от FTP: это разброс мощности относительно средней
	if w.NormPower > 0 && w.AvgPower > 0 {
		w.VI = float64(w.NormPower) / float64(w.AvgPower)
		w.HasVI = true
	}
	w.MetricsVersion = metricsVersion
}

// BackfillMetrics пересчитывает метрики нагрузки у тренировок со старой
// версией в одной транзакции. Идемпотентен: после прогона
// metrics_version == metricsVersion, повторный вызов ничего не находит.
func BackfillMetrics(db *sql.DB) (int, error) {
	// пустую транзакцию не открываем: на каждом старте пересчитывать нечего
	refs, err := store.WorkoutsNeedingMetrics(db, metricsVersion)
	if err != nil || len(refs) == 0 {
		return 0, err
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n, err := BackfillMetricsTx(tx)
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// BackfillMetricsTx — пересчёт в переданной транзакции: правка профиля и
// зависящие от неё метрики коммитятся вместе, ошибка откатывает всё.
// Сэмплы и NP/FTP/duration уже в БД; elapsed_sec и work_kj есть только в
// FIT-сессии и дочитываются для тренировок с мощностью (если файла нет —
// останутся NULL). TSS считается от duration_sec и заполняется и без сэмплов.
func BackfillMetricsTx(tx *sql.Tx) (int, error) {
	return backfillMetrics(tx)
}

func backfillMetrics(q store.DBTX) (int, error) {
	refs, err := store.WorkoutsNeedingMetrics(q, metricsVersion)
	if err != nil {
		return 0, err
	}
	updates := make([]*store.Workout, 0, len(refs))
	for _, ref := range refs {
		w, err := store.GetWorkout(q, ref.ID)
		if err != nil {
			return 0, err
		}
		samples, err := store.RawSamples(q, ref.ID)
		if err != nil {
			return 0, err
		}
		// elapsed и работу из сэмплов не восстановить — берём из сессии, если
		// файл на месте. Для тренировок без мощности FIT не читаем: TSS им не
		// нужен, а чтение 50 файлов на каждую правку профиля — лишняя работа.
		elapsed, work := w.ElapsedSec, w.WorkKJ
		if (elapsed == 0 || work == 0) && sampleMetricsOf(samples).HasPowerSample {
			if activity, err := readActivity(ref.Path); err == nil {
				e, wk := sessionTotals(activity.Sessions[0])
				if elapsed == 0 {
					elapsed = e
				}
				if work == 0 {
					work = wk
				}
			} else if elapsed == 0 {
				// без FIT недостающие elapsed/work останутся NULL
				fmt.Fprintf(os.Stderr, "бэкфилл метрик: сессия недоступна, недостающие elapsed/work останутся NULL: %s\n", ref.Path)
			}
		}
		applyMetrics(w, samples, elapsed, work)
		// зоны зависят от FTP — обновляем вместе с метриками
		if err := store.SetWorkoutZones(q, w.ID, zonesOf(samples, w.FTP), zonesVersion); err != nil {
			return 0, err
		}
		updates = append(updates, w)
	}
	if err := store.SetWorkoutMetricsBatch(q, updates); err != nil {
		return 0, err
	}
	return len(updates), nil
}
