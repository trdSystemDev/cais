# Politica de privacidade do cais

O cais e um programa que roda na sua propria VPS e administra os servicos
dela (proxy, bancos, storage e apps). Esta politica cobre o acesso que o cais
pede ao Google Drive e ao OneDrive para guardar os backups.

## O que o cais acessa

- **Google Drive:** so o escopo `drive.file`. O cais ve e altera apenas os
  arquivos que ele mesmo cria (a pasta `cais-backups-<hostname>`). O resto do
  seu Drive nao e visto.
- **OneDrive:** so a pasta do app (`Files.ReadWrite.AppFolder`, em
  `Aplicativos/cais`) e o e-mail da conta (`User.Read`), para mostrar qual
  conta esta conectada.

## Onde ficam os dados

- O token do login fica **so na sua VPS**, como um secret do cais. Ele nao e
  enviado para a TRD System nem para mais ninguem.
- Os backups sao criptografados na VPS, com a sua chave publica, antes de
  sair dela. So quem tem a chave privada abre os arquivos, e ela nao fica na
  VPS nem com a TRD System.
- A TRD System **nao recebe, nao guarda e nao ve** nenhum dado seu: o app
  registrado no Google e na Microsoft so identifica o cais no login. A
  comunicacao e direta entre a sua VPS e o Google ou a Microsoft.

## Como tirar o acesso

- No cais: Setup -> Backups -> Desconectar (ou `cais backup logout`). O
  token e apagado da VPS.
- No Google: https://myaccount.google.com/permissions
- Na Microsoft: https://account.live.com/consent/Manage

Os backups que ja estao na sua nuvem continuam la ate voce apagar.

## Contato

Duvidas: abra uma issue em https://github.com/trdSystemDev/cais/issues
