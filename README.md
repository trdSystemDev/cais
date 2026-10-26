# vpsctl

Painel TUI (Go + tview, com suporte a mouse) para preparar e administrar uma VPS
que so tem Docker instalado: BunkerWeb, registry, bancos Postgres/MySQL,
secrets por app e CI com Woodpecker.

Roda direto no host, pela sessao SSH: nao abre porta e nao tem login proprio.

## Arquitetura

O projeto e agnostico a interface. O `core` e o unico dono do estado e o
unico que executa acoes; as interfaces so chamam acoes e reagem ao estado.

```
   TUI (internal/ui)          MCP (futuro)
   acoes  -> c.Acao()         tool -> c.Acao()
   estado <- c.Subscribe()    c.Snapshot() em JSON
              \                /
               internal/core: estado imutavel + acoes
                       |
     docker / secrets / databases / apps / github / woodpecker
```

Regras:

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
| `cmd/vpsctl` | ponto de entrada: monta o core e a interface |
| `internal/core` | estado, acoes e inscricoes -- o que as interfaces usam |
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
| `internal/config` | `/etc/vpsctl/config.yaml` |
| `assets/` | `deploy.sh` (forced command) e CI de exemplo |

Os arquivos em `internal/stacks/templates/` sao copia dos compose do repo `VPS`
e ainda serao parametrizados (dominio, e-mail etc.) na fase 2.

## Roadmap

1. Esqueleto: layout tview com abas clicaveis, checagem de Docker/Compose
2. Bootstrap: templates das stacks + assistente da VPS vazia
3. Apps e secrets: logs ao vivo, restart, propagar secret
4. Bancos
5. GitHub + Woodpecker: criar app pelo link, detectar/gerar CI
6. Backups, releases e `install.sh`

## Build

```sh
make build   # gera dist/vpsctl-linux-amd64 e dist/vpsctl-linux-arm64
```
