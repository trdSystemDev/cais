package apps

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// GuideInput e o que o texto de ajuda de um app precisa saber.
type GuideInput struct {
	App string
	// Host e o dominio sugerido pro app (<app>.<dominio>), como exemplo do
	// valor de DOMINIO.
	Host string
	// Port e a porta HTTP do service web; 0 vira um exemplo.
	Port int
	// Networks sao as redes das stacks ligadas que o app pode usar, alem
	// da proxy_network (postgres_network, mysql_network, garage_network).
	Networks  []string
	Env       []string     // nomes das variaveis do app
	Secrets   []string     // nomes dos secrets do app (os dos bancos inclusive)
	Databases []DBLink     // bancos da infra ligados
	Buckets   []BucketLink // buckets do Garage ligados
}

// Guide e o texto que o cais devolve pra escrever o compose de producao do
// app: labels do Traefik, redes, variaveis e secrets. O compose e do app; o
// cais so le e roda.
func Guide(in GuideInput) string {
	port := in.Port
	if port == 0 {
		port = 3000
	}
	router := in.App
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("# Compose de producao de %s -- modelo do cais\n", in.App)
	w("#\n")
	w("# No deploy o cais baixa o commit, grava um .env na pasta do projeto com as\n")
	w("# variaveis do app e roda: docker compose -p %s build && up -d --wait\n", in.App)
	w("# Nada aqui e reescrito pelo cais: este arquivo e do repositorio.\n")
	w("#\n")
	w("# - acesso pelo dominio: labels do Traefik + proxy_network. Sem ports:\n")
	w("#   o Traefik faz HTTPS e entrega pra porta do container.\n")
	w("# - healthcheck: o deploy espera ficar healthy e troca sem queda: o novo sobe\n")
	w("#   ao lado do antigo; o cais cria /tmp/drain no antigo (o healthcheck falha,\n")
	w("#   o Traefik para de mandar requisicao) e so entao para o antigo.\n")
	w("# - se o up falhar, o cais volta o commit anterior.\n")
	w("# - o .env fica ao lado deste arquivo: ponha .env no .dockerignore pra as\n")
	w("#   variaveis nao entrarem na imagem num COPY . .\n\n")

	w("services:\n")
	w("  web:\n")
	w("    build: .                 # ou build: {context: ./app, dockerfile: Dockerfile}\n")
	w("    restart: unless-stopped\n")
	w("    env_file: .env           # todas as variaveis do app (gravadas pelo cais)\n")
	if len(in.Env) > 0 {
		w("    # ou so algumas: environment: [\"%s=${%s}\"]\n", in.Env[0], in.Env[0])
	}
	if len(in.Secrets) > 0 {
		w("    secrets:\n")
		for _, s := range in.Secrets {
			w("      - %s\n", s)
		}
	}
	w("    labels:\n")
	w("      traefik.enable: \"true\"\n")
	w("      # o dominio vem da variavel DOMINIO do app (ex: %s, ou um dominio do\n", in.Host)
	w("      # cliente apontado pra VPS): trocar e so mudar a variavel e fazer deploy.\n")
	w("      # O certificado (Let's Encrypt) sai sozinho pra qualquer dominio na regra.\n")
	w("      # Mais de um: Host(`${DOMINIO}`) || Host(`www.${DOMINIO}`)\n")
	w("      traefik.http.routers.%s.rule: Host(`${DOMINIO:?defina DOMINIO nas variaveis do app}`)\n", router)
	w("      traefik.http.routers.%s.entrypoints: websecure\n", router)
	w("      traefik.http.services.%s.loadbalancer.server.port: \"%d\"\n", router, port)
	w("      # outro service no mesmo dominio por caminho (ex: API em /api):\n")
	w("      #   traefik.http.routers.%s-api.rule: Host(`${DOMINIO}`) && PathPrefix(`/api`)\n", router)
	w("    networks:\n")
	w("      - default\n")
	w("      - proxy_network\n")
	for _, n := range in.Networks {
		w("      - %s\n", n)
	}
	w("    # troca sem queda no deploy: o test ! -f /tmp/drain tira o container antigo\n")
	w("    # do Traefik antes de parar. A imagem precisa ter wget (alpine tem; debian\n")
	w("    # slim: troque por curl -fsS) e o app responder em / (ou ajuste o caminho).\n")
	w("    healthcheck:\n")
	w("      test: [\"CMD-SHELL\", \"test ! -f /tmp/drain && wget -qO- http://127.0.0.1:%d/ || exit 1\"]\n", port)
	w("      interval: 5s           # o deploy espera interval x retries pra drenar\n")
	w("      retries: 2\n")
	w("      start_period: 10s\n")
	w("      start_interval: 1s     # o novo fica healthy em ~1s\n")
	w("    logging:                 # sem limite o log enche o disco\n")
	w("      driver: json-file\n")
	w("      options: {max-size: \"20m\", max-file: \"5\"}\n\n")

	w("networks:\n")
	w("  proxy_network:\n    external: true\n")
	for _, n := range in.Networks {
		w("  %s:\n    external: true\n", n)
	}
	if len(in.Secrets) > 0 {
		w("\n# secrets do app: arquivos gravados pelo cais em ${CAIS_SECRETS}; dentro\n")
		w("# do container ficam em /run/secrets/<nome> (o app le o arquivo)\n")
		w("secrets:\n")
		for _, s := range in.Secrets {
			w("  %s:\n    file: ${CAIS_SECRETS}/%s\n", s, s)
		}
	}

	w("\n# Bancos e storage da infra (ligados na aba Infra):\n")
	hosts := map[string]string{
		"postgres_network": "postgres:5432 (Postgres)",
		"mysql_network":    "mysql:3306 (MySQL)",
		"garage_network":   "http://garage:3900 (S3 do Garage; regiao garage)",
	}
	nets := append([]string(nil), in.Networks...)
	sort.Strings(nets)
	for _, n := range nets {
		if h, ok := hosts[n]; ok {
			w("#   %s -> %s\n", n, h)
		}
	}
	if len(nets) == 0 {
		w("#   nenhum ligado ainda\n")
	}
	for _, l := range in.Databases {
		w("#   banco %s ligado: usuario em ${CAIS_SECRETS}/%s e senha em\n", l.ID(), l.UserSecret())
		w("#   ${CAIS_SECRETS}/%s; o database tem o mesmo nome do usuario.\n", l.PassSecret())
	}
	if len(in.Databases) == 0 {
		w("# Database do app: aba Infra -> Novo banco e depois Apps -> Secrets ->\n")
		w("# Ligar banco (ou cais app db %s postgres/<nome>): o deploy grava os\n", in.App)
		w("# secrets postgres_<nome>_user e postgres_<nome>_pass.\n")
	}
	for _, l := range in.Buckets {
		w("#   bucket %s ligado: endpoint http://garage:3900, regiao garage, path-style;\n", l.Name)
		w("#   chave em ${CAIS_SECRETS}/%s e ${CAIS_SECRETS}/%s.\n", l.AccessKeySecret(), l.SecretKeySecret())
	}
	if len(in.Buckets) == 0 && slices.Contains(in.Networks, "garage_network") {
		w("# Bucket do app: aba Infra -> Garage -> Novo bucket e depois Apps -> Secrets ->\n")
		w("# Ligar bucket (ou cais app bucket %s <nome>): o deploy grava os secrets\n", in.App)
		w("# s3_<nome>_access_key e s3_<nome>_secret_key.\n")
	}
	w("#\n")
	w("# Variaveis do cais no .env: CAIS_APP, CAIS_COMMIT (commit no ar) e\n")
	w("# CAIS_SECRETS (pasta dos secrets).\n")
	w("#\n")
	w("# Segredo que so o build precisa (token de pacote privado): build secret,\n")
	w("# nao build arg (fica na imagem). No build: secrets: [npm_token], o secret\n")
	w("# do app no secrets: de cima e, no Dockerfile:\n")
	w("#   RUN --mount=type=secret,id=npm_token NPM_TOKEN=$(cat /run/secrets/npm_token) npm ci\n")
	return b.String()
}
