#!/usr/bin/env bash
# Roda na VPS depois de um `docker compose up -d` em proxy/ pra validar,
# de uma vez, se o bunkerweb esta roteando corretamente todos os dominios
# ja migrados (proxy/docker-compose.yml) para os backends certos.
#
# Testa via HTTPS na porta publicada do bunkerweb (8443 por padrao, antes
# do corte final pra 443) usando --resolve, pra forcar o SNI + Host a
# baterem com o dominio de verdade -- sem precisar de DNS nem alterar
# /etc/hosts. Nao valida certificado (-k): nesse estagio o objetivo e so
# confirmar que o reverse proxy chega no app certo, nao o TLS.
#
# Uso:
#   ./validate-bunkerweb.sh              # testa contra 127.0.0.1:8443
#   ./validate-bunkerweb.sh 1.2.3.4      # testa contra outro IP/porta 8443
#   ./validate-bunkerweb.sh 1.2.3.4 8443 # IP e porta explicitos
set -uo pipefail

HOST_IP="${1:-127.0.0.1}"
PORT="${2:-8443}"

# dominio -> path de teste (a maioria responde em "/"; os que nao tem
# location "/" definida usam um path que existe de verdade)
declare -A TARGETS=(
  ["api.vertex.trds.com.br"]="/"
  ["arborscan.trds.com.br"]="/"
  ["s3.arborscan.trds.com.br"]="/"
  ["automecanica.trds.com.br"]="/"
  ["crm.trds.com.br"]="/"
  ["eagles.trds.com.br"]="/"
  ["govdocs.com.br"]="/"
  ["govluz.com.br"]="/"
  ["govluz.trds.com.br"]="/"
  ["housedigital.trds.com.br"]="/"
  ["maginato.trds.com.br"]="/"
  ["neuromes.trds.com.br"]="/"
  ["nf.trds.com.br"]="/"
  ["plic.ia.br"]="/"
  ["portal.trds.com.br"]="/"
  ["sinalronda.trds.com.br"]="/"
  ["trds.com.br"]="/jenkins/"
  ["trdsite.trds.com.br"]="/"
  ["trdsystem.com.br"]="/"
  ["vinica.trds.com.br"]="/api/"
)

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

FAIL_COUNT=0
TOTAL=${#TARGETS[@]}

printf 'Validando %d dominios contra https://%s:%s (SNI/Host por --resolve)\n\n' "$TOTAL" "$HOST_IP" "$PORT"
printf '%-32s %-10s %-8s %s\n' "DOMINIO" "PATH" "STATUS" "RESULTADO"
printf '%.0s-' {1..70}; echo

for domain in "${!TARGETS[@]}"; do
  path="${TARGETS[$domain]}"

  code=$(curl -sk -o /dev/null -w '%{http_code}' \
    --connect-timeout 5 --max-time 10 \
    --resolve "${domain}:${PORT}:${HOST_IP}" \
    "https://${domain}:${PORT}${path}")
  curl_exit=$?

  if [ "$curl_exit" -ne 0 ]; then
    printf '%-32s %-10s %-8s ' "$domain" "$path" "ERR"
    printf "${RED}FALHOU (curl exit %s -- sem conexao/timeout)${NC}\n" "$curl_exit"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  elif [ "$code" = "000" ]; then
    printf '%-32s %-10s %-8s ' "$domain" "$path" "$code"
    printf "${RED}FALHOU (sem resposta)${NC}\n"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  elif [ "$code" -ge 500 ]; then
    printf '%-32s %-10s %-8s ' "$domain" "$path" "$code"
    printf "${RED}FALHOU (backend nao respondeu -- ver porta/host.docker.internal)${NC}\n"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  elif [ "$code" = "404" ]; then
    printf '%-32s %-10s %-8s ' "$domain" "$path" "$code"
    printf "${YELLOW}CONFERIR (404 -- pode ser normal ou path errado)${NC}\n"
  else
    printf '%-32s %-10s %-8s ' "$domain" "$path" "$code"
    printf "${GREEN}OK${NC}\n"
  fi
done

echo
if [ "$FAIL_COUNT" -eq 0 ]; then
  printf "${GREEN}Todos os %d dominios responderam.${NC}\n" "$TOTAL"
else
  printf "${RED}%d de %d dominios falharam.${NC}\n" "$FAIL_COUNT" "$TOTAL"
fi

exit "$FAIL_COUNT"
