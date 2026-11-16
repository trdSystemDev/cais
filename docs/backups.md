# Backups e disco

Backup diario criptografado pro Google Drive, restore, e o que o cais faz pra o disco nao encher.

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

O arquivo (`cais-<hostname>-<data>.tar.gz.age`) e criptografado com
[age](https://age-encryption.org) pra uma chave publica e sobe pra uma pasta
`cais-backups-<hostname>` no seu Google Drive (o cais guarda o id da pasta:
trocar o hostname depois nao separa os backups). A chave privada aparece uma vez
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

---

[Voltar pra documentacao](README.md)
