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

	imported, skipped, err := importer.ImportDir(db, absDir)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("импорт: %d новых, %d без изменений\n", imported, skipped)

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
