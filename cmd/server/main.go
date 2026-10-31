package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"path/filepath"

	"cycling-dashboard/internal/importer"
	"cycling-dashboard/internal/store"
	"cycling-dashboard/internal/web"
)

func main() {
	dir := flag.String("dir", "tracks", "папка с .fit файлами")
	dbPath := flag.String("db", "cycling.db", "путь к SQLite")
	addr := flag.String("addr", "127.0.0.1:8080", "адрес сервера")
	reimport := flag.Bool("reimport", false, "перечитать все .fit файлы заново (после изменений парсера)")
	flag.Parse()

	absDir, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatal(err)
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	imported, skipped, err := importer.ImportDir(db, absDir, *reimport)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("импорт: %d новых, %d без изменений\n", imported, skipped)

	// subsport раньше не записывался — дочитываем из FIT для старых тренировок.
	// Идемпотентно: когда заполнять нечего, файлы не перечитываются.
	if n, err := importer.BackfillSubSport(db); err != nil {
		log.Printf("бэкфилл subsport: %v", err)
	} else if n > 0 {
		fmt.Printf("бэкфилл subsport: %d тренировок\n", n)
	}

	// NP старых тренировок посчитан алгоритмом с окном в сэмплах — пересчитываем
	// по уже сохранённым сэмплам, без перечитывания FIT.
	if n, skipped, err := importer.BackfillNormalizedPower(db); err != nil {
		log.Printf("бэкфилл NP: %v", err)
	} else if n > 0 {
		// «пропущено» печатаем только вместе с реальной работой: иначе одна и та
		// же строка про тренировки без поверметра повторялась бы каждый старт
		fmt.Printf("бэкфилл NP: %d тренировок, пропущено (нет мощности): %d\n", n, skipped)
	}

	// Метрики нагрузки (IF/TSS/VI/...) — после NP: считаются от актуального NP.
	if n, err := importer.BackfillMetrics(db); err != nil {
		log.Printf("бэкфилл метрик: %v", err)
	} else if n > 0 {
		fmt.Printf("бэкфилл метрик: %d тренировок\n", n)
	}

	// Зоны по тренировкам для старых записей (до появления workout_zones).
	if n, err := importer.BackfillZones(db); err != nil {
		log.Printf("бэкфилл зон: %v", err)
	} else if n > 0 {
		fmt.Printf("бэкфилл зон: %d тренировок\n", n)
	}

	syncMgr := web.NewSyncManager()
	h, err := web.Handler(db, syncMgr, absDir, func() {
		n, _, err := importer.ImportDir(db, absDir)
		if err != nil {
			log.Printf("импорт после синка: %v", err)
			return
		}
		fmt.Printf("импорт после синка: %d новых\n", n)
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("дашборд: http://%s\n", *addr)
	log.Fatal(http.ListenAndServe(*addr, h))
}
