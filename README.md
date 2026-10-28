# cais

Painel TUI (Go + Bubble Tea/Lip Gloss, com suporte a mouse) para preparar e administrar uma VPS
que so tem Docker instalado: Traefik, registry, bancos Postgres/MySQL,
storage S3 com Garage, secrets por app e CI com Woodpecker.

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
| `internal/compose` | parse e validacao do compose (recusa escalada pro host) |
| `internal/secrets` | secrets centrais e por app |
| `internal/databases` | databases/usuarios Postgres e MySQL |
| `internal/apps` | apps em `/var/lib/cais/apps/<app>` |
| `internal/github` | link do GitHub, deteccao de CI |
| `internal/woodpecker` | cliente da API do Woodpecker |
| `internal/config` | `/etc/cais/config.yaml` e os caminhos no disco |
| `internal/ci` | pipeline do Woodpecker e chave de deploy do CI |
| `assets/` | unidade systemd |

## Diretorios na VPS

| Caminho | Conteudo |
|---|---|
| `/etc/cais/config.yaml` | configuracao: dominio base, e-mail, servicos opcionais ativos |
| `/var/lib/cais/stacks/<stack>/` | compose das stacks base (proxy, registry, woodpecker, postgres, mysql, garage) |
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

O cais gera sozinho todos os secrets das stacks (senhas dos bancos, agent
secret do Woodpecker, RPC secret e token de admin do Garage) e nunca troca
um que ja existe; so o client secret do GitHub e informado. O
registry tem um usuario por quem acessa: `ci` (push do Woodpecker) e `vps`
(pull do daemon) com senha guardada, e um por pessoa so com o hash (a senha e
mostrada uma vez). Todo usuario pode push/pull, entao revogar e por pessoa; o
delete pela API fica desligado.

## CI (Woodpecker)

```
push -> Woodpecker: build do Dockerfile (kaniko) -> push registry/<app>:<commit>
     -> ssh cais-deploy@vps "<app> <commit>"
     -> sshd forca: cais deploy -> /run/cais/deploy.sock -> deploy por tag
```

```sh
cais ci pipeline > .woodpecker.yml   # em cada repositorio (igual pra todo app)
cais ci setup                        # usuario cais-deploy, authorized_keys e secrets do Woodpecker
```

Nao ha script de deploy no host. A chave do CI tem `restrict` e o forced command
`cais deploy`: sem shell, pty nem tunel, so o pedido de deploy de uma tag. Ela
fala com um socket proprio (`deploy.sock`, so do usuario `cais-deploy`, aberto com
`cais daemon --deploy-user cais-deploy`), onde o daemon confere quem conectou
(`SO_PEERCRED`) e so aceita `app.deploy.tag`: o CI escolhe a tag, nunca a
configuracao do app. Cada pedido fica no log do daemon.

## Configuracao

`/etc/cais/config.yaml` guarda so o que um humano decide (secrets nunca):

```yaml
domain: exemplo.com.br          # ci., registry. e s3.<domain> saem daqui
email: admin@exemplo.com.br     # avisos do Let's Encrypt
stacks: [postgres, mysql, garage] # opcionais: so os que usar
proxy:
  http_port: 80                 # outra porta so numa migracao
  https_port: 443
hosts:                          # opcional: troca o nome padrao
  ci: build.exemplo.com.br
  registry: imagens.exemplo.com.br
  s3: arquivos.exemplo.com.br
woodpecker:
  github_client: Ov23li...      # Client ID do OAuth app do GitHub
  admin: seu-usuario-github
```

Campo desconhecido e erro (pega erro de digitacao). Os compose das stacks sao
gerados de `internal/stacks/templates/*/docker-compose.yml.tmpl` com esses
valores; o proxy (Traefik) nasce sem nenhum site, e cada servico se publica
com labels `traefik.*` na `proxy_network`. O Traefik so publica o que tem
`traefik.enable=true` e le o docker por um socket-proxy somente leitura.

Sem WAF no proxy, de proposito: pra apps proprios ele gera mais falso positivo
que protecao. A protecao vem de TLS, login forte e bancos fora da rede publica.
WAF e DDoS, se precisar, ficam melhor na borda (ex: Cloudflare).

Proxy, registry e Woodpecker sao fixos e sobem sempre. O Setup exige o
Client ID, o usuario administrador e o client secret do OAuth app do GitHub.
O campo `stacks` lista apenas os opcionais: Postgres, MySQL e Garage.
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

O `cais setup` mostra esse comando e onde ler as senhas.

O Garage publica a API S3 em `https://s3.<domain>` (ou `hosts.s3`), com
regiao `garage` e enderecamento por caminho (path-style). O Setup prepara
o layout de no unico na primeira subida; execucoes seguintes preservam o
layout. Os volumes `garage_meta` e `garage_data` persistem ao desligar.
Esse layout nao oferece redundancia entre maquinas.

Buckets e chaves ainda sao administrados pelo CLI do Garage:

```sh
cais service garage on
docker exec garage /garage bucket create meu-bucket
docker exec garage /garage key create minha-app
docker exec garage /garage bucket allow --read --write --key minha-app meu-bucket
```

Guarde a chave retornada em local seguro. Configure seu cliente S3 com o
endpoint HTTPS, regiao `garage` e path-style; no teste local, use a CA gerada
pelo cais. O secret RPC e o token de admin sao internos, nao sao chaves S3.

O registry de imagens e publico em `https://registry.<domain>` (TLS do Let's
Encrypt + login), numa stack propria: CI e VPS fazem push/pull pelo mesmo nome,
sem `insecure-registries`. Os bancos nunca entram na `proxy_network`,
e os builds do CI rodam fora da rede deles.

## Roadmap

1. Esqueleto: TUI com abas clicaveis, checagem de Docker/Compose
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
cais service postgres on # liga o Postgres e aplica a configuracao
cais service garage off  # desliga o S3 e preserva os volumes
```

A aba **Setup** concentra dominio, e-mail, certificados, GitHub e hosts.
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
cais setup config --domain exemplo.com.br --email voce@exemplo.com.br \
    --github-client <client id> --github-admin <usuario>
cais setup secret github_client_secret   # le sem eco, ou do stdin
cais setup                               # plano: o que falta e o que muda
cais setup run                           # gera secrets, grava os compose, sobe tudo
```

O `run` sobe as stacks em ordem com `docker compose up -d --wait` (cada uma
saudavel antes da proxima) e termina com o `docker login` da VPS no registry.
Antes, o DNS de `ci.` e `registry.<dominio>` precisa apontar pra VPS,
assim como `s3.<dominio>` quando o Garage estiver ativo.

Pra testar num PC, sem DNS publico: use um dominio `.localhost` (o
systemd-resolved e os navegadores ja resolvem `*.localhost` pra 127.0.0.1; `.local`
e recusado porque e do mDNS) e `--tls selfsigned`. O cais cria uma CA local
restrita ao dominio do teste (name constraints: nao assina nada fora dele) e um
certificado com SAN pra todos os nomes; o `setup run` termina mostrando como
confiar nela (`/etc/docker/certs.d/<registry>/ca.crt` pro docker, `trust anchor`
pro sistema). Use `CAIS_SOCKET`, `CAIS_CONFIG` e `CAIS_DATA_DIR` numa pasta
persistente (nao `/tmp`: os volumes sobrevivem ao reboot e os secrets nao).

Apps (cada um em `/var/lib/cais/apps/<app>`, projeto compose com o nome do app).
O repositorio do app so precisa do Dockerfile:

```sh
cais app init meu-app ./meu-app        # analisa Dockerfile, .env.example e manifestos;
                                       # mostra o plano (porta, rota, volumes, banco,
                                       # variaveis, secrets) e configura com confirmacao
cais app secret meu-app jwt_secret     # segredos detectados viram arquivo em /run/secrets
cais app deploy meu-app --tag <sha>    # o CI chama isso depois do build: o compose sai da
                                       # spec do app; o CI so escolhe a tag
```

A analise so sugere: porta, banco e variaveis podem ser trocados depois (`cais app
db`, `routes`). Variavel com cara de segredo (`*_SECRET`, `*_TOKEN`, `*_PASSWORD`...)
e recusada na spec: vira secret do app e o app recebe `X_FILE=/run/secrets/x`.

Modo avancado (compose pronto, validado do mesmo jeito):

```sh
cais app create meu-app
cais app deploy meu-app < docker-compose.yml       # valida, sobe e espera os healthchecks
cais app routes meu-app app.exemplo.com.br=web:3000 app.exemplo.com.br/api=api:8080
cais apps                                          # estado de todos
cais app logs meu-app meu-app-web-1
cais app db meu-app postgres                       # database + usuario do app no Postgres da infra
cais app db meu-app postgres DATABASE_URL_FILE=url_file   # caminho do arquivo com a URL completa
```

O app nao declara o banco no compose: `cais app db` cria database e usuario com o
nome do app e injeta a conexao pelo override -- rede do banco, os secrets e as
variaveis (`DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_PASSWORD_FILE` por
padrao; `VAR=host|port|name|user|password_file|url_file` pra outros nomes). O
usuario de um app nao conecta no database de outro.

**A senha nunca vai em variavel de ambiente** (apareceria no `docker inspect`):
fica so nos arquivos `/run/secrets/cais_db_password` e `/run/secrets/cais_db_url`
(a URL completa), e o app le de la. Pra framework que so aceita `DATABASE_URL`,
leia o arquivo no codigo antes de conectar, ou num entrypoint:

```sh
export DATABASE_URL="$(cat /run/secrets/cais_db_url)"   # so dentro do processo
exec "$@"
```

O compose do app vem do repositorio e passa pela validacao de `internal/compose`
(lista de permitidos: nada de privilegiado, caminho do host, rede ou volume de
outra stack, porta publicada nem label do Traefik). O roteamento e do cais: as
rotas viram um `cais.override.yml` com os labels, e um dominio pertence a um app
so. Se o deploy falha e havia versao no ar, ela volta sozinha.

Acesso ao registry de imagens:

```sh
cais registry add patrick      # mostra a senha uma vez
cais registry users
cais registry remove patrick
```

Em desenvolvimento (Windows/macOS) o socket padrao fica na pasta temporaria;
rode `cais daemon` num terminal e `cais` em outro.

## Build

```sh
make build   # gera dist/cais-linux-amd64 e dist/cais-linux-arm64
```
