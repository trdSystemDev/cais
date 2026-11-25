VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
# release reproduzivel: sem os caminhos da maquina que compilou (-trimpath) e
# com o commit gravado no binario (go version -m, cais version). O Go e o
# do toolchain do go.mod, o mesmo no PC e no GitHub Actions.
BUILDFLAGS := -trimpath -buildvcs=true

.PHONY: build build-amd64 build-arm64 test test-race lint test-docker release clean dev daemon tui cais

# Ambiente de teste no PC: socket, config e dados fora de /run, /etc e /var/lib
DEV_DIR ?= $(HOME)/.local/share/cais-teste
DEV_ENV := CAIS_SOCKET=$(DEV_DIR)/cais.sock CAIS_CONFIG=$(DEV_DIR)/etc/config.yaml CAIS_DATA_DIR=$(DEV_DIR)/data

# binario local (./cais), sempre recompilado. Estatico (CGO_ENABLED=0) como o
# do release: os apps montam ele no container pro cais run (alpine, distroless)
cais:
	CGO_ENABLED=0 go build -ldflags "-X main.version=$(VERSION)" -o cais ./cmd/cais

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

build: build-amd64 build-arm64 build-darwin

# macOS: so o cliente (cais remote, cais -r, cais mcp --remotes) no
# computador de quem administra; a VPS e Linux
build-darwin:
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build $(BUILDFLAGS) -ldflags "$(LDFLAGS)" -o dist/cais-darwin-amd64 ./cmd/cais
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(BUILDFLAGS) -ldflags "$(LDFLAGS)" -o dist/cais-darwin-arm64 ./cmd/cais

build-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(BUILDFLAGS) -ldflags "$(LDFLAGS)" -o dist/cais-linux-amd64 ./cmd/cais

build-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(BUILDFLAGS) -ldflags "$(LDFLAGS)" -o dist/cais-linux-arm64 ./cmd/cais

test:
	go test ./...

test-race:
	go test -race ./...

# lint: formatacao, vet, staticcheck e vulnerabilidades conhecidas. As
# ferramentas rodam pela versao fixa abaixo (o go baixa na primeira vez).
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.2
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.10.0

lint:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "arquivos sem gofmt:" >&2; echo "$$out" >&2; exit 1; fi
	go vet ./...
	go run $(STATICCHECK) ./...
	go run $(GOVULNCHECK) ./...

# release: cria a tag e manda pro GitHub; o GitHub Actions
# (.github/workflows/release.yml) testa, compila e publica o release, de onde
# o install.sh pelo curl baixa. Uso: make release V=v0.3.0
release:
	@case "$(V)" in v[0-9]*.[0-9]*.[0-9]*) ;; *) echo "uso: make release V=v0.3.0" >&2; exit 1 ;; esac
	@test -z "$$(git status --porcelain)" || { echo "ha mudancas sem commit" >&2; exit 1; }
	@test "$$(git rev-parse --abbrev-ref HEAD)" = main || { echo "faca o release da main" >&2; exit 1; }
	git fetch -q origin main
	@test "$$(git rev-parse HEAD)" = "$$(git rev-parse origin/main)" || { echo "a main local difere da origin/main: de push (ou pull) antes" >&2; exit 1; }
	git tag -a $(V) -m "cais $(V)"
	git push origin $(V)
	@echo "Acompanhe em https://github.com/trdSystemDev/cais/actions"

# test-docker: os testes que sobem bancos de verdade no docker (importacao
# de banco). Lento; containers e rede com sufixo aleatorio, apagados no fim.
test-docker:
	CAIS_DOCKER_TEST=1 go test ./internal/core -run Docker -v

clean:
	rm -rf dist
