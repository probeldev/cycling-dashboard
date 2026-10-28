package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"cycling-dashboard/internal/blesync"
)

// SyncManager запускает BLE-синхронизацию в фоне и накапливает лог.
type SyncManager struct {
	mu      sync.Mutex
	running bool
	lines   []string
	started time.Time
}

func NewSyncManager() *SyncManager {
	return &SyncManager{}
}

// Start запускает синк, если не запущен. Возвращает false, если уже идёт.
func (m *SyncManager) Start(dir string, after func()) bool {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return false
	}
	m.running = true
	m.lines = nil
	m.started = time.Now()
	m.mu.Unlock()

	go func() {
		s := blesync.New(dir, func(line string) { m.logf("%s", line) })
		err := s.Run()
		m.logf("---")
		if err != nil {
			m.logf("Синхронизация завершилась с ошибкой: %v", err)
		} else {
			m.logf("Синхронизация завершена успешно")
		}
		if after != nil {
			m.logf("Обновляю базу...")
			after()
			m.logf("База обновлена")
		}
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
	}()
	return true
}

func (m *SyncManager) logf(format string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lines = append(m.lines, time.Now().Format("15:04:05")+" "+fmt.Sprintf(format, args...))
	if len(m.lines) > 300 {
		m.lines = m.lines[len(m.lines)-300:]
	}
}

type syncStatus struct {
	Running bool     `json:"running"`
	Lines   []string `json:"lines"`
}

func (m *SyncManager) status() syncStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	lines := make([]string, len(m.lines))
	copy(lines, m.lines)
	return syncStatus{Running: m.running, Lines: lines}
}

func (m *SyncManager) routes(r chi.Router, dir string, after func()) {
	r.Post("/sync", func(w http.ResponseWriter, req *http.Request) {
		m.Start(dir, after)
		w.Header().Set("HX-Trigger", "sync-started")
		w.WriteHeader(http.StatusNoContent)
	})
	r.Get("/sync/status", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(m.status())
	})
}
