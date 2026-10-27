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
| `internal/compose` | parse e validacao do compose (recusa escalada pro host) |
| `internal/secrets` | secrets centrais e por app |
| `internal/databases` | databases/usuarios Postgres e MySQL |
| `internal/apps` | apps em `/var/lib/cais/apps/<app>` |
| `internal/github` | link do GitHub, deteccao de CI |
| `internal/woodpecker` | cliente da API do Woodpecker |
| `internal/config` | `/etc/cais/config.yaml` e os caminhos no disco |
| `assets/` | CI de exemplo e unidade systemd |

## Diretorios na VPS

| Caminho | Conteudo |
|---|---|
| `/etc/cais/config.yaml` | configuracao: dominio base, e-mail, stacks ativas |
| `/var/lib/cais/stacks/<stack>/` | compose das stacks base (proxy, infra, woodpecker, dbgate) |
| `/var/lib/cais/apps/<app>/` | compose do app e `secrets/` dele |
| `/var/lib/cais/secrets/` | secrets centrais (senhas das instancias de banco etc.) |
| `/var/lib/cais/backups/` | backups |
| `/run/cais/cais.sock` | socket do daemon |

`/etc/cais` e `/var/lib/cais` sao criados pelo systemd (`ConfigurationDirectory`
e `StateDirectory`); `/var/lib/cais` e `0700`, so o daemon escreve nele. Os
caminhos ficam em `internal/config/paths.go` e podem ser trocados por
`CAIS_CONFIG` e `CAIS_DATA_DIR` (ex: pra testar no PC).

Secrets ficam em texto puro em `/var/lib/cais` (pasta root `0700`, arquivos
`0444`) e chegam ao container por `secrets: file: ./secrets/<nome>`. Nunca em
`environment:`; o valor nunca aparece no estado, em log ou na TUI; backups saem
criptografados com [age](https://age-encryption.org) para uma chave publica
cuja privada fica fora da VPS. Motivos em `internal/secrets/doc.go`.

Nao ha script de deploy no host: o CI faz `ssh deploy@vps <app> < docker-compose.yml`
e o forced command da chave e o proprio `cais deploy` (fase 3), que manda o
compose pro daemon pelo socket. O daemon valida, grava e roda `pull`/`up -d`,
e o usuario `deploy` nao tem acesso a nenhum arquivo.

## Configuracao

`/etc/cais/config.yaml` guarda so o que um humano decide (secrets nunca):

```yaml
domain: exemplo.com.br          # ci.<domain> e db.<domain> saem daqui
email: admin@exemplo.com.br     # avisos do Let's Encrypt
stacks: [proxy, infra, woodpecker, dbgate]
proxy:
  http_port: 80                 # outra porta so numa migracao
  https_port: 443
hosts:                          # opcional: troca o nome padrao
  ci: build.exemplo.com.br
woodpecker:
  github_client: Ov23li...      # Client ID do OAuth app do GitHub
  admin: seu-usuario-github
```

Campo desconhecido e erro (pega erro de digitacao). Os compose das stacks sao
gerados de `internal/stacks/templates/*/docker-compose.yml.tmpl` com esses
valores; o proxy nasce sem nenhum site, e cada servico se publica com labels
`bunkerweb.*` na `proxy_network` (autoconf).

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
