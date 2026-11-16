#!/bin/sh
# Instala (ou atualiza) o cais nesta VPS, pelo release do GitHub (o
# repositorio e privado: precisa de um token so de leitura, GH_TOKEN):
#
#   read -s GH_TOKEN && export GH_TOKEN
#   curl -fsSL -H "Authorization: Bearer $GH_TOKEN" -H "Accept: application/vnd.github.raw" \
#     https://api.github.com/repos/trdSystemDev/cais/contents/install.sh |
#     sudo --preserve-env=GH_TOKEN sh -s -- --ssh
#
# O mesmo comando atualiza. Baixa o binario desta arquitetura, confere o
# SHA-256, instala em /usr/local/bin/cais, instala a unidade do systemd e
# sobe (ou reinicia) o daemon. Os containers das stacks e dos apps nao
# param: so o daemon reinicia.
set -eu

REPO=trdSystemDev/cais
# CAIS_RELEASE_API troca a API (testes, espelho)
API=${CAIS_RELEASE_API:-https://api.github.com/repos/$REPO}

BIN=/usr/local/bin/cais
UNIT=/etc/systemd/system/cais.service
DROPIN=/etc/systemd/system/cais.service.d/group.conf
SOCKET=/run/cais/cais.sock
SSH_USER=cais
SSH_WRAPPER=/usr/local/bin/cais-ssh
SSHD_DROPIN=/etc/ssh/sshd_config.d/cais.conf

usage() {
	cat <<EOF
Uso: curl ... | sudo --preserve-env=GH_TOKEN sh -s -- [opcoes]

Instala ou atualiza o cais pelo release do GitHub (GH_TOKEN: token so de
leitura do repositorio $REPO, enquanto ele for privado).

Opcoes:
  --group NOME      libera o cais pra quem esta no grupo NOME, sem sudo (cria o
                    grupo e coloca nele quem rodou o sudo). Quem esta no grupo
                    controla a VPS inteira, como no grupo docker.
  --ssh             cria o usuario cais pro ssh: "ssh cais@vps" abre a TUI,
                    "ssh cais@vps apps" roda um comando e "ssh cais@vps cais mcp"
                    e o MCP. Sem shell. Recebe as chaves SSH de quem rodou o
                    sudo (authorized_keys); implica --group cais.
  --ssh-key ARQUIVO chave publica (.pub ou authorized_keys) pro usuario cais,
                    no lugar das suas
  --version vX.Y.Z  pelo release: essa versao em vez da mais nova
  --dry-run         mostra o que faria, sem mudar nada (baixa mesmo assim)
  --uninstall       remove o binario, o servico e o usuario cais do ssh;
                    /etc/cais, /var/lib/cais e os containers ficam
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
WANT=
UNINSTALL=
SSH=
SSH_KEY=
while [ $# -gt 0 ]; do
	case "$1" in
	--group)
		[ $# -ge 2 ] || die "--group precisa do nome do grupo"
		GROUP=$2
		shift 2
		;;
	--version)
		[ $# -ge 2 ] || die "--version precisa da versao (ex: v0.3.0)"
		WANT=$2
		shift 2
		;;
	--ssh)
		SSH=1
		shift
		;;
	--ssh-key)
		[ $# -ge 2 ] || die "--ssh-key precisa do arquivo"
		SSH=1
		SSH_KEY=$2
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

# write_unit grava a unidade do systemd. Fica aqui (e nao num arquivo do
# repositorio) pra instalacao pelo curl ter tudo.
write_unit() {
	if [ -n "$DRY_RUN" ]; then
		echo "    + grava $UNIT"
		return
	fi
	cat >"$UNIT.new" <<'UNITFILE'
# Daemon do cais: dono do estado e das acoes. A TUI e o CLI sao clientes
# dele pelo socket /run/cais/cais.sock.
#
# Instalado pelo install.sh do cais. Nao edite aqui: a proxima instalacao
# sobrescreve. O --group (liberar o cais pra um grupo sem sudo) vai num
# drop-in, cais.service.d/group.conf.
[Unit]
Description=cais daemon
After=docker.service network-online.target
Wants=docker.service network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/cais daemon
Restart=on-failure
RestartSec=2
# parar/reiniciar espera os deploys e backups em andamento terminarem (o
# daemon recusa acao nova e sai quando nao sobra nenhuma). mixed manda o
# SIGTERM so pro daemon: os docker compose que ele roda nao sao cortados.
# Passado o limite, o systemd mata tudo e o daemon seguinte volta a versao
# anterior de um deploy que ficou no meio. 45min cobre o limite de um
# deploy (--deploy-timeout, 30min) mais a volta da versao anterior (10min).
KillMode=mixed
TimeoutStopSec=45min
# cria /run/cais (e apaga ao parar) com permissao so pro root
RuntimeDirectory=cais
RuntimeDirectoryMode=0755
# /etc/cais: config.yaml, editavel pelo admin
ConfigurationDirectory=cais
ConfigurationDirectoryMode=0755
# /var/lib/cais: stacks, apps, secrets e backups -- so o daemon mexe
StateDirectory=cais
StateDirectoryMode=0700

[Install]
WantedBy=multi-user.target
UNITFILE
	chmod 0644 "$UNIT.new"
	mv -f "$UNIT.new" "$UNIT"
}

# asset_url acha, na resposta JSON de um release, a URL da API do asset
# chamado $1. Sem jq nem python (a VPS pode nao ter): le o JSON caractere a
# caractere e guarda o "url" e o "name" de cada objeto do segundo nivel (os
# assets), em qualquer ordem; o "url" do uploader, mais fundo, nao conta.
asset_url() {
	awk -v want="$1" '
		{ s = s $0 "\n" }
		END {
			n = length(s); depth = 0; instr = 0; val = 0
			for (i = 1; i <= n; i++) {
				c = substr(s, i, 1)
				if (instr) {
					if (c == "\\") { tok = tok substr(s, i + 1, 1); i++; continue }
					if (c != "\"") { tok = tok c; continue }
					instr = 0
					if (!val) { key = tok; continue }
					val = 0
					if (depth == 2 && key == "url") url = tok
					if (depth == 2 && key == "name") name = tok
					continue
				}
				if (c == "\"") { instr = 1; tok = "" }
				else if (c == ":") val = 1
				else if (c == "," || c == "[") val = 0
				else if (c == "{") { depth++; val = 0; if (depth == 2) { url = ""; name = "" } }
				else if (c == "}") {
					if (depth == 2 && name == want && url ~ /\/releases\/assets\/[0-9]+$/) { print url; exit }
					depth--
				}
			}
		}
	'
}

# gh_get baixa da API do GitHub (com o token, se houver) pro arquivo $2.
gh_get() {
	if [ -n "${GH_TOKEN:-}" ]; then
		curl -fsSL --retry 3 -H "Authorization: Bearer $GH_TOKEN" -H "Accept: $3" -o "$2" "$1"
	else
		curl -fsSL --retry 3 -H "Accept: $3" -o "$2" "$1"
	fi
}

# download baixa o binario desta arquitetura do release (WANT ou o mais
# novo) e confere o SHA-256 contra o SHA256SUMS do mesmo release.
download() {
	case $(uname -m) in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) die "arquitetura $(uname -m) sem binario do cais (so amd64 e arm64)" ;;
	esac
	command -v curl >/dev/null 2>&1 || die "curl nao encontrado"
	command -v sha256sum >/dev/null 2>&1 || die "sha256sum nao encontrado"
	rel=$API/releases/latest
	[ -n "$WANT" ] && rel=$API/releases/tags/$WANT
	say "Baixando o cais ${WANT:-mais novo} ($arch) do release do GitHub"
	gh_get "$rel" "$OUT/release.json" application/vnd.github+json ||
		die "nao consegui ler o release ${WANT:-mais novo} de $REPO: confira o GH_TOKEN (token com Contents: read nesse repositorio) e se ha release publicado"
	name=cais-linux-$arch
	bin_url=$(asset_url "$name" <"$OUT/release.json")
	sums_url=$(asset_url SHA256SUMS <"$OUT/release.json")
	[ -n "$bin_url" ] && [ -n "$sums_url" ] || die "o release nao tem $name e SHA256SUMS"
	gh_get "$bin_url" "$OUT/$name" application/octet-stream || die "falhou o download de $name"
	gh_get "$sums_url" "$OUT/SHA256SUMS" application/octet-stream || die "falhou o download do SHA256SUMS"
	(cd "$OUT" && grep " $name\$" SHA256SUMS | sha256sum -c - >/dev/null) ||
		die "o SHA-256 de $name nao confere com o SHA256SUMS do release: download corrompido, nada foi instalado"
	mv "$OUT/$name" "$OUT/cais"
	chmod 0755 "$OUT/cais"
}

reload_sshd() {
	run systemctl reload ssh 2>/dev/null || run systemctl reload sshd
}

# setup_ssh cria o usuario cais, o comando forcado dele e a config do sshd.
# A config vai num arquivo do sshd_config.d e so vale pro usuario cais: antes
# de recarregar, confere com sshd -T que nada mudou pros outros usuarios.
setup_ssh() {
	sshd=$(command -v sshd || echo /usr/sbin/sshd)
	[ -x "$sshd" ] || die "--ssh: openssh-server (sshd) nao encontrado"
	grep -Eqi '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\.d/\*\.conf' /etc/ssh/sshd_config ||
		die "--ssh: o /etc/ssh/sshd_config nao inclui /etc/ssh/sshd_config.d/*.conf; ponha no inicio dele:  Include /etc/ssh/sshd_config.d/*.conf"

	say "Usuario $SSH_USER pro ssh (cai direto na TUI)"
	if ! id "$SSH_USER" >/dev/null 2>&1; then
		run useradd -m -g "$GROUP" -s /bin/sh -c "cais - TUI pelo ssh" "$SSH_USER"
		# sem senha (so chave), mas nao bloqueado: "!" barra o login por chave em sshd sem PAM
		run usermod -p '*' "$SSH_USER"
	else
		run usermod -aG "$GROUP" "$SSH_USER"
	fi

	# chaves: a do --ssh-key, ou as de quem rodou o sudo
	keys=$SSH_KEY
	if [ -z "$keys" ]; then
		keys="$(getent passwd "${SUDO_USER:-root}" | cut -d: -f6)/.ssh/authorized_keys"
	fi
	home=$(getent passwd "$SSH_USER" | cut -d: -f6)
	[ -n "$home" ] || home=/home/$SSH_USER
	if [ -f "$keys" ]; then
		if [ -n "$DRY_RUN" ]; then
			echo "    + copia as chaves de $keys pra $home/.ssh/authorized_keys"
		else
			mkdir -p "$home/.ssh"
			touch "$home/.ssh/authorized_keys"
			# so as que ainda nao estao la
			while IFS= read -r k; do
				case "$k" in "" | "#"*) continue ;; esac
				grep -qxF "$k" "$home/.ssh/authorized_keys" || echo "$k" >>"$home/.ssh/authorized_keys"
			done <"$keys"
			chown -R "$SSH_USER:$GROUP" "$home/.ssh"
			chmod 700 "$home/.ssh"
			chmod 600 "$home/.ssh/authorized_keys"
		fi
		echo "    chaves de $keys liberadas pro $SSH_USER"
	else
		echo "    AVISO: $keys nao existe: ponha a chave publica em $home/.ssh/authorized_keys"
	fi

	if [ -n "$DRY_RUN" ]; then
		echo "    + grava $SSH_WRAPPER (o comando do $SSH_USER) e $SSHD_DROPIN (ForceCommand so pro $SSH_USER)"
		echo "    + confere com sshd -t e sshd -T e recarrega o ssh"
		return
	fi
	cat >"$SSH_WRAPPER" <<'WRAPPER'
#!/bin/sh
# Comando do usuario cais no ssh (instalado pelo install.sh --ssh do cais):
#   ssh cais@vps                  abre a TUI
#   ssh cais@vps apps             um comando do cais (com ou sem "cais" na frente)
#   ssh cais@vps cais mcp         o servidor MCP (Claude Code)
# Sem shell: as palavras do comando viram argumentos do cais, sem aspas,
# variaveis nem ; (nada e interpretado).
set -f
# shellcheck disable=SC2086 # separar em palavras e o objetivo
set -- $SSH_ORIGINAL_COMMAND
[ "${1:-}" = cais ] && shift
exec /usr/local/bin/cais "$@"
WRAPPER
	chmod 0755 "$SSH_WRAPPER"

	ctx="host=localhost,addr=127.0.0.1"
	before=$("$sshd" -T -C "user=root,$ctx" 2>/dev/null) || die "--ssh: sshd -T falhou; a config atual do ssh tem erro?"
	cat >"$SSHD_DROPIN" <<SSHDCONF
# Instalado pelo install.sh do cais (--ssh). O usuario $SSH_USER so roda o
# cais: ssh $SSH_USER@vps abre a TUI. Sem shell, sem tuneis, so chave.
Match User $SSH_USER
	ForceCommand $SSH_WRAPPER
	PermitTTY yes
	PasswordAuthentication no
	KbdInteractiveAuthentication no
	AllowTcpForwarding no
	AllowStreamLocalForwarding no
	AllowAgentForwarding no
	X11Forwarding no
	PermitTunnel no
	PermitUserRC no
SSHDCONF
	chmod 0644 "$SSHD_DROPIN"
	"$sshd" -t || undo_ssh "sshd -t recusou a config"
	after=$("$sshd" -T -C "user=root,$ctx" 2>/dev/null)
	[ "$before" = "$after" ] || undo_ssh "o arquivo mudaria o ssh dos outros usuarios (o Match do sshd_config.d vazou)"
	"$sshd" -T -C "user=$SSH_USER,$ctx" 2>/dev/null | grep -qi "^forcecommand $SSH_WRAPPER" ||
		undo_ssh "o ForceCommand nao pegou no usuario $SSH_USER"
	reload_sshd
}

undo_ssh() {
	rm -f "$SSHD_DROPIN"
	die "--ssh: $1; a config do ssh nao foi mudada"
}

# --- checagens ---------------------------------------------------------------

[ "$(uname -s)" = Linux ] || die "o cais roda em Linux"
if [ -z "$DRY_RUN" ] && [ "$(id -u)" -ne 0 ]; then
	die "rode como root: ... | sudo --preserve-env=GH_TOKEN sh"
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
	if [ -f "$SSHD_DROPIN" ]; then
		# sem o ForceCommand o usuario cais teria shell: sai junto
		say "Removendo o usuario $SSH_USER do ssh"
		run rm -f "$SSHD_DROPIN" "$SSH_WRAPPER"
		reload_sshd
		if id "$SSH_USER" >/dev/null 2>&1; then
			run userdel -r "$SSH_USER" 2>/dev/null || true
		fi
	fi
	cat <<EOF

Pronto. Ficaram, de proposito:
  /etc/cais        a configuracao
  /var/lib/cais    stacks, apps, secrets e backups (os dados)
  os containers das stacks e dos apps, que continuam no ar

Reinstalar (o mesmo comando da instalacao) retoma tudo como estava.
EOF
	exit 0
fi

command -v docker >/dev/null 2>&1 || die "Docker nao encontrado: instale o Docker antes (https://docs.docker.com/engine/install/)"
docker info >/dev/null 2>&1 || die "o Docker nao respondeu (o servico esta no ar? rode como root)"
docker compose version >/dev/null 2>&1 || die "Docker Compose v2 nao encontrado (o plugin docker compose)"

# --- binario -----------------------------------------------------------------

OUT=$(mktemp -d)
trap 'rm -rf "$OUT"' EXIT

download
"$OUT/cais" version >/dev/null 2>&1 || die "o binario nao roda nesta maquina (arquitetura errada?)"
NEW=$("$OUT/cais" version)

# --- instalacao --------------------------------------------------------------

OLD=
[ -x "$BIN" ] && OLD=$("$BIN" version 2>/dev/null || true)
# mesma versao, sem opcao nova e no ar: nada a fazer (nao reinicia a toa)
if [ -n "$OLD" ] && [ "$OLD" = "$NEW" ] && [ -z "$GROUP$SSH$DRY_RUN" ] &&
	systemctl is-active --quiet cais; then
	say "$NEW ja esta instalado e no ar"
	exit 0
fi
if [ -n "$OLD" ]; then
	say "Atualizando: $OLD -> $NEW"
else
	say "Instalando $NEW"
fi

# troca atomica: um cais rodando nunca ve o binario pela metade
run install -m 0755 "$OUT/cais" "$BIN.new"
run mv -f "$BIN.new" "$BIN"
write_unit

# --ssh: o usuario cais entra pelo grupo do daemon (o da instalacao anterior,
# se houver; senao cais)
if [ -n "$SSH" ] && [ -z "$GROUP" ]; then
	if [ -f "$DROPIN" ]; then
		GROUP=$(sed -n 's/.*daemon --group \([^ ]*\).*/\1/p' "$DROPIN")
	fi
	GROUP=${GROUP:-cais}
fi

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

if [ -n "$SSH" ]; then
	setup_ssh
fi

run systemctl daemon-reload
if systemctl is-active --quiet cais; then
	say "Reiniciando o daemon (os containers continuam no ar; deploys e backups em andamento terminam antes)"
	run systemctl restart cais
else
	say "Ligando o daemon"
	run systemctl enable --now cais
fi

if [ -n "$DRY_RUN" ]; then
	echo
	echo "(--dry-run: nada foi mudado; o binario baixado e o $NEW)"
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

say "$NEW no ar"
OPEN="sudo cais"
if [ -f "$SSHD_DROPIN" ]; then
	OPEN="ssh $SSH_USER@<esta-vps>"
	echo
	echo "Do seu computador:"
	echo "  ssh $SSH_USER@<esta-vps>                abre a TUI"
	echo "  ssh $SSH_USER@<esta-vps> apps           um comando do cais"
	echo "  claude mcp add -s user cais -- ssh $SSH_USER@<esta-vps> cais mcp"
fi
if [ -n "$OLD" ]; then
	echo
	echo "Abra a interface com: $OPEN"
	exit 0
fi
cat <<EOF

Proximos passos (ou pela TUI: $OPEN, abas Setup e Infra):
  sudo cais setup config --email voce@exemplo.com.br --github-app-id <id>
  sudo cais setup secret github_app_key < chave-do-github-app.pem
  sudo cais setup          # o plano: o que falta e o que muda
  sudo cais setup run      # sobe o proxy e os servicos

Ajuda: cais help (e cais <comando> -h)
EOF
