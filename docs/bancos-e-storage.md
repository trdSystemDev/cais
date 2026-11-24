# Bancos e storage

Databases no Postgres/MySQL compartilhados (vazios ou importados de outro servidor) e buckets S3 no Garage.

## Databases

Cada app usa um database proprio no banco compartilhado (o compose de
producao nao declara banco; o de desenvolvimento pode). Crie na aba **Infra**
(card do Postgres/MySQL, secao **Geral**: **Novo banco**) ou pelo CLI:

```sh
cais db create postgres meu_app -extensions postgis
cais db list
```

O cais cria o database e um usuario de mesmo nome com senha nova, que so
acessa esse database. A senha fica guardada no cais e nao aparece na tela;
`cais db list` mostra cada database com as variaveis que o app recebe, o app
ligado e o espaco ocupado.

## Importar um banco de outro servidor

Em vez de nascer vazio, o database pode nascer com o conteudo de um banco
que ja existe em outro lugar (a VPS antiga, um RDS, o Supabase). No campo
**Importar de** do Novo banco, ou pelo CLI:

```sh
cais db create postgres loja -import   # pede a string de conexao, sem eco
echo "$URL" | cais db create mysql blog -import
```

```
postgres://usuario:senha@host:5432/banco?sslmode=require
mysql://usuario:senha@host:3306/banco
```

- O dump (`pg_dump`/`mysqldump`) roda dentro do container do banco do cais e
  vai direto pro restore, sem passar pelo disco. A origem so e lida.
- No Postgres o dump sai sem dono e sem GRANT e, no fim, tudo (schemas,
  tabelas, sequences, views, funcoes, tipos) passa a ser do usuario do app.
  Extensoes do dump sao criadas como superusuario (a imagem e a do PostGIS).
- No MySQL o `DEFINER` das views, triggers e rotinas da origem e tirado (o
  usuario de la nao existe aqui).
- Se a copia falha (senha, rede, extensao que nao existe aqui), o database
  novo e apagado: nada fica pela metade. Nome que ja existe na instancia sem
  ter sido criado pelo cais e recusado.
- A senha da origem vai pro dump pelo stdin: nao aparece no `ps`, no log nem
  no estado. Senha com `@ : / #` pode ir como esta, sem codificar.
- A origem tem que aceitar conexao vinda do IP desta VPS. Banco na propria
  VPS: use o IP dela na rede do docker (ex: `172.17.0.1`), nao `localhost`.
- O `pg_dump` do cais e o do Postgres 18: le servidores ate a versao 18.
- Importar nao e oferecido pelo MCP: a string de conexao tem a senha da
  origem, e ela nao passa pela IA.

Depois ligue o database ao app (Apps -> Bancos e buckets -> Ligar banco, ou
pelo CLI):

```sh
cais app db portal postgres/portal       # liga
cais app db portal off postgres/portal   # desliga (o database fica)
cais db delete postgres portal         # apaga database e usuario (desligado de todos os apps)
```

A cada deploy o cais le a credencial guardada e o app recebe, pelo `cais run`
(veja [apps.md](apps.md#variaveis-e-o-cais-run)), as variaveis
`<ENGINE>_<DATABASE>_USER`, `_PASS` e `_URL` (ex: `POSTGRES_PORTAL_USER`,
`POSTGRES_PORTAL_PASS` e `POSTGRES_PORTAL_URL=postgres://portal:...@postgres:5432/portal`,
com a senha escapada). O database tem o nome do usuario; o app chega no banco
por `postgres:5432` (ou `mysql:3306`). Senha nova no cais vale no proximo
deploy. Enquanto o banco esta ligado, o app nao pode ter variavel com esses
nomes.

## Buckets S3 (Garage)

O Garage publica a API S3 em `https://<hosts.s3>` (o endereco do S3, no
card do Garage na aba **Infra**, ou `cais setup config --host-s3`), com
regiao `garage` e enderecamento por caminho (path-style). O Setup prepara
o layout de no unico na primeira subida; execucoes seguintes preservam o
layout. Os volumes `garage_meta` e `garage_data` persistem ao desligar.
Esse layout nao oferece redundancia entre maquinas.

Crie os buckets na aba **Infra** (card do Garage, secao **Geral**: **Novo bucket**)
ou pelo CLI. Cada bucket ganha uma chave so dele (leitura, escrita e dono),
guardada no cais:

```sh
cais bucket create portal-fotos
cais bucket list                       # variaveis, espaco e app de cada bucket
cais app bucket portal portal-fotos        # liga ao app
cais app bucket portal off portal-fotos    # desliga (o bucket e os arquivos ficam)
cais bucket delete portal-fotos          # apaga (vazio e desligado de todos os apps)
```

A cada deploy o app recebe a chave nas variaveis `S3_<BUCKET>_ACCESS_KEY`,
`_SECRET_KEY` e `_ENDPOINT` (`-` do nome vira `_`; ex: `S3_PORTAL_FOTOS_ACCESS_KEY`).
Dentro da `garage_network` o endpoint e `http://garage:3900`, regiao `garage` e
path-style. O secret RPC e o token de admin sao internos,
nao sao chaves S3.

Os bancos nunca entram na `proxy_network`.

---

[Voltar pra documentacao](README.md)
