# Desenvolvimento

Como o cais e organizado por dentro, como rodar no PC e como lancar uma versao.

## Arquitetura

Go, com a TUI em Bubble Tea e Lip Gloss (clicavel, com mouse).

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

### Logs

O daemon loga com `log/slog` no stderr, em `chave=valor` (ou JSON com
`--log-format json`; nivel com `--log-level`). Sob o systemd o horario sai da
linha, porque o journal ja registra o dele. Campos fixos, pra filtrar:

| Campo | O que e |
| --- | --- |
| `acao` | `deploy`, `deploy_automatico`, `rollback`, `promocao`, `recuperacao`, `backup`, `restore`, `aviso`, `limpeza`, `desligamento` |
| `app` | o app do deploy |
| `commit` | o commit no ar depois da acao (curto) |
| `duracao` | quanto a acao levou |
| `erro` | a falha |

Ex: `journalctl -u cais | grep app=loja` mostra cada deploy do app, do
comeco ao fim. Nenhum valor de secret, senha ou chave vai pro log (o restore
registra o arquivo e os databases, nunca a chave); `internal/core/log_test.go`
confere.

O core recebe o logger em `core.Deps.Logger` (padrao `slog.Default()`, que o
`cais daemon` configura).

### Adicionar uma acao

1. O metodo na `core.API` (`internal/core/api.go`) e no `*core.Core`.
2. A constante em `internal/rpc/protocol.go`, com `// params: <tipo>` ao lado
   quando recebe parametros (e o tipo dos parametros, se for novo).
3. Uma linha na tabela `handlers` de `internal/rpc/server.go`:
   `get`/`run` sem parametros, `call`/`do` com parametros (`call` e `get`
   devolvem valor; `run` e `do` so erro). Metodo que recebe uma struct inteira
   usa a expressao de metodo direto (`call(core.API.CreateApp)`); com varios
   argumentos, um adaptador que desmonta os parametros.
4. O metodo no `internal/rpc/client.go` (`result[R]` pra quem devolve valor).
5. No MCP (`internal/mcpserver`), se a IA puder usar.

O teste de contrato (`internal/rpc/contract_test.go`) pega o que faltar: cada
metodo da `core.API` e chamado pelo cliente e tem que chegar no core com os
mesmos argumentos, e cada constante do protocolo tem que ter handler.

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
| `internal/secrets` | secrets centrais (senhas das stacks, chaves do GitHub App e da Brevo) |
| `internal/databases` | databases/usuarios Postgres e MySQL |
| `internal/apps` | apps em `/var/lib/cais/apps/<app>`: deploy, variaveis (`run.env` e `.env`), avisos e ajuda do compose |
| `internal/github` | GitHub App: token por repositorio, commit e tarball do codigo |
| `internal/config` | `/etc/cais/config.yaml` e os caminhos no disco |
| `internal/host` | seguranca da VPS: checagem (`cais host check`, estado) e preparo (`cais host harden`) |
| `install.sh` | instalador: baixa o release e traz a unidade do systemd |
| `.github/workflows/` | release a cada tag `v*` |
| `docs/` | esta documentacao |

## Pastas na VPS

| Caminho | Conteudo |
|---|---|
| `/etc/cais/config.yaml` | configuracao: e-mail, servicos opcionais ativos, hosts, avisos |
| `/var/lib/cais/stacks/<stack>/` | compose das stacks (proxy, postgres, mysql, garage) |
| `/var/lib/cais/apps/<app>/` | `app.yaml`, `env` (variaveis, JSON `0600`), `run.env` (o que o `cais run` injeta, gerado no deploy) e o codigo: `src/` (no ar) e `src.prev` (anterior) |
| `/var/lib/cais/secrets/` | secrets centrais (senhas das instancias de banco etc.) |
| `/var/lib/cais/backups/` | backups |
| `/run/cais/cais.sock` | socket do daemon |

`/etc/cais` e `/var/lib/cais` sao criados pelo systemd (`ConfigurationDirectory`
e `StateDirectory`); `/var/lib/cais` e `0700`, so o daemon escreve nele. Os
caminhos ficam em `internal/config/paths.go` e podem ser trocados por
`CAIS_CONFIG` e `CAIS_DATA_DIR` (ex: pra testar no PC).

Secrets e variaveis dos apps ficam em texto puro em `/var/lib/cais` (pasta
root `0700`); o valor nunca aparece no estado, em log nem pelo MCP. O app
recebe as variaveis no processo pelo `cais run`, nunca na config do container
(`docker inspect`). Backups saem
criptografados com [age](https://age-encryption.org) para uma chave publica
cuja privada fica fora da VPS. Motivos em `internal/secrets/doc.go`.

O cais gera sozinho os secrets das stacks (senhas root dos bancos, RPC
secret e token de admin do Garage) e nunca troca um que ja existe; so a chave
do GitHub App e a da Brevo (avisos por e-mail) sao informadas.

## Testar no PC

Pra testar num PC, sem DNS publico: `--tls selfsigned --test-domain
trd.localhost` (o systemd-resolved e os navegadores ja resolvem `*.localhost`
pra 127.0.0.1; `.local` e recusado porque e do mDNS), e os apps em
`<app>.trd.localhost`. O cais cria uma CA local
restrita ao dominio do teste (name constraints: nao assina nada fora dele) e um
certificado com SAN pra todos os nomes; ligar o proxy termina mostrando como
confiar nela (`trust anchor`). Use `CAIS_SOCKET`, `CAIS_CONFIG` e `CAIS_DATA_DIR` numa pasta
persistente (nao `/tmp`: os volumes sobrevivem ao reboot e os secrets nao).

Em desenvolvimento (Windows/macOS) o socket padrao fica na pasta temporaria;
rode `cais daemon` num terminal e `cais` em outro.

## Build e testes

```sh
make build         # dist/cais-linux-amd64 e dist/cais-linux-arm64
make test          # go test ./...
make test-race     # go test -race ./... (o que o CI roda)
make lint          # gofmt, go vet, staticcheck e govulncheck
make test-docker   # importacao de banco contra bancos de verdade (docker)
make dev           # daemon de teste + TUI no PC
```

O CI (`.github/workflows/ci.yml`) roda `make lint` e `make test-race` a cada
push na `main` e em todo pull request. As versoes do staticcheck e do
govulncheck ficam fixas no `Makefile`; o go baixa na primeira vez. O
`make test-docker` fica fora do CI automatico (lento, precisa do docker):
roda pelo botao "Run workflow" do CI no GitHub.

## Releases

Cada tag `v*` no GitHub vira um release pelo GitHub Actions
(`.github/workflows/release.yml`): roda o `make lint` e os testes com `-race`, compila os binarios
`cais-linux-amd64` e `cais-linux-arm64` e publica com o `SHA256SUMS` e o
`install.sh`. Pra lancar uma versao, da `main` em dia com o GitHub:

```sh
make release V=v0.3.0
```

O build e reproduzivel: `-trimpath` (nenhum caminho da maquina no binario) e
o Go fixo no `toolchain` do `go.mod`, que o release confere. O mesmo commit da
o mesmo binario no PC e no GitHub Actions; pra conferir um release:

```sh
git checkout v0.3.0 && make build VERSION=v0.3.0
sha256sum dist/cais-linux-*    # igual ao SHA256SUMS do release
```

`cais version` mostra a versao, o commit e o Go do binario.

Pra testar numa VPS antes de lancar, uma tag com `-` vira pre-release: o
GitHub nao a entrega como a mais nova, entao so instala quem pedir.

```sh
make release V=v0.3.0-rc1
# na VPS: o comando de instalacao com --version v0.3.0-rc1
```

## Roadmap

1. Esqueleto: TUI com abas clicaveis, checagem de Docker/Compose
2. Bootstrap: templates das stacks + assistente da VPS vazia
3. Apps: repositorio do GitHub com compose de producao, deploy com volta, logs, restart
4. Bancos: database e usuario por app no Postgres/MySQL compartilhado
5. Deploy automatico (feito: consulta ao GitHub a cada minuto, sem webhook nem porta) e homologacao com promocao
6. Backups (feito: bancos e estado do cais, criptografados com age, pro Google Drive ou OneDrive)
7. `install.sh` (feito: por curl, do release do GitHub)
8. MCP (feito: `cais mcp` pelo SSH, sem exclusoes nem Setup)
9. Seguranca da VPS (feito: `cais host harden` e a checagem no painel; o `install.sh` instala o Docker)

---

[Voltar pra documentacao](README.md)
