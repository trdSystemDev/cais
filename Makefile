VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build build-amd64 build-arm64 test test-docker clean dev daemon tui cais

# Ambiente de teste no PC: socket, config e dados fora de /run, /etc e /var/lib
DEV_DIR ?= $(HOME)/.local/share/cais-teste
DEV_ENV := CAIS_SOCKET=$(DEV_DIR)/cais.sock CAIS_CONFIG=$(DEV_DIR)/etc/config.yaml CAIS_DATA_DIR=$(DEV_DIR)/data

# binario local (./cais), sempre recompilado
cais:
	go build -ldflags "-X main.version=$(VERSION)" -o cais ./cmd/cais

# make dev: sobe o daemon em segundo plano (log em $(DEV_DIR)/daemon.log), abre a
# TUI e, ao sair dela, para o daemon. Se ja tem um daemon de pe, so abre a TUI.
dev: cais
	@mkdir -p $(DEV_DIR)/etc $(DEV_DIR)/data
	@if $(DEV_ENV) ./cais state >/dev/null 2>&1; then \
		echo "daemon ja rodando, abrindo a TUI"; \
		$(DEV_ENV) ./cais; \
	else \
		$(DEV_ENV) ./cais daemon >> $(DEV_DIR)/daemon.log 2>&1 & pid=$$!; \
		trap 'kill $$pid 2>/dev/null; wait $$pid 2>/dev/null' EXIT INT TERM; \
		i=0; until $(DEV_ENV) ./cais state >/dev/null 2>&1; do \
			i=$$((i+1)); \
			if [ $$i -gt 50 ] || ! kill -0 $$pid 2>/dev/null; then \
				echo "daemon nao subiu; veja $(DEV_DIR)/daemon.log" >&2; tail -5 $(DEV_DIR)/daemon.log >&2; exit 1; \
			fi; \
			sleep 0.1; \
		done; \
		$(DEV_ENV) ./cais; \
	fi

# make daemon: so o daemon, em primeiro plano (log na tela)
daemon: cais
	@mkdir -p $(DEV_DIR)/etc $(DEV_DIR)/data
	$(DEV_ENV) ./cais daemon

# make tui: so a TUI, ligada a um daemon ja rodando (make daemon em outro terminal)
tui: cais
	$(DEV_ENV) ./cais

build: build-amd64 build-arm64

build-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/cais-linux-amd64 ./cmd/cais

build-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/cais-linux-arm64 ./cmd/cais

test:
	go test ./...

# test-docker: os testes que sobem bancos de verdade no docker (importacao
# de banco). Lento; containers e rede com sufixo aleatorio, apagados no fim.
test-docker:
	CAIS_DOCKER_TEST=1 go test ./internal/core -run Docker -v

clean:
	rm -rf dist
