package ridedata

import (
	"testing"

	"cycling-dashboard/internal/store"
)

// TestTimingsPedalingWithoutSpeed — ни дистанции, ни скорости нет, но мощность
// и каденс есть: это прореживание записи при движении, а не пауза.
func TestTimingsPedalingWithoutSpeed(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 120, HasPwr: true, Cadence: 90, HasCad: true},
		{Offset: 30, Power: 120, HasPwr: true, Cadence: 90, HasCad: true},
	}
	times := Timings(samples)
	if times[0].Secs != 30 || times[0].Pause {
		t.Errorf("педалирование без скорости: %.1f с, пауза=%v; хочу 30 и false",
			times[0].Secs, times[0].Pause)
	}
	if times[1].Secs != 1 {
		t.Errorf("последняя запись: %.1f с, хочу 1", times[1].Secs)
	}
}

// TestTimingsStopWithDistance — тот же разрыв, но дистанция не выросла:
// это стоянка, даже если в записи осталась мощность.
func TestTimingsStopWithDistance(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 120, HasPwr: true, DistanceM: 100, HasDist: true},
		{Offset: 30, Power: 120, HasPwr: true, DistanceM: 100, HasDist: true},
	}
	got := Timings(samples)[0]
	if got.Secs != 1 || !got.Pause {
		t.Errorf("стоянка с мощностью: %.1f с, пауза=%v; хочу 1 и true", got.Secs, got.Pause)
	}
}

// TestTimingsSmartRecording — редкая запись при движении (smart recording)
// не пауза: интервал засчитывается целиком.
func TestTimingsSmartRecording(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 200, HasPwr: true, SpeedMs: 20 / 3.6},
		{Offset: 30, Power: 200, HasPwr: true, SpeedMs: 20 / 3.6},
	}
	if got := Timings(samples)[0].Secs; got != 30 {
		t.Errorf("движение на 20 км/ч: %.1f с, хочу 30", got)
	}
}

// TestTimingsCoastSlowRecording — накат на скорости при шаге записи 10 с:
// дистанция растёт, значит ехали, и все 10 с — накат.
func TestTimingsCoastSlowRecording(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 0, HasPwr: true, SpeedMs: 15 / 3.6, DistanceM: 0, HasDist: true},
		{Offset: 10, Power: 0, HasPwr: true, SpeedMs: 15 / 3.6, DistanceM: 41.7, HasDist: true},
	}
	if got := Timings(samples)[0].Secs; got != 10 {
		t.Errorf("качение на 15 км/ч: %.1f с, хочу 10", got)
	}
}

// TestTimingsCoastStop — накат, потом стоянка: 10-секундный интервал не накат.
func TestTimingsCoastStop(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 0, HasPwr: true},
		{Offset: 10, Power: 0, HasPwr: true},
	}
	if got := Timings(samples)[0].Secs; got != 1 {
		t.Errorf("стоянка на 0 Вт: %.1f с, хочу 1", got)
	}
}

// TestTimingsStaleSpeed — как на тренировке 418: перед авто-паузой в записи
// осталась последняя «ещё ехавшая» скорость 7.5 км/ч, но дистанция не выросла.
func TestTimingsStaleSpeed(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 0, HasPwr: true, SpeedMs: 7.5 / 3.6, DistanceM: 361.74, HasDist: true},
		{Offset: 37, Power: 0, HasPwr: true, DistanceM: 361.74, HasDist: true},
	}
	if got := Timings(samples)[0].Secs; got != 1 {
		t.Errorf("стоянка со «старой» скоростью: %.1f с, хочу 1 (дистанция не выросла)", got)
	}
}

// TestTimingsMovingGapCap — ехали, но запись пропала надолго: потолок MaxGapSec.
func TestTimingsMovingGapCap(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 200, HasPwr: true, SpeedMs: 20 / 3.6},
		{Offset: 120, Power: 200, HasPwr: true, SpeedMs: 20 / 3.6},
	}
	if got := Timings(samples)[0].Secs; got != MaxGapSec {
		t.Errorf("движение с разрывом 120 с: %.1f с, хочу потолок %d", got, MaxGapSec)
	}
}

// TestTimingsDuplicateOffsets — запись 2 Гц: две записи на секунду делят
// интервал поровну.
func TestTimingsDuplicateOffsets(t *testing.T) {
	var samples []store.Sample
	for sec := 0; sec < 10; sec++ {
		samples = append(samples,
			store.Sample{Offset: int64(sec), Power: 0, HasPwr: true},
			store.Sample{Offset: int64(sec), Power: 200, HasPwr: true})
	}
	for i, got := range Timings(samples) {
		if got.Secs != 0.5 || got.Pause {
			t.Fatalf("запись %d: %.1f с, пауза=%v; хочу 0.5 и false", i, got.Secs, got.Pause)
		}
	}
}

// TestTimingsPauseFlagOnGroupEnd — флаг паузы стоит только на последней записи
// группы: по нему окно NP обрывается ровно один раз.
func TestTimingsPauseFlagOnGroupEnd(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 0, HasPwr: true},
		{Offset: 0, Power: 0, HasPwr: true}, // дубль того же offset
		{Offset: 30, Power: 200, HasPwr: true},
		{Offset: 31, Power: 200, HasPwr: true},
	}
	times := Timings(samples)
	if times[0].Pause {
		t.Error("пауза выставлена первой записи группы, а не последней")
	}
	if !times[1].Pause {
		t.Error("у последней записи группы перед паузой нет флага")
	}
	if times[2].Pause || times[3].Pause {
		t.Error("флаг паузы после стоянки не должен тянуться дальше")
	}
}

// TestTimingsNegativeDistance — дистанция уменьшилась (сбой GPS): это не стоянка,
// решают скорость и педалирование.
func TestTimingsNegativeDistance(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 200, HasPwr: true, DistanceM: 100, HasDist: true},
		{Offset: 30, Power: 200, HasPwr: true, DistanceM: 50, HasDist: true},
	}
	if got := Timings(samples)[0]; got.Pause || got.Secs != 30 {
		t.Errorf("отрицательный прирост: %.1f с, пауза=%v; хочу 30 и false", got.Secs, got.Pause)
	}
}

// TestTimingsDistanceOnOneSide — дистанция известна только с одной стороны:
// на неё не опираемся, решают скорость и педалирование.
func TestTimingsDistanceOnOneSide(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 200, HasPwr: true, DistanceM: 100, HasDist: true},
		{Offset: 30, Power: 200, HasPwr: true}, // дистанции нет
	}
	if got := Timings(samples)[0]; got.Pause || got.Secs != 30 {
		t.Errorf("дистанция с одной стороны: %.1f с, пауза=%v; хочу 30 и false", got.Secs, got.Pause)
	}
}

// TestTimingsResidualSpeedNoDistance — без дистанции остаточная скорость
// 7.5 км/ч считается движением, а не паузой.
func TestTimingsResidualSpeedNoDistance(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 0, HasPwr: true, SpeedMs: 7.5 / 3.6},
		{Offset: 10, Power: 0, HasPwr: true},
	}
	if got := Timings(samples)[0]; got.Pause || got.Secs != 10 {
		t.Errorf("остаточная скорость: %.1f с, пауза=%v; хочу 10 и false", got.Secs, got.Pause)
	}
}
