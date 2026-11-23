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
`cais db list` mostra cada database com os secrets que o app recebe, o app
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

## Buckets S3 (Garage)

O Garage publica a API S3 em `https://<hosts.s3>`, com
regiao `garage` e enderecamento por caminho (path-style). O Setup prepara
o layout de no unico na primeira subida; execucoes seguintes preservam o
layout. Os volumes `garage_meta` e `garage_data` persistem ao desligar.
Esse layout nao oferece redundancia entre maquinas.

Crie os buckets na aba **Infra** (card do Garage, secao **Geral**: **Novo bucket**)
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

---

[Voltar pra documentacao](README.md)
