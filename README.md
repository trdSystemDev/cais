# cais

Painel TUI (Go + Bubble Tea/Lip Gloss, com suporte a mouse) para preparar e administrar uma VPS
que so tem Docker instalado: Traefik, bancos Postgres/MySQL compartilhados,
storage S3 com Garage e os apps -- cada um e um repositorio do GitHub com o
compose de producao pronto, que o cais baixa, builda e sobe.

Roda direto no host, pela sessao SSH: nao abre porta e nao tem login proprio.

## Arquitetura

O projeto e agnostico a interface. O `core` e o unico dono do estado e o
unico que executa acoes; as interfaces so chamam acoes e reagem ao estado.

O core roda num unico processo, o **daemon** (`cais daemon`, via systemd).
TUI, CLI e o futuro MCP sao clientes dele por um socket Unix, entao todos
enxergam o mesmo estado ao mesmo tempo.

```
   cais (TUI)     cais state/check (CLI)     MCP (futuro)
        \                   |                       /
         \------ rpc.Client (implementa core.API) -/
                            |
          /run/cais/cais.sock  (JSON-RPC 2.0, uma msg por linha)
                            |
   cais daemon:  rpc.Serve -> internal/core (estado + acoes)
                            |
          docker / secrets / databases / apps / github
```

Regras:

- **Um contrato so.** `core.API` e implementada pelo `*core.Core` (local, usado
  nos testes) e pelo `rpc.Client` (remoto). As interfaces dependem so dela.
- **Protocolo.** JSON-RPC 2.0 sobre o socket; mudancas de estado chegam como
  notificacao `state` depois de um `state.subscribe`. Metodos em
  `internal/rpc/protocol.go`.
- **Socket e root.** Nasce com permissao `0660`: so root, ou o grupo passado em
  `--group`. Quem acessa o socket controla a VPS (como o `docker.sock`).
- **Acoes nao morrem com o cliente.** Rodam com o contexto do daemon: se a TUI
  cair no meio de um deploy, o deploy termina.
- **Daemon unico.** `Listen` recusa um segundo daemon no mesmo socket e remove
  socket orfao de um daemon que morreu.

- **Estado imutavel e serializavel.** `core.State` e uma foto; quem altera e
  so o core. Tudo tem tag JSON e erros viram texto, pra o MCP devolver direto.
- **Acao publica e devolve.** Cada metodo de acao publica o novo estado pra
  todos os inscritos *e* devolve o proprio resultado (o MCP precisa da resposta
  da chamada).
- **Reatividade sem acoplamento.** `Subscribe` entrega sempre a foto mais
  recente; leitor lento perde fotos intermediarias, mas nunca trava o core. Uma
  acao disparada pelo MCP atualiza a TUI aberta sem nenhum codigo extra.
- **Nucleo sem tela.** `internal/core/arch_test.go` falha se qualquer pacote
  fora de `internal/ui` importar as libs de tela (Bubble Tea, Lip Gloss).

## Estrutura

| Caminho | Responsabilidade |
|---|---|
| `cmd/cais` | ponto de entrada: monta o core e a interface |
| `internal/core` | estado, acoes e inscricoes -- o que as interfaces usam |
| `internal/rpc` | socket do daemon: servidor, cliente (`core.API`) e protocolo |
| `internal/ui` | TUI em Bubble Tea, clicavel (so le estado e chama acoes); cores pelo fundo do terminal |
| `internal/bootstrap` | assistente da VPS vazia |
| `internal/stacks` | stacks base + `templates/` embutidos |
| `internal/docker` | checagem, SDK do Docker e `docker compose` |
| `internal/secrets` | secrets centrais e por app |
| `internal/databases` | databases/usuarios Postgres e MySQL |
| `internal/apps` | apps em `/var/lib/cais/apps/<app>`: deploy, `.env`, secrets, avisos e ajuda do compose |
| `internal/github` | GitHub App: token por repositorio, commit e tarball do codigo |
| `internal/config` | `/etc/cais/config.yaml` e os caminhos no disco |
| `assets/` | unidade systemd |

## Diretorios na VPS

| Caminho | Conteudo |
|---|---|
| `/etc/cais/config.yaml` | configuracao: dominio base, e-mail, servicos opcionais ativos |
| `/var/lib/cais/stacks/<stack>/` | compose das stacks (proxy, postgres, mysql, garage) |
| `/var/lib/cais/apps/<app>/` | `app.yaml`, `env` (variaveis), `secrets/` e o codigo: `src/` (no ar) e `src.prev` (anterior) |
| `/var/lib/cais/secrets/` | secrets centrais (senhas das instancias de banco etc.) |
| `/var/lib/cais/backups/` | backups |
| `/run/cais/cais.sock` | socket do daemon |

`/etc/cais` e `/var/lib/cais` sao criados pelo systemd (`ConfigurationDirectory`
e `StateDirectory`); `/var/lib/cais` e `0700`, so o daemon escreve nele. Os
caminhos ficam em `internal/config/paths.go` e podem ser trocados por
`CAIS_CONFIG` e `CAIS_DATA_DIR` (ex: pra testar no PC).

Secrets ficam em texto puro em `/var/lib/cais` (pasta root `0700`, arquivos
`0444`); o valor nunca aparece no estado nem em log. Backups saem
criptografados com [age](https://age-encryption.org) para uma chave publica
cuja privada fica fora da VPS. Motivos em `internal/secrets/doc.go`.

O cais gera sozinho os secrets das stacks (senhas root dos bancos, RPC
secret e token de admin do Garage) e nunca troca um que ja existe; so a chave
do GitHub App e informada.

## Configuracao

`/etc/cais/config.yaml` guarda so o que um humano decide (secrets nunca):

```yaml
domain: exemplo.com.br          # s3.<domain> e <app>.<domain> saem daqui
email: admin@exemplo.com.br     # avisos do Let's Encrypt
stacks: [postgres, mysql, garage] # opcionais: so os que usar
proxy:
  http_port: 80                 # outra porta so numa migracao
  https_port: 443
hosts:                          # opcional: troca o nome padrao
  s3: arquivos.exemplo.com.br
github:
  app_id: 123456                # GitHub App do cais (baixa o codigo dos apps)
```

Campo desconhecido e erro (pega erro de digitacao). Os compose das stacks sao
gerados de `internal/stacks/templates/*/docker-compose.yml.tmpl` com esses
valores; o proxy (Traefik) nasce sem nenhum site, e cada servico se publica
com labels `traefik.*` na `proxy_network`. O Traefik so publica o que tem
`traefik.enable=true` e le o docker por um socket-proxy somente leitura.

Sem WAF no proxy, de proposito: pra apps proprios ele gera mais falso positivo
que protecao. A protecao vem de TLS, login forte e bancos fora da rede publica.
WAF e DDoS, se precisar, ficam melhor na borda (ex: Cloudflare).

O proxy e fixo e sobe sempre. O campo `stacks` lista apenas os opcionais:
Postgres, MySQL e Garage.
Ative cada um pela aba **Infra** ou com `cais service <nome> on`; para desligar,
use `off`. So os secrets dos servicos ativos sao gerados. Cada banco fica
na propria rede (`postgres_network`, `mysql_network`),
entao um app so enxerga o banco que usa.

Tirar um servico opcional da config nao apaga nada: o proximo `setup run` derruba os
containers dela (`docker compose down`, sem `-v`) e o compose vira
`docker-compose.yml.desativada`, como registro. Os volumes, com os dados, ficam;
religar a stack traz tudo de volta. Stacks de versoes antigas do cais sao
tratadas do mesmo jeito.

Os bancos nao tem painel web: so escutam no loopback da VPS (`127.0.0.1:5432` e
`:3306`), e a administracao e por tunel SSH, com o cliente que preferir no seu
computador. Nenhuma porta nova fica aberta na internet:

```sh
ssh -N -L 5432:127.0.0.1:5432 -L 3306:127.0.0.1:3306 voce@vps
# e conecte em localhost:5432 (postgres) e localhost:3306 (root)
```

O `cais setup` mostra esse comando e onde ler as senhas root.

Cada app usa um database proprio no banco compartilhado (o compose de
producao nao declara banco; o de desenvolvimento pode). Crie na aba **Infra**
(botao **Novo banco** no card do Postgres/MySQL, ou tecla `b`) ou pelo CLI:

```sh
cais db create postgres meu_app -extensions postgis
cais db list
```

O cais cria o database e um usuario de mesmo nome com senha nova, que so
acessa esse database. A senha fica guardada no cais e nao aparece na tela;
`cais db list` mostra cada database com os secrets que o app recebe, o app
ligado e o espaco ocupado.

Depois ligue o database ao app (Apps -> Secrets -> Ligar banco, ou pelo CLI):

```sh
cais app db portal postgres/portal       # liga
cais app db portal off postgres/portal   # desliga (o database fica)
```

A cada deploy o cais le a credencial guardada e grava os secrets
`${CAIS_SECRETS}/<engine>_<database>_user` e `..._pass` (ex:
`postgres_portal_user` e `postgres_portal_pass`). O app chega no banco
por `postgres:5432` (ou `mysql:3306`) e o database tem o nome do usuario;
montar a URL de conexao e com o app. Senha nova no cais vale no proximo
deploy.

O Garage publica a API S3 em `https://s3.<domain>` (ou `hosts.s3`), com
regiao `garage` e enderecamento por caminho (path-style). O Setup prepara
o layout de no unico na primeira subida; execucoes seguintes preservam o
layout. Os volumes `garage_meta` e `garage_data` persistem ao desligar.
Esse layout nao oferece redundancia entre maquinas.

Crie os buckets na aba **Infra** (botao **Novo bucket** no card do Garage)
ou pelo CLI. Cada bucket ganha uma chave so dele (leitura, escrita e dono),
guardada no cais:

```sh
cais bucket create portal-fotos
cais bucket list                       # secrets, espaco e app de cada bucket
cais app bucket portal portal-fotos        # liga ao app
cais app bucket portal off portal-fotos    # desliga (o bucket e os arquivos ficam)
```

A cada deploy o cais grava a chave nos secrets do app
`${CAIS_SECRETS}/s3_<bucket>_access_key` e `..._secret_key` (`-` do nome vira
`_`). Dentro da `garage_network` o app usa o endpoint `http://garage:3900`,
regiao `garage` e path-style. O secret RPC e o token de admin sao internos,
nao sao chaves S3.

Os bancos nunca entram na `proxy_network`.

## Roadmap

1. Esqueleto: TUI com abas clicaveis, checagem de Docker/Compose
2. Bootstrap: templates das stacks + assistente da VPS vazia
3. Apps: repositorio do GitHub com compose de producao, deploy com volta, logs, restart
4. Bancos: database e usuario por app no Postgres/MySQL compartilhado
5. Deploy automatico no push (webhook do GitHub App)
6. Backups, releases e `install.sh`

## Uso

```sh
cais daemon        # normalmente via systemd (assets/systemd/cais.service)
cais               # abre a TUI
cais state         # estado atual em JSON
cais check         # checa o ambiente e imprime o resultado
cais service postgres on # liga o Postgres e aplica a configuracao
cais service garage off  # desliga o S3 e preserva os volumes
```

A aba **Setup** concentra dominio, e-mail, certificados, o GitHub App e hosts.
A aba **Infra** mostra o estado das stacks fixas, os botoes Ligar/Desligar
dos servicos opcionais, o passo em andamento e as instrucoes do S3.
Os cards ficam a esquerda; a direita aparecem os logs do container selecionado
(ultimas 200 linhas, atualizadas a cada 2 segundos). Clique num card ou use
↑/↓; ←/→ troca o container nas stacks com mais de um. PgUp/PgDown ou a roda
do mouse rola os logs, e End volta a acompanhar as mensagens recentes.
O plano e a aplicacao inicial tambem ficam disponiveis na TUI e pelo CLI.
Pode rodar de novo a qualquer momento: nada que existe e regenerado e stacks sem mudanca nao sao recriadas.
Um Setup disparado pelo CLI aparece ao vivo na TUI aberta, e vice-versa.

```sh
cais setup config --domain exemplo.com.br --email voce@exemplo.com.br --github-app-id <id>
cais setup secret github_app_key < chave.pem
cais setup                               # plano: o que falta e o que muda
cais setup run                           # gera secrets, grava os compose, sobe tudo
```

O `run` sobe as stacks em ordem com `docker compose up -d --wait` (cada uma
saudavel antes da proxima). O DNS de cada app (`<app>.<dominio>` ou o que o
compose dele usar) precisa apontar pra VPS, assim como `s3.<dominio>` quando o
Garage estiver ativo.

Pra testar num PC, sem DNS publico: use um dominio `.localhost` (o
systemd-resolved e os navegadores ja resolvem `*.localhost` pra 127.0.0.1; `.local`
e recusado porque e do mDNS) e `--tls selfsigned`. O cais cria uma CA local
restrita ao dominio do teste (name constraints: nao assina nada fora dele) e um
certificado com SAN pra todos os nomes; o `setup run` termina mostrando como
confiar nela (`trust anchor`). Use `CAIS_SOCKET`, `CAIS_CONFIG` e `CAIS_DATA_DIR` numa pasta
persistente (nao `/tmp`: os volumes sobrevivem ao reboot e os secrets nao).

## Apps

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
6. troca `src` por `src.next` e `up -d --wait`;
7. observa os containers por 15 segundos: reiniciando, parado com erro ou
   unhealthy conta como falha (sem healthcheck, "running" na hora do `up` nao
   garante que o app fica de pe);
8. se falhar, volta o commit anterior (`src.prev`) e sobe ele de novo.

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
dentro do codigo (some no proximo deploy).

Variaveis vao no `.env` (`env_file: .env` ou `${VAR}` no compose) e valem no
proximo deploy. Secret em arquivo e opcional, pra quem nao quer o valor no
`docker inspect`. Ponha `.env` no `.dockerignore` pra as variaveis nao entrarem
na imagem num `COPY . .` (o deploy avisa).

O GitHub App do cais e configurado uma vez na aba **Setup** (App ID e a chave:
cole o .pem ou o caminho do arquivo) ou com `cais setup config --github-app-id`
e `cais setup secret github_app_key < chave.pem`. O app precisa da permissao
*Contents: read* e estar instalado nos repositorios.

Em desenvolvimento (Windows/macOS) o socket padrao fica na pasta temporaria;
rode `cais daemon` num terminal e `cais` em outro.

## Build

```sh
make build   # gera dist/cais-linux-amd64 e dist/cais-linux-arm64
```
