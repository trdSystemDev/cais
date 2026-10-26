#!/bin/sh
# Forced command da chave SSH restrita de deploy. Instalar na VPS em
# /deploy/deploy.sh e apontar no authorized_keys do usuario "deploy"
# (ver woodpecker/deploy/authorized_keys.example).
#
# Nao aceita comando livre: le so o nome do servico via $SSH_ORIGINAL_COMMAND,
# valida o formato, confere que a pasta ja foi provisionada (apps-tui/
# provision-app.sh) pra esse nome, e roda pull+up so nela.
#
# Opcionalmente aceita um docker-compose.yml novo via stdin (o pipeline faz
# `ssh deploy@host servico < docker-compose.yml`) e substitui o existente
# antes do deploy -- NUNCA mexe em secrets/. Pra so reaplicar o que ja esta
# na pasta, sem atualizar nada, mande stdin vazio: `... servico < /dev/null`.
#
# Como o conteudo do compose passa a vir do pipeline, ele e filtrado contra
# padroes que dariam escalada pro host (privileged, docker.sock, volume na
# raiz, etc) antes de ser aceito. Toda referencia "file:" de secret tambem
# e validada: so pode ser ./secrets/<nome>, relativo, sem ".." -- nunca o
# secret de outro app nem arquivo arbitrario do host.
set -eu

APPS_ROOT=/deploy/apps
SERVICE="${SSH_ORIGINAL_COMMAND:-}"

case "$SERVICE" in
  ''|*[!a-z0-9-]*)
    echo "nome de servico invalido ou vazio" >&2
    exit 1
    ;;
esac

TARGET_DIR="$APPS_ROOT/$SERVICE"

if [ ! -d "$TARGET_DIR" ]; then
  echo "servico desconhecido (rode o provisionamento primeiro): $SERVICE" >&2
  exit 1
fi

NEW_COMPOSE=$(cat)

if [ -n "$NEW_COMPOSE" ]; then
  case "$NEW_COMPOSE" in
    *"services:"*) ;;
    *)
      echo "conteudo recebido nao parece um docker-compose.yml valido" >&2
      exit 1
      ;;
  esac

  case "$NEW_COMPOSE" in
    *privileged*|*docker.sock*|*cap_add*|*"network_mode: host"*|*"network_mode:\"host\""*|*"pid: host"*|*"pid:\"host\""*|*'- /:'*|*'- "/:'*)
      echo "docker-compose.yml recusado: contem padrao perigoso (privileged/docker.sock/cap_add/network_mode host/pid host/volume na raiz)" >&2
      exit 1
      ;;
  esac

  # toda referencia "file:" (usada em secrets:) so pode apontar pra dentro
  # da propria pasta secrets/ do app, relativo, sem ".." -- nunca outro app,
  # nunca caminho absoluto do host
  BAD_FILE_REFS=$(printf '%s\n' "$NEW_COMPOSE" \
    | grep -E '^[[:space:]]*file:' \
    | sed -E 's/^[[:space:]]*file:[[:space:]]*//; s/^["'"'"']//; s/["'"'"']$//' \
    | while IFS= read -r ref; do
        case "$ref" in
          *..*) echo "$ref" ;;
          ./secrets/[a-zA-Z0-9_.-]*) ;;
          *) echo "$ref" ;;
        esac
      done)
  if [ -n "$BAD_FILE_REFS" ]; then
    echo "docker-compose.yml recusado: 'file:' de secret precisa ser ./secrets/<nome>, sem '..', encontrado:" >&2
    printf '%s\n' "$BAD_FILE_REFS" >&2
    exit 1
  fi

  printf '%s\n' "$NEW_COMPOSE" > "$TARGET_DIR/docker-compose.yml"
fi

if [ ! -f "$TARGET_DIR/docker-compose.yml" ]; then
  echo "docker-compose.yml nao existe em $TARGET_DIR (rode o provisionamento primeiro)" >&2
  exit 1
fi

cd "$TARGET_DIR"
docker compose pull
docker compose up -d
