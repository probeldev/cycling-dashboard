// Package blesync — порт cycSync.py: вытягивание FIT-треков с велокомпа
// Cycplus C2 по BLE (Nordic UART Service, представляется как M2_XXXX).
package blesync

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"
)

const (
	// NUS: RX = запись команд, TX = нотификации с данными.
	// 6e400004 — cycplus-расширение: команды и квитанции запросов файлов.
	uartServiceUUID = "6e400001-b5a3-f393-e0a9-e50e24dcca9e"
	charCmdUUID     = "6e400004-b5a3-f393-e0a9-e50e24dcca9e" // subscribe + write команды
	charTxUUID      = "6e400003-b5a3-f393-e0a9-e50e24dcca9e" // subscribe: поток данных
	charRxUUID      = "6e400002-b5a3-f393-e0a9-e50e24dcca9e" // write: квитанции
	devicePrefix    = "M2_"
)

var (
	cmdReadPermission = []byte{0xff, 0x00, 0xff}
	cmdDiskSpace      = []byte{0x09, 0x00, 0x09}
	cmdCopy           = []byte{0x43}
	cmdCopyOK         = []byte{0x06}
	cmdCopyOKOK       = []byte{0x15}
	cmdFileList       = append([]byte{0x05}, append([]byte("filelist.txt"), 0x57)...)
)

var fitNameRe = regexp.MustCompile(`\d{14}\.fit`)

type notification struct {
	cmd  bool // пришла на 6e400004 (квитанция запроса)
	data []byte
}

// Syncer качает новые FIT-файлы с велокомпа в dir.
type Syncer struct {
	Dir  string
	Logf func(string)

	mu           sync.Mutex
	combine      bool
	replyOK      bool
	data         []byte
	fileCheck    []byte
	trigger      bool
	count        int
	notifications chan notification
}

func New(dir string, logf func(string)) *Syncer {
	return &Syncer{Dir: dir, Logf: logf, notifications: make(chan notification, 256)}
}

func (s *Syncer) log(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(fmt.Sprintf(format, args...))
	}
}

func mustParseUUID(s string) bluetooth.UUID {
	u, err := bluetooth.ParseUUID(s)
	if err != nil {
		panic(err)
	}
	return u
}

var (
	uartService = mustParseUUID(uartServiceUUID)
	charCmd     = mustParseUUID(charCmdUUID)
	charTx      = mustParseUUID(charTxUUID)
	charRx      = mustParseUUID(charRxUUID)
)

// Run выполняет полный цикл: поиск устройства, файл-лист, докачка новых FIT.
func (s *Syncer) Run() error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}

	adapter := bluetooth.DefaultAdapter
	if err := adapter.Enable(); err != nil {
		return fmt.Errorf("включение Bluetooth: %w", err)
	}

	s.log("Сканирую Bluetooth-устройства...")
	var targetAddr bluetooth.Address
	var targetName string
	scanDone := make(chan struct{})
	go func() {
		err := adapter.Scan(func(a *bluetooth.Adapter, r bluetooth.ScanResult) {
			s.log("Найдено устройство: %s (%s)", r.LocalName(), r.Address.String())
			if len(r.LocalName()) >= len(devicePrefix) && r.LocalName()[:len(devicePrefix)] == devicePrefix {
				targetAddr = r.Address
				targetName = r.LocalName()
				a.StopScan()
			}
		})
		if err != nil && targetName == "" {
			s.log("Ошибка сканирования: %v", err)
		}
		close(scanDone)
	}()
	select {
	case <-scanDone:
	case <-time.After(20 * time.Second):
		adapter.StopScan()
		<-scanDone
	}
	if targetName == "" {
		return fmt.Errorf("велокомп %s* не найден — включи его и положи рядом (телефонное приложение закрой)", devicePrefix)
	}
	s.log("Велокомп найден: %s", targetName)

	var device bluetooth.Device
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		s.log("Подключение, попытка %d/3...", attempt)
		device, err = adapter.Connect(targetAddr, bluetooth.ConnectionParams{})
		if err == nil {
			break
		}
		s.log("Не удалось подключиться: %v", err)
		if attempt < 3 {
			time.Sleep(time.Duration(5+2*attempt) * time.Second)
		}
	}
	if err != nil {
		return fmt.Errorf("подключение: %w", err)
	}
	defer device.Disconnect()
	s.log("Подключено")

	time.Sleep(5 * time.Second)

	services, err := device.DiscoverServices([]bluetooth.UUID{uartService})
	if err != nil || len(services) == 0 {
		return fmt.Errorf("сервис UART не найден: %w", err)
	}
	chars, err := services[0].DiscoverCharacteristics(nil)
	if err != nil {
		return fmt.Errorf("характеристики: %w", err)
	}
	var cmdChar, txChar, rxChar bluetooth.DeviceCharacteristic
	var haveCmd, haveTx, haveRx bool
	for i := range chars {
		switch chars[i].UUID() {
		case charCmd:
			cmdChar, haveCmd = chars[i], true
		case charTx:
			txChar, haveTx = chars[i], true
		case charRx:
			rxChar, haveRx = chars[i], true
		}
	}
	if !haveCmd || !haveTx || !haveRx {
		return fmt.Errorf("не найдены характеристики NUS (cmd=%v tx=%v rx=%v)", haveCmd, haveTx, haveRx)
	}

	// нотификации: 6e400004 — квитанции запросов, 6e400003 — поток данных
	if err := cmdChar.EnableNotifications(func(buf []byte) {
		s.onNotify(true, buf)
	}); err != nil {
		return err
	}
	if err := txChar.EnableNotifications(func(buf []byte) {
		s.onNotify(false, buf)
	}); err != nil {
		return err
	}
	defer cmdChar.EnableNotifications(nil)
	defer txChar.EnableNotifications(nil)

	writeCmd := func(b []byte) {
		if _, err := cmdChar.WriteWithoutResponse(b); err != nil {
			s.log("Ошибка записи в cmd: %v", err)
		}
	}
	writeRx := func(b []byte) {
		if _, err := rxChar.WriteWithoutResponse(b); err != nil {
			s.log("Ошибка записи в rx: %v", err)
		}
	}

	s.readDiskSpace(writeCmd)
	if err := s.getFileList(writeCmd, writeRx); err != nil {
		return err
	}

	files := s.parseFileList(filepath.Join(s.Dir, "output.txt"))
	s.log("На устройстве %d FIT-файлов", len(files))

	var newFiles []string
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(s.Dir, f)); os.IsNotExist(err) {
			newFiles = append(newFiles, f)
		}
	}
	if len(newFiles) == 0 {
		s.log("Новых файлов нет — всё уже скачано")
		return nil
	}
	s.log("Качаю %d новых файлов...", len(newFiles))

	ok, failed := 0, 0
	for i, f := range newFiles {
		s.log("Файл %d/%d: %s", i+1, len(newFiles), f)
		if s.syncFile(writeCmd, writeRx, f) {
			ok++
		} else {
			failed++
		}
		if i+1 < len(newFiles) {
			time.Sleep(2 * time.Second)
		}
	}
	s.log("Готово: %d скачано, %d не удалось", ok, failed)
	if failed > 0 {
		return fmt.Errorf("%d файлов не скачалось", failed)
	}
	return nil
}

func (s *Syncer) onNotify(cmd bool, buf []byte) {
	data := make([]byte, len(buf))
	copy(data, buf)

	s.mu.Lock()
	if cmd {
		// квитанция запроса файла: совпадает с ожидаемым → replyOK
		if len(data) > 1 && len(s.fileCheck) > 0 && string(data[:len(data)-1]) == string(s.fileCheck) {
			s.replyOK = true
		}
		s.mu.Unlock()
	} else {
		if len(data) == 1 && data[0] == 0x04 {
			s.combine = false
			s.count = 6
			s.mu.Unlock()
			return
		}
		if s.trigger && len(data) > 3 {
			data = data[3:]
			s.trigger = false
		}
		if s.combine {
			s.data = append(s.data, data...)
			s.count++
		}
		s.mu.Unlock()
	}
	select {
	case s.notifications <- notification{cmd: cmd, data: data}:
	default:
	}
}

// waitData ждёт следующей нотификации с таймаутом.
func (s *Syncer) waitData(timeout time.Duration) ([]byte, bool) {
	select {
	case n := <-s.notifications:
		return n.data, true
	case <-time.After(timeout):
		return nil, false
	}
}

// drain выбрасывает накопившиеся в канале устаревшие нотификации.
func (s *Syncer) drain() {
	for {
		select {
		case <-s.notifications:
		default:
			return
		}
	}
}

// waitACK ждёт подтверждения запроса файла, пропуская посторонние нотификации.
func (s *Syncer) waitACK(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		ok := s.replyOK
		s.mu.Unlock()
		if ok {
			return true
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			return false
		}
		if _, ok := s.waitData(remain); !ok {
			return false
		}
	}
}

func (s *Syncer) requestReadPermission(writeCmd func([]byte)) bool {
	writeCmd(cmdReadPermission)
	_, ok := s.waitData(10 * time.Second)
	return ok
}

// copyCopyOK: запрос порции данных + квитанция.
func (s *Syncer) copyCopyOK(writeRx func([]byte)) {
	writeRx(cmdCopy)
	s.waitData(10 * time.Second)
	writeRx(cmdCopyOK)
}

// copyCopyOKCombine: ждём ~5 чанков, просим следующую порцию.
// Устройство шлёт данные пачками; после каждой пачки ждёт 0x06.
// false — данные перестали приходить (таймаут), передачу надо прерывать.
func (s *Syncer) copyCopyOKCombine(writeRx func([]byte)) bool {
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.mu.Lock()
		n := s.count
		combining := s.combine
		s.mu.Unlock()
		if n > 5 || !combining {
			break
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.mu.Lock()
	s.count = 0
	if len(s.data) >= 2 {
		s.data = s.data[:len(s.data)-2]
	}
	s.trigger = true
	s.mu.Unlock()
	writeRx(cmdCopyOK)
	return true
}

func (s *Syncer) endOfTransfer(writeRx func([]byte)) {
	writeRx(cmdCopyOKOK)
	s.waitData(10 * time.Second)
	writeRx(cmdCopyOK)
	s.waitData(10 * time.Second)
}

func (s *Syncer) readDiskSpace(writeCmd func([]byte)) {
	writeCmd(cmdDiskSpace)
	if data, ok := s.waitData(10 * time.Second); ok && len(data) > 2 && data[0] == '\n' {
		s.log("Свободно на устройстве: %s кб", string(data[1:len(data)-1]))
	}
}

func (s *Syncer) getFileList(writeCmd, writeRx func([]byte)) error {
	if !s.requestReadPermission(writeCmd) {
		return fmt.Errorf("нет разрешения на чтение")
	}
	writeCmd(cmdFileList)
	s.waitData(10 * time.Second)
	s.copyCopyOK(writeRx)

	s.mu.Lock()
	s.combine = true
	s.trigger = true
	s.mu.Unlock()
	writeRx(cmdCopy)

	for s.isCombining() {
		if !s.copyCopyOKCombine(writeRx) {
			return fmt.Errorf("передача filelist.txt зависла")
		}
	}
	s.endOfTransfer(writeRx)
	time.Sleep(2 * time.Second)

	s.mu.Lock()
	data := make([]byte, len(s.data))
	copy(data, s.data)
	s.mu.Unlock()
	data = trimZeros(data)
	return os.WriteFile(filepath.Join(s.Dir, "output.txt"), data, 0o644)
}

func (s *Syncer) isCombining() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.combine
}

// syncFile качает один FIT-файл.
func (s *Syncer) syncFile(writeCmd, writeRx func([]byte), name string) bool {
	for attempt := 1; attempt <= 3; attempt++ {
		if s.syncFileOnce(writeCmd, writeRx, name) {
			return true
		}
		if attempt < 3 {
			s.log("  повтор %d/3 через 2 с...", attempt+1)
			time.Sleep(2 * time.Second)
		}
	}
	return false
}

func (s *Syncer) syncFileOnce(writeCmd, writeRx func([]byte), name string) bool {
	s.mu.Lock()
	s.combine = false
	s.replyOK = false
	s.data = nil
	s.trigger = false
	s.count = 0
	s.fileCheck = append([]byte{0x06}, []byte(name)...)
	s.mu.Unlock()
	s.drain()

	if !s.requestReadPermission(writeCmd) {
		s.log("  нет разрешения на чтение")
		return false
	}

	req := append([]byte{0x05}, []byte(name)...)
	req = append(req, 0x50)
	writeCmd(req)

	if !s.waitACK(10 * time.Second) {
		s.log("  запрос не подтверждён устройством")
		return false
	}

	// рукопожатие как в getFileList: 0x43 → первый ответ → 0x06, иначе
	// устройство ждёт 0x06, а мы ждём данные — взаимная блокировка
	s.copyCopyOK(writeRx)

	s.mu.Lock()
	s.data = nil
	s.combine = true
	s.trigger = true
	s.mu.Unlock()
	writeRx(cmdCopy)

	// цикл приёма с защитой от зависания
	iterations, stall, lastSize := 0, 0, 0
	for s.isCombining() {
		if !s.copyCopyOKCombine(writeRx) {
			s.log("  передача зависла: данные не приходят")
			return false
		}
		iterations++
		s.mu.Lock()
		size := len(s.data)
		s.mu.Unlock()
		if size > lastSize {
			lastSize = size
			stall = 0
		} else {
			stall++
		}
		if iterations > 3000 {
			s.log("  таймаут передачи (%d итераций)", iterations)
			return false
		}
		if stall > 150 {
			s.log("  передача зависла: прогресса нет %d итераций", stall)
			return false
		}
	}
	s.endOfTransfer(writeRx)

	s.mu.Lock()
	data := make([]byte, len(s.data))
	copy(data, s.data)
	s.mu.Unlock()

	if len(data) == 0 {
		s.log("  данные не получены")
		return false
	}
	data = trimZeros(data)
	path := filepath.Join(s.Dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		s.log("  ошибка записи файла: %v", err)
		return false
	}
	s.log("  скачано %d байт", len(data))
	return true
}

// parseFileList достаёт имена FIT-файлов из filelist.txt (формат: 14 цифр + .fit).
func (s *Syncer) parseFileList(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		s.log("Не прочитался filelist.txt: %v", err)
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range fitNameRe.FindAll(raw, -1) {
		name := string(m)
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func trimZeros(data []byte) []byte {
	for len(data) > 0 && data[len(data)-1] == 0x00 {
		data = data[:len(data)-1]
	}
	return data
}
