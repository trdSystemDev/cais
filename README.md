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
TUI, CLI e o MCP (`cais mcp`) sao clientes dele por um socket Unix, entao todos
enxergam o mesmo estado ao mesmo tempo.

```
   cais (TUI)     cais state/check (CLI)     cais mcp (IA)
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
- **Nem com o restart do daemon.** O SIGTERM (`systemctl restart`, `install.sh`)
  fecha o socket pra acao nova e espera as em andamento (deploy manual ou
  automatico, backup, restore) terminarem e responderem; so entao o daemon
  sai. A unidade usa `KillMode=mixed` (os `docker compose` que ele roda nao
  levam o sinal) e `TimeoutStopSec=45min` (o limite de um deploy mais a volta
  da versao anterior); um segundo sinal encerra na hora.
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
| `internal/mcpserver` | servidor MCP: cada ferramenta chama uma acao da `core.API` (nada que apaga dados) |
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
do GitHub App e a da Brevo (avisos por e-mail) sao informadas.

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
notify:                         # avisos por e-mail pela Brevo; sem "to", desligados
  from: cais@exemplo.com.br     # remetente validado na conta da Brevo
  to: [ops@exemplo.com.br]
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
cais db delete postgres portal         # apaga database e usuario (desligado de todos os apps)
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
cais bucket delete portal-fotos          # apaga (vazio e desligado de todos os apps)
```

A cada deploy o cais grava a chave nos secrets do app
`${CAIS_SECRETS}/s3_<bucket>_access_key` e `..._secret_key` (`-` do nome vira
`_`). Dentro da `garage_network` o app usa o endpoint `http://garage:3900`,
regiao `garage` e path-style. O secret RPC e o token de admin sao internos,
nao sao chaves S3.

Os bancos nunca entram na `proxy_network`.

## MCP (deploy por IA)

`cais mcp` expoe o cais como servidor MCP pelo stdin/stdout. Ele roda na VPS e
o cliente MCP no seu computador chama pelo SSH: a autenticacao e a sua chave
SSH e nenhuma porta nova abre.

```sh
claude mcp add cais -- ssh cais@vps cais mcp          # instalado com --ssh
claude mcp add cais -- ssh root@vps cais mcp          # sem o --ssh
```

As ferramentas chamam as mesmas acoes da TUI e do CLI, com as mesmas regras
(o deploy recusa compose fora do formato e diz a correcao):

- ler: `status`, `infra_status`, `infra_logs`, `apps_list`,
  `app_logs`, `app_guide`, `app_env_get`, `app_secrets_list`,
  `databases_list`, `buckets_list`, `app_promote_check`;
- agir: `app_create`, `app_update`, `app_deploy`, `app_rollback`, `app_promote`, `app_restart`, `app_stop`,
  `app_env_set`, `app_secret_set`, `database_create`, `bucket_create`,
  `app_link_database`/`app_unlink_database`, `app_link_bucket`/`app_unlink_bucket`.

Nada que apaga dados nem o Setup: excluir app, banco, bucket ou secret, ligar
ou desligar servico e a config da VPS ficam so na TUI e no CLI. Valores de secret nunca sao
devolvidos (a IA ve so os nomes).

## Roadmap

1. Esqueleto: TUI com abas clicaveis, checagem de Docker/Compose
2. Bootstrap: templates das stacks + assistente da VPS vazia
3. Apps: repositorio do GitHub com compose de producao, deploy com volta, logs, restart
4. Bancos: database e usuario por app no Postgres/MySQL compartilhado
5. Deploy automatico (feito: consulta ao GitHub a cada minuto, sem webhook nem porta) e homologacao com promocao
6. Backups (feito: bancos e estado do cais, criptografados com age, pro Google Drive)
7. `install.sh` (feito: compila no clone, sem releases)
8. MCP (feito: `cais mcp` pelo SSH, sem exclusoes nem Setup)

## Instalacao

A VPS precisa de Linux com systemd, Docker com o Compose v2 e acesso de
leitura a este repositorio (chave SSH). Nao precisa de Go: o `install.sh`
compila o cais num container `golang`.

```sh
git clone git@github.com:trdSystemDev/cais.git && cd cais
sudo ./install.sh                 # compila, instala e sobe o daemon (systemd)
git pull && sudo ./install.sh     # atualiza: so o daemon reinicia, os containers ficam
```

Instala `/usr/local/bin/cais` e o servico `cais` (unidade em
`assets/systemd/cais.service`). Opcoes (`./install.sh -h`):

- `--group cais`: libera o cais pra quem esta no grupo, sem sudo (cria o grupo
  e coloca nele quem rodou o sudo). Quem esta no grupo controla a VPS, como no
  grupo `docker`.
- `--ssh`: cria o usuario `cais` pro ssh, com as chaves de quem rodou o sudo
  (ou `--ssh-key arquivo.pub`). `ssh cais@vps` abre direto a TUI,
  `ssh cais@vps apps` roda um comando do cais e `ssh cais@vps cais mcp` e o
  MCP. Sem shell, sem tunel e so por chave (a config fica em
  `/etc/ssh/sshd_config.d/cais.conf`, conferida com `sshd -T` pra nao mudar o
  ssh dos outros usuarios). Implica `--group cais`.
- `--dry-run`: mostra o que faria, sem mudar nada.
- `--binary arquivo`: instala um binario pronto em vez de compilar.
- `--uninstall`: remove o binario, o servico e o usuario `cais` do ssh;
  `/etc/cais`, `/var/lib/cais` e os containers ficam.

Depois, o Setup (aba Setup da TUI, ou o CLI abaixo).

## Uso

`cais help` lista todos os comandos. `cais <grupo> -h` mostra so os de um
grupo (`cais app -h`, `cais db -h`) e `cais <grupo> <comando> -h` explica um
comando com as flags e exemplos (`cais app deploy -h`).

```sh
cais daemon        # normalmente via systemd (assets/systemd/cais.service)
cais               # abre a TUI
cais state         # estado atual em JSON
cais check         # checa o ambiente e imprime o resultado
cais service postgres on # liga o Postgres e aplica a configuracao
cais service garage off  # desliga o S3 e preserva os volumes
```

A aba **Setup** concentra dominio, e-mail, certificados, o GitHub App, os
avisos por e-mail e hosts.
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

### Avisos por e-mail

O cais manda e-mail pela API da [Brevo](https://www.brevo.com) quando:

- um **deploy automatico falha de vez**: falha passageira com nova tentativa
  marcada (2, 10 e 30 min) ainda nao avisa; a ultima, sim;
- o **backup automatico nao da certo**: nao chegou no Drive (um aviso por
  sequencia de falhas, nao um por tentativa de hora em hora) ou subiu sem
  algum database.

Deploy e backup manuais nao avisam: quem disparou ja ve o resultado. Falha
no envio vai so pro log do daemon.

```sh
cais setup config --notify-from cais@exemplo.com.br --notify-to ops@exemplo.com.br,dev@exemplo.com.br
cais setup secret brevo_api_key          # cola a chave xkeysib-... (sem eco)
cais setup notify-test                   # manda um e-mail de teste agora
```

O remetente precisa estar validado na Brevo (Senders, Domains). `--notify-to ""`
desliga os avisos. Na TUI, os mesmos campos ficam na aba **Setup**, com o
botao **Testar e-mail**.

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

Em desenvolvimento (Windows/macOS) o socket padrao fica na pasta temporaria;
rode `cais daemon` num terminal e `cais` em outro.

### Historico e rollback

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

### Deploy automatico

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

## Backups

Todo dia no horario configurado (e no "Fazer backup agora"), o cais faz um
arquivo so com:

- o dump de cada database criado pelo cais (`pg_dump` / `mysqldump` rodando
  dentro do container do banco, com os donos das tabelas);
- o estado do cais: `/etc/cais` e `/var/lib/cais` (apps, variaveis, secrets,
  senhas dos bancos), sem o codigo dos apps (volta no deploy) e sem os
  proprios backups.

**Fora do backup, por enquanto:** os arquivos do Garage (buckets S3) e os
volumes nomeados dos apps. Perder a VPS (ou o disco) perde esses dados. O
painel e o `cais backup status` dizem isso, o card do Garage e o `cais bucket`
lembram, e o deploy avisa (sem bloquear) quando o compose do app declara
volumes. Dado que nao pode se perder vai no banco da infra, ou precisa de um
backup proprio do app.

O arquivo (`cais-<dominio>-<data>.tar.gz.age`) e criptografado com
[age](https://age-encryption.org) pra uma chave publica e sobe pra uma pasta
`cais-backups-<dominio>` no seu Google Drive. A chave privada aparece uma vez
quando e gerada e **nao fica na VPS**: quem invade a VPS nao abre os backups
que ja estao no Drive. Guarde num gerenciador de senhas; sem ela nenhum
backup abre. Os mais velhos que a retencao (padrao 30 dias) saem do Drive; o
mais novo fica sempre. Um database que falha no dump nao impede os outros.

Configuracao (TUI: Infra -> Backups; ou o CLI):

1. No [Google Cloud](https://console.cloud.google.com): crie um projeto,
   ative a **Google Drive API**, configure a tela de consentimento e
   **publique** (em "Teste" o login expira em 7 dias) e crie em Credenciais
   um **ID do cliente OAuth** do tipo **TVs e dispositivos de entrada
   limitada**. O escopo e `drive.file`: o cais so ve a pasta que ele cria.
2. `cais backup client <client-id>` (o secret e pedido sem eco).
3. `cais backup login`: abra google.com/device em qualquer aparelho e digite
   o codigo (funciona pelo SSH, nada volta colado).
4. `cais backup key`: guarde a chave privada que aparece.
5. `cais backup schedule 03:00 -days 30` (fuso da VPS). Se a VPS estava
   desligada na hora, o backup sai assim que o daemon subir. Um backup que
   nao chega no Drive (Drive fora do ar, rede, disco) e tentado de novo a
   cada hora ate subir, sem esperar o dia seguinte; o "Proximo" mostra a
   nova tentativa. Backup que subiu sem algum database nao repete (o
   database quebrado falharia de novo): o erro fica no ultimo backup.

Restaurar:

```sh
cais backup files                                    # backups no Drive
cais backup restore <arquivo> -key chave.txt         # lista o que tem
cais backup restore <arquivo> -key chave.txt postgres/loja mysql/blog
```

O restore apaga o conteudo atual do database (as conexoes abertas caem),
recria com o mesmo usuario e senha e poe o dump. A chave so passa pela
memoria do daemon. Numa VPS nova, baixe o arquivo do Drive e extraia sem o
daemon: `cais backup open <arquivo> -key chave.txt -to /root/restore`
(dumps em `postgres/` e `mysql/`, estado em `state/etc` e `state/data`).

## Disco

Disco cheio derruba tudo de uma vez, bancos inclusive. O cais segura o que
mais cresce:

- **cache de build**: cada `build --pull` guarda camadas novas e nada as
  apaga. O daemon limpa uma vez por dia (a primeira 10 minutos depois de
  subir): o que esta sem uso ha uma semana e, depois, o que passar de 10 GB.
  So deixa o proximo build mais lento; nenhuma imagem ou container e tocado.
- **imagens antigas**: no storage classico (overlay2) cada build deixa a
  versao anterior da imagem sem tag. Depois de cada deploy que deu certo, o
  cais remove as do app (`image prune` com o label do projeto; o que e de
  fora do cais fica). No image store do containerd (padrao do Docker novo)
  isso nao acontece.
- **logs**: o log `json-file` padrao do Docker nao tem limite. As stacks da
  infra saem com 20 MB x 5 arquivos por container, e a validacao do compose
  avisa service de app sem limite (o modelo da Ajuda ja vem com ele).

## Build

```sh
make build   # gera dist/cais-linux-amd64 e dist/cais-linux-arm64
```
