# cais

Painel TUI (Go + tview, com suporte a mouse) para preparar e administrar uma VPS
que so tem Docker instalado: BunkerWeb, registry, bancos Postgres/MySQL,
secrets por app e CI com Woodpecker.

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
     docker / secrets / databases / apps / github / woodpecker
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
  fora de `internal/ui` importar tview/tcell.

## Estrutura

| Caminho | Responsabilidade |
|---|---|
| `cmd/cais` | ponto de entrada: monta o core e a interface |
| `internal/core` | estado, acoes e inscricoes -- o que as interfaces usam |
| `internal/rpc` | socket do daemon: servidor, cliente (`core.API`) e protocolo |
| `internal/ui` | paginas tview clicaveis (so le estado e chama acoes) |
| `internal/bootstrap` | assistente da VPS vazia |
| `internal/stacks` | stacks base + `templates/` embutidos |
| `internal/docker` | checagem, SDK do Docker e `docker compose` |
| `internal/compose` | parse e validacao (regras do `deploy.sh`) |
| `internal/secrets` | secrets centrais e por app |
| `internal/databases` | databases/usuarios Postgres e MySQL |
| `internal/apps` | apps em `/deploy/apps/<app>` |
| `internal/github` | link do GitHub, deteccao de CI |
| `internal/woodpecker` | cliente da API do Woodpecker |
| `internal/config` | `/etc/cais/config.yaml` |
| `assets/` | `deploy.sh` (forced command), CI de exemplo e unidade systemd |

Os arquivos em `internal/stacks/templates/` sao copia dos compose do repo `VPS`
e ainda serao parametrizados (dominio, e-mail etc.) na fase 2.

## Roadmap

1. Esqueleto: layout tview com abas clicaveis, checagem de Docker/Compose
2. Bootstrap: templates das stacks + assistente da VPS vazia
3. Apps e secrets: logs ao vivo, restart, propagar secret
4. Bancos
5. GitHub + Woodpecker: criar app pelo link, detectar/gerar CI
6. Backups, releases e `install.sh`

## Uso

```sh
cais daemon        # normalmente via systemd (assets/systemd/cais.service)
cais               # abre a TUI
cais state         # estado atual em JSON
cais check         # checa o ambiente e imprime o resultado
```

Em desenvolvimento (Windows/macOS) o socket padrao fica na pasta temporaria;
rode `cais daemon` num terminal e `cais` em outro.

## Build

```sh
make build   # gera dist/cais-linux-amd64 e dist/cais-linux-arm64
```
