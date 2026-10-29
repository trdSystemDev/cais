#!/bin/sh
# Instala (ou atualiza) o cais nesta VPS a partir deste clone do repositorio.
#
#   git clone git@github.com:trdSystemDev/cais.git && cd cais
#   sudo ./install.sh                 # instala e sobe o daemon
#   git pull && sudo ./install.sh     # atualiza
#
# Compila o binario num container golang (nao precisa de Go na VPS, so do
# Docker que o cais ja exige), instala em /usr/local/bin/cais, instala a
# unidade do systemd e sobe (ou reinicia) o daemon. Os containers das stacks
# e dos apps nao param: so o daemon reinicia.
set -eu

GO_IMAGE=golang:1.25-alpine
BIN=/usr/local/bin/cais
UNIT=/etc/systemd/system/cais.service
DROPIN=/etc/systemd/system/cais.service.d/group.conf
SOCKET=/run/cais/cais.sock

usage() {
	cat <<EOF
Uso: sudo ./install.sh [opcoes]

Instala ou atualiza o cais a partir deste clone (compila num container golang).

Opcoes:
  --group NOME      libera o cais pra quem esta no grupo NOME, sem sudo (cria o
                    grupo e coloca nele quem rodou o sudo). Quem esta no grupo
                    controla a VPS inteira, como no grupo docker.
  --binary ARQUIVO  instala esse binario em vez de compilar
  --dry-run         mostra o que faria, sem mudar nada (compila mesmo assim)
  --uninstall       remove o binario e o servico; /etc/cais, /var/lib/cais e
                    os containers ficam
  -h, --help        esta ajuda
EOF
}

die() {
	echo "install.sh: $*" >&2
	exit 1
}

say() { echo "==> $*"; }

DRY_RUN=
# run executa um comando que muda o sistema (no --dry-run, so mostra)
run() {
	if [ -n "$DRY_RUN" ]; then
		echo "    + $*"
	else
		"$@"
	fi
}

GROUP=
BINARY=
UNINSTALL=
while [ $# -gt 0 ]; do
	case "$1" in
	--group)
		[ $# -ge 2 ] || die "--group precisa do nome do grupo"
		GROUP=$2
		shift 2
		;;
	--binary)
		[ $# -ge 2 ] || die "--binary precisa do arquivo"
		BINARY=$2
		shift 2
		;;
	--dry-run)
		DRY_RUN=1
		shift
		;;
	--uninstall)
		UNINSTALL=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		usage >&2
		die "opcao desconhecida: $1"
		;;
	esac
done

SRC=$(cd "$(dirname "$0")" && pwd)

# --- checagens ---------------------------------------------------------------

[ "$(uname -s)" = Linux ] || die "o cais roda em Linux"
if [ -z "$DRY_RUN" ] && [ "$(id -u)" -ne 0 ]; then
	die "rode como root: sudo ./install.sh"
fi
command -v systemctl >/dev/null 2>&1 || die "systemd nao encontrado (o cais roda como servico do systemd)"

if [ -n "$UNINSTALL" ]; then
	say "Removendo o servico e o binario"
	if systemctl list-unit-files cais.service >/dev/null 2>&1; then
		run systemctl disable --now cais || true
	fi
	run rm -f "$BIN" "$UNIT" "$DROPIN"
	run rmdir "$(dirname "$DROPIN")" 2>/dev/null || true
	run systemctl daemon-reload
	cat <<EOF

Pronto. Ficaram, de proposito:
  /etc/cais        a configuracao
  /var/lib/cais    stacks, apps, secrets e backups (os dados)
  os containers das stacks e dos apps, que continuam no ar

Reinstalar com sudo ./install.sh retoma tudo como estava.
EOF
	exit 0
fi

command -v docker >/dev/null 2>&1 || die "Docker nao encontrado: instale o Docker antes (https://docs.docker.com/engine/install/)"
docker info >/dev/null 2>&1 || die "o Docker nao respondeu (o servico esta no ar? rode como root)"
docker compose version >/dev/null 2>&1 || die "Docker Compose v2 nao encontrado (o plugin docker compose)"
if [ ! -f "$SRC/go.mod" ] || [ ! -f "$SRC/assets/systemd/cais.service" ]; then
	die "rode o install.sh de dentro do clone do cais"
fi

# --- binario -----------------------------------------------------------------

OUT=$(mktemp -d)
trap 'rm -rf "$OUT"' EXIT

if [ -n "$BINARY" ]; then
	[ -f "$BINARY" ] || die "binario $BINARY nao existe"
	cp "$BINARY" "$OUT/cais"
	chmod 0755 "$OUT/cais"
else
	# o clone e do usuario: git como root recusa a pasta sem o safe.directory
	VERSION=$(git -c safe.directory="$SRC" -C "$SRC" describe --tags --always --dirty 2>/dev/null || echo dev)
	say "Compilando o cais $VERSION ($GO_IMAGE)"
	# a saida vai pra uma pasta temporaria: nada do root fica no clone; o
	# volume cais-gocache guarda os modulos e o cache entre atualizacoes
	docker run --rm \
		-v "$SRC":/src:ro -v "$OUT":/out -v cais-gocache:/cache \
		-e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod -e CGO_ENABLED=0 \
		-w /src "$GO_IMAGE" \
		go build -buildvcs=false -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/cais ./cmd/cais ||
		die "a compilacao falhou"
fi
"$OUT/cais" version >/dev/null 2>&1 || die "o binario nao roda nesta maquina (arquitetura errada?)"
NEW=$("$OUT/cais" version)

# --- instalacao --------------------------------------------------------------

OLD=
[ -x "$BIN" ] && OLD=$("$BIN" version 2>/dev/null || true)
if [ -n "$OLD" ]; then
	say "Atualizando: $OLD -> $NEW"
else
	say "Instalando $NEW"
fi

# troca atomica: um cais rodando nunca ve o binario pela metade
run install -m 0755 "$OUT/cais" "$BIN.new"
run mv -f "$BIN.new" "$BIN"
run install -m 0644 "$SRC/assets/systemd/cais.service" "$UNIT"

if [ -n "$GROUP" ]; then
	say "Liberando o cais pro grupo $GROUP"
	getent group "$GROUP" >/dev/null 2>&1 || run groupadd --system "$GROUP"
	if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ]; then
		run usermod -aG "$GROUP" "$SUDO_USER"
		echo "    $SUDO_USER entra no grupo $GROUP (vale no proximo login)"
	fi
	run mkdir -p "$(dirname "$DROPIN")"
	if [ -n "$DRY_RUN" ]; then
		echo "    + grava $DROPIN com: ExecStart=$BIN daemon --group $GROUP"
	else
		printf '[Service]\nExecStart=\nExecStart=%s daemon --group %s\n' "$BIN" "$GROUP" >"$DROPIN"
	fi
elif [ -f "$DROPIN" ]; then
	echo "    mantendo o grupo da instalacao anterior ($DROPIN)"
fi

run systemctl daemon-reload
if systemctl is-active --quiet cais; then
	say "Reiniciando o daemon (os containers continuam no ar)"
	run systemctl restart cais
else
	say "Ligando o daemon"
	run systemctl enable --now cais
fi

if [ -n "$DRY_RUN" ]; then
	echo
	echo "(--dry-run: nada foi mudado; o binario compilado e o $NEW)"
	exit 0
fi

# o daemon sobe em menos de um segundo; o socket e o sinal de pronto
i=0
while [ ! -S "$SOCKET" ] && [ $i -lt 30 ]; do
	sleep 0.5
	i=$((i + 1))
done
[ -S "$SOCKET" ] || die "o daemon nao subiu: veja journalctl -u cais -n 50"
"$BIN" state >/dev/null 2>&1 || die "o daemon nao respondeu: veja journalctl -u cais -n 50"

say "cais $NEW no ar"
if [ -n "$OLD" ]; then
	echo
	echo "Abra a interface com: sudo cais"
	exit 0
fi
cat <<EOF

Proximos passos (ou faca tudo pela aba Setup: sudo cais):
  sudo cais setup config --domain exemplo.com.br --email voce@exemplo.com.br --github-app-id <id>
  sudo cais setup secret github_app_key < chave-do-github-app.pem
  sudo cais setup          # o plano: o que falta e o que muda
  sudo cais setup run      # sobe o proxy e os servicos

Ajuda: cais help (e cais <comando> -h)
EOF
