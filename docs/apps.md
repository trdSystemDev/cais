# Apps

Como o cais faz deploy, o formato do compose, historico, homologacao e deploy automatico.

Um app e um repositorio do GitHub (privado) com o **compose de producao
pronto**. O cais nao analisa nem reescreve nada: baixa o commit, grava o `.env`
com as variaveis do app, faz o build e sobe. O que sobe e como e decisao do
repositorio.

```sh
cais app create meu-app dono/meu-app -compose docker-compose.producao.yml   # -branch opcional
cais app guide meu-app                       # modelo de compose: labels, redes, secrets
cais app env meu-app NODE_ENV=production DATABASE_URL=postgres://...
cais app secret meu-app jwt_secret           # le sem eco, ou do stdin
cais app deploy meu-app                      # o commit mais novo da branch
cais app deploy meu-app -ref 3f2a1c9         # ou um commit/branch
cais apps                                    # estado de todos
cais app logs meu-app meu-app-web-1
cais app history meu-app                     # ultimos deploys
cais app rollback meu-app [commit]           # volta pra um deploy anterior (sem build)
cais app restart meu-app [service]
cais app stop meu-app
cais app delete meu-app [-volumes]           # pede o nome pra confirmar; -volumes apaga os dados
```

Na TUI, aba **Apps**: **+ Novo app** com o link do repositorio; cada app tem
Geral (deploy, reiniciar, parar, repositorio/branch/compose), Variaveis,
Secrets, Ajuda (o modelo de compose) e Logs.

O deploy:

1. pergunta ao GitHub o commit da branch (ou do `-ref`);
2. baixa o tarball desse commit pelo GitHub App em `src.next` (token de uma
   hora so pro repositorio; caminhos e symlinks que saem da pasta sao recusados);
3. grava o `.env` ao lado do compose: as variaveis do app mais `CAIS_APP`,
   `CAIS_COMMIT` e `CAIS_SECRETS` (a pasta dos secrets);
4. `docker compose config`: confere o compose resolvido contra o formato do
   cais e **recusa** (antes do build, sem tocar no que esta no ar) o que
   foge dele, dizendo o motivo e o jeito certo de cada item (veja abaixo);
5. `docker compose -p <app> build --pull` com a versao atual ainda no ar;
6. troca `src` por `src.next`; os services atras do Traefik com
   `healthcheck` no compose trocam **sem queda** (abaixo) e o resto sobe com
   `up -d --wait` (so recria o que mudou);
7. observa os containers por 15 segundos: reiniciando, parado com erro ou
   unhealthy conta como falha (sem healthcheck, "running" na hora do `up` nao
   garante que o app fica de pe);
8. se falhar, volta o commit anterior (`src.prev`) e sobe ele de novo.

O deploy inteiro (download, build, troca e observacao) tem limite de 30
minutos (`cais daemon --deploy-timeout`): download ou build travado prenderia
o app pra sempre (nenhum deploy, nem o automatico, passa da trava). Passou do
limite, o deploy e cancelado, a versao anterior volta (com prazo proprio de 10
minutos, normalmente pelas imagens guardadas) e a falha conta como passageira
pro deploy automatico.

Se o daemon morre no meio da troca (queda, kill, reboot, o limite do
`TimeoutStopSec`), a marca `deploy.yaml` na pasta do app fica. Na subida, o
daemon volta a versao que estava no ar (o ultimo deploy que deu certo, em
`src` ou `src.prev`) e sobe ela, como num deploy que falha; o historico
registra o deploy como interrompido. O deploy automatico tenta o commit
interrompido mais uma vez (o commit nao teve culpa); interrompido de novo,
espera o proximo commit.

**Deploy sem queda.** Um service com `traefik.enable` e `healthcheck` no
compose nao e recriado: o cais sobe os containers novos ao lado dos antigos
(`up --no-recreate --scale <service>=2`, mesmo projeto, entao mesmos volumes,
banco e labels), espera ficarem healthy e o Traefik passar a mandar trafego
pra eles. Depois **drena** os antigos: cria `/tmp/drain` neles, espera
ficarem unhealthy e o Traefik larga-los (ele tira do balanceamento container
unhealthy, ainda vivo), e so entao para (SIGTERM, respeitando o
`stop_grace_period`) e remove.

O cais nao chuta tempo: pergunta ao Traefik. A API dele fica presa no
loopback do container do proxy (`127.0.0.1:8080`, sem porta publicada; os
apps na `proxy_network` nao alcancam) e o cais le por
`docker exec proxy-traefik wget` quais IPs cada service usa. O Traefik
relista o Docker a cada evento, um de cada vez: com o Docker lento chega a
ficar dezenas de segundos atras, e o deploy espera o que for preciso (ate 2
minutos por etapa; passou disso, segue). Sem a API (proxy de antes dela),
espera 3s fixos. Se os novos nao ficam healthy, saem e os antigos nem foram tocados.
Deploy do mesmo commit (mesma config e imagem) nao troca nada.

A drenagem precisa do healthcheck falhando com o arquivo (convencao do
[docker-rollout](https://docker-rollout.wowu.dev/container-draining.html)):

```yaml
    healthcheck:
      test: ["CMD-SHELL", "test ! -f /tmp/drain && wget -qO- http://127.0.0.1:3000/ || exit 1"]
      interval: 5s        # o deploy espera interval x retries pra drenar
      retries: 2
      start_period: 10s
      start_interval: 1s  # o novo fica healthy em ~1s
```

Por que: sem drenar, o Traefik so tira o antigo depois que ele morre e
relista o Docker; as requisicoes dessa janela vao pro IP que sumiu e ficam
presas. Medido (uma requisicao a cada 50ms): recriando, ~2-4s fora do ar;
trocando sem drenar, de 0 a 41 falhas por deploy; drenando com esperas fixas,
de 0 a 13 (o Traefik atrasava mais que a espera com a maquina carregada);
drenando com a confirmacao do Traefik, 0 em todos os deploys medidos.

- A validacao do compose avisa (sem bloquear) service do Traefik sem
  healthcheck, que e recriado como antes, e healthcheck sem a drenagem.
- `retries` baixo deixa o healthcheck sensivel (uma falha por acaso tira o
  container do Traefik ate o proximo check); alto deixa o deploy esperar mais.
- Drenagem que falha (imagem sem `touch`, healthcheck que nunca fica unhealthy
  em 2 minutos) nao derruba o deploy: o antigo para sem drenar.
- Por alguns segundos as duas versoes atendem juntas: migracao de banco tem
  que ser compativel com a versao anterior.
- Conexao longa (WebSocket, SSE) cai quando o antigo para; o cliente que
  reconecta ja acha o novo no ar.

O compose do app publica o dominio pelo Traefik e usa as redes da infra; a
**Ajuda** (`cais app guide`) gera o trecho pronto pra colar:

```yaml
services:
  web:
    build: .
    env_file: .env
    secrets: [jwt_secret]
    labels:
      traefik.enable: "true"
      traefik.http.routers.meu-app.rule: Host(`meu-app.exemplo.com.br`)
      traefik.http.routers.meu-app.entrypoints: websecure
      traefik.http.services.meu-app.loadbalancer.server.port: "3000"
    networks: [default, proxy_network, postgres_network]
networks:
  proxy_network: {external: true}
  postgres_network: {external: true}
secrets:
  jwt_secret:
    file: ${CAIS_SECRETS}/jwt_secret   # no container: /run/secrets/jwt_secret
```

O que o deploy recusa (a mensagem de cada item diz como fazer; o MCP recebe
a lista em `problems`):

- porta publicada (`ports`): o acesso e pelo dominio, via Traefik; use `expose`
  entre services;
- `docker.sock`, bind de pasta do host fora do app, `driver_opts device`:
  dados vao em volume nomeado; arquivos, no repositorio ou em secret do app;
- `secrets`/`configs`/`env_file` lendo arquivo fora do app: secret do app em
  `${CAIS_SECRETS}/<nome>`, variaveis no `.env`;
- privileged, `cap_add`, `devices`, rede/pid/ipc/uts do host ou de outro
  container, `security_opt` `unconfined`;
- `container_name`, `external_links`, rede ou volume externo/de outro projeto
  (so `proxy_network` e as redes das stacks ligadas), build de fora do
  repositorio ou com `ssh`;
- labels do Traefik sem `proxy_network`, ou router/service/middleware com nome
  que nao comeca pelo nome do app (um app nao pode pisar na rota de outro).

Avisos (nao bloqueiam): `.env` fora do `.dockerignore`, bind com escrita
dentro do codigo (some no proximo deploy), volumes do app (nao entram no
backup).

Variaveis vao no `.env` (`env_file: .env` ou `${VAR}` no compose) e valem no
proximo deploy. Secret em arquivo e opcional, pra quem nao quer o valor no
`docker inspect`. Ponha `.env` no `.dockerignore` pra as variaveis nao entrarem
na imagem num `COPY . .` (o deploy avisa).

O GitHub App do cais e configurado uma vez na aba **Setup** (App ID e a chave:
cole o .pem ou o caminho do arquivo) ou com `cais setup config --github-app-id`
e `cais setup secret github_app_key < chave.pem`. O app precisa da permissao
*Contents: read* e estar instalado nos repositorios.

## Historico e rollback

Cada app guarda os ultimos 20 deploys (quando, commit, manual/automatico/
promovido/rollback e se deu certo) e, dos ultimos 5 que deram certo, as
imagens (`<imagem>:cais-<commit>`). **Voltar** (TUI: Geral -> Historico ->
voltar, que pede confirmacao; CLI: `cais app rollback <app> [commit]`) baixa
o commit, poe as imagens guardadas no lugar sem build e sobe pela troca
normal (sem queda; se falhar, a versao no ar volta). Sem imagem guardada, faz
o build. As variaveis e os secrets sao os de agora, nao os da epoca.

Com deploy automatico ligado, a ponta da branch na hora do rollback nao sobe
sozinha (desfaria o rollback): o automatico volta no proximo commit novo, a
correcao. O rollback automatico de um deploy que falhou tambem usa as
imagens guardadas.

## Homologacao e promocao

A homologacao e um **app separado**, ligado a producao (`promote_to`): mesmo
repositorio, compose proprio (o nome que quiser, ex:
`docker-compose.homolog.yaml`) e branch propria ou a mesma da producao. Por ser
outro projeto compose, sobe, cai e faz deploy sem tocar na producao; variaveis,
secrets, bancos e buckets sao dela. As rotas do Traefik usam o nome dela
(`portal-homolog-*` e o dominio `portal-homolog.exemplo.com.br`; a Ajuda gera) e o
cais nao deixa a producao usar um nome que e da homologacao (o nome e do app
de prefixo mais longo).

```sh
cais app create portal-homolog trdSystemDev/portal-web -branch homolog \
    -compose docker-compose.homolog.yaml -promote-to portal -auto on
cais app promote portal-homolog        # sobe na producao o commit validado
```

Na TUI: Geral da producao -> **Criar homologacao**; a homologacao aparece
embaixo da producao na lista, e a Geral das duas mostra o que promover faria.

**Promover** sobe na producao o commit que esta no ar (e de pe) na
homologacao, pelo deploy normal da producao (build com a versao atual no ar e
volta automatica se falhar). A regra e a mesma pra homologacao na mesma branch
(um commit a frente) ou numa branch propria: o commit validado tem que estar no
historico da branch da producao. Assim um deploy futuro da branch nao desfaz a
promocao.

- sem merge: bloqueia e pede o merge (PR) da branch da homologacao;
- squash merge: o commit validado nao fica no historico, mas a arvore de
  arquivos da ponta da branch e igual a dele, entao sobe a ponta (mesmo codigo);
- homologacao desatualizada (sem commits que ja estao na producao) ou commit
  mais velho que o da producao: pede confirmacao (`-yes`), porque a producao
  perderia codigo;
- homologacao com o ultimo deploy falhado, parada ou com container doente:
  nao promove.

O build da producao e refeito a partir do mesmo commit (nao ha registry): com
lockfiles o resultado e o mesmo, mas `FROM` sem versao fixa pode puxar outra
imagem base. Migracao de banco nao volta com o codigo: faca migracoes
compativeis com a versao anterior.

## Deploy automatico

Cada app escolhe: **manual** ou **automatico a cada commit na branch dele**
(TUI: seletor Deploy na Geral; CLI: `cais app set <app> -auto on|off`). O daemon
consulta o GitHub a cada minuto (`cais daemon --auto-every`), sem webhook e sem
abrir porta. Um commit que falhou por culpa dele (compose recusado, build
quebrado, app que nao fica de pe) nao e tentado de novo; o proximo sim.
Falha que parece passageira (rede, registry fora do ar, limite do Docker
Hub, `ETIMEDOUT` do npm) e tentada de novo depois de 2, 10 e 30 minutos; a
Geral e o `cais apps` mostram o horario da proxima tentativa.

| Como quer trabalhar | producao | homologacao |
|---|---|---|
| tudo manual | manual | manual |
| producao direto da main | auto (main) | - |
| homolog automatica, promocao manual | manual | auto (homolog) |
| homolog automatica e producao no merge | auto (main) | auto (homolog) |

Com producao automatica, o merge na main ja sobe a producao (sem passar pelo
Promover); com homologacao na mesma branch, a producao automatica pularia a
validacao.

A consulta cabe no limite da API do GitHub (5.000 requisicoes por hora pro
app e por instalacao) mesmo com dezenas de apps:

- o token de cada repositorio (uma hora, so aquele repositorio) e guardado e
  reaproveitado ate faltar 5 minutos pra vencer: a consulta de um app e uma
  chamada so, e as do app (JWT) caem pra uma por repositorio por hora;
- a consulta manda o ETag da anterior; branch sem commit novo volta `304`,
  que nao conta no limite;
- o cais anota a cota que cada resposta informa. Com menos de 10% (minimo
  100), a rodada do automatico espera o reset e a reserva fica pros deploys
  manuais (o log diz quanto sobra e ate quando); com a cota esgotada, nem
  chama (insistir faz o GitHub bloquear por mais tempo) e o erro diz o
  horario em que volta.

---

[Voltar pra documentacao](README.md)
