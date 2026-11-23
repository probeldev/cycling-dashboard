# Dev-сервер cycling-dashboard: run / restart / stop в фоне.

ADDR      := 127.0.0.1:8082
PORT      := 8082
BIN       := ./bin/server
LOG       := /tmp/cycling-dashboard-8082.log
PIDFILE   := /tmp/cycling-dashboard-8082.pid
CHECK_URL := http://127.0.0.1:8082/progress

.PHONY: run restart stop build

run: build
	@if pid=$$(lsof -ti :$(PORT) 2>/dev/null); then \
		echo "сервер уже запущен (pid $$pid), используйте make restart"; \
	else \
		nohup $(BIN) -addr $(ADDR) > $(LOG) 2>&1 & echo $$! > $(PIDFILE); \
		sleep 1; \
		echo "сервер запущен: pid $$(cat $(PIDFILE)), лог: $(LOG)"; \
		curl -s -o /dev/null -w "проверка $(CHECK_URL): HTTP %{http_code}\n" $(CHECK_URL); \
	fi

build:
	@mkdir -p bin
	go build -o $(BIN) ./cmd/server

stop:
	@if [ -f $(PIDFILE) ]; then \
		pid=$$(cat $(PIDFILE)); \
		if kill -0 $$pid 2>/dev/null; then kill $$pid && echo "остановлен pid $$pid"; fi; \
		rm -f $(PIDFILE); \
	fi
	@pkill -f 'server -addr $(ADDR)' 2>/dev/null && echo "остановлены остальные инстансы на $(ADDR)"; \
	for i in 1 2 3 4 5; do lsof -ti :$(PORT) >/dev/null 2>&1 || break; sleep 0.5; done; true

restart: stop run
