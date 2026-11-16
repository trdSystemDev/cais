# Setup e configuracao

O Setup so guarda a configuracao da VPS (e-mail do Let's Encrypt, GitHub App, avisos, host do S3): salvar nao sobe nada. Os servicos (proxy, bancos, S3) se ligam na aba **Infra** ou com `cais service`.

## Comandos

`cais help` lista todos os comandos. `cais <grupo> -h` mostra so os de um
grupo (`cais app -h`, `cais db -h`) e `cais <grupo> <comando> -h` explica um
comando com as flags e exemplos (`cais app deploy -h`).

```sh
cais daemon        # normalmente via systemd (o servico cais, do install.sh)
cais               # abre a TUI
cais state         # estado atual em JSON
cais check         # checa o ambiente e imprime o resultado
cais service proxy on    # liga o proxy (HTTPS dos apps)
cais service postgres on # liga o Postgres
cais service garage off  # desliga o S3 e preserva os volumes
```

A aba **Setup** concentra o e-mail do Let's Encrypt, os certificados (Let's
Encrypt, ou a CA local pra teste no PC), o GitHub App, os avisos por e-mail e
o host do S3.
A aba **Infra** mostra cada servico com os botoes Ligar/Desligar, o passo em
andamento e as instrucoes do S3. Quando a config salva muda um servico que ja
esta ligado (ex: o e-mail do Let's Encrypt muda o proxy), o card mostra
**Aplicar** (tecla `a`); pelo CLI, `cais service <nome> on` de novo.
Os cards ficam a esquerda; a direita aparecem os logs do container selecionado
(ultimas 200 linhas, atualizadas a cada 2 segundos). Clique num card ou use
↑/↓; ←/→ troca o container nas stacks com mais de um. PgUp/PgDown ou a roda
do mouse rola os logs, e End volta a acompanhar as mensagens recentes.
Ligar e aplicar podem ser repetidos: nada que existe e regenerado e servico
sem mudanca nao e recriado. O que for ligado pelo CLI aparece ao vivo na TUI
aberta, e vice-versa.

```sh
cais setup config --email voce@exemplo.com.br --github-app-id <id>
cais setup secret github_app_key < chave.pem
cais setup                               # a config, o que falta e o que aplicar
cais service proxy on                    # gera secrets, grava o compose, sobe
```

Ligar sobe com `docker compose up -d --wait` (saudavel antes de seguir). O DNS de cada app (o dominio que o compose dele
usar) precisa apontar pra VPS, assim como o host do S3 quando o Garage estiver
ativo.

## config.yaml

`/etc/cais/config.yaml` guarda so o que um humano decide (secrets nunca):

```yaml
email: admin@exemplo.com.br     # avisos do Let's Encrypt
stacks: [postgres, mysql, garage] # opcionais: so os que usar
proxy:
  http_port: 80                 # outra porta so numa migracao
  https_port: 443
hosts:
  s3: s3.exemplo.com.br         # endereco publico do S3; obrigatorio com o Garage
github:
  app_id: 123456                # GitHub App do cais (baixa o codigo dos apps)
notify:                         # avisos por e-mail pela Brevo; sem "to", desligados
  from: cais@exemplo.com.br     # remetente validado na conta da Brevo
  to: [ops@exemplo.com.br]
```

Nao ha dominio da VPS: cada app traz o proprio dominio (a variavel `DOMINIO`
nas labels do compose dele), e o certificado do Let's Encrypt e pedido pra
esse nome. A VPS aparece pelo hostname no assunto dos avisos e no nome dos
backups. So o modo de teste (`tls: selfsigned`) tem um dominio, o
`test_domain`: a CA local so assina nomes dele. Config de versao antiga com
`domain:` ainda abre: ele vira `test_domain` (no modo de teste) e
`hosts.s3: s3.<domain>` (com o Garage ligado).

Campo desconhecido e erro (pega erro de digitacao). Os compose das stacks sao
gerados de `internal/stacks/templates/*/docker-compose.yml.tmpl` com esses
valores; o proxy (Traefik) nasce sem nenhum site, e cada servico se publica
com labels `traefik.*` na `proxy_network`. O Traefik so publica o que tem
`traefik.enable=true` e le o docker por um socket-proxy somente leitura.

Sem WAF no proxy, de proposito: pra apps proprios ele gera mais falso positivo
que protecao. A protecao vem de TLS, login forte e bancos fora da rede publica.
WAF e DDoS, se precisar, ficam melhor na borda (ex: Cloudflare).

Nenhum servico sobe sem ser ligado. O campo `stacks` lista os ligados:
proxy, Postgres, MySQL e Garage (o Garage precisa do proxy). Ligue cada um
pela aba **Infra** ou com `cais service <nome> on`; para desligar, use `off`.
Sem o proxy, app com dominio (labels do Traefik na `proxy_network`) e
recusado no deploy, com o aviso pra ligar o proxy. So os secrets dos servicos ativos sao gerados. Cada banco fica
na propria rede (`postgres_network`, `mysql_network`),
entao um app so enxerga o banco que usa.

Desligar um servico nao apaga nada: a aplicacao derruba os
containers dela (`docker compose down`, sem `-v`) e o compose vira
`docker-compose.yml.desativada`, como registro. Os volumes, com os dados, ficam;
religar a stack traz tudo de volta. Stacks de versoes antigas do cais sao
tratadas do mesmo jeito.

## Portas do proxy

O proxy sobe nas portas 80 e 443 do host. Pra testar (ou numa migracao, com
outro proxy ainda nas 80/443), da pra usar outras: na aba **Setup**, secao
**Proxy**, ou pelo CLI. Mudou com o proxy ligado, aplique (aba **Infra** ->
**Aplicar**, ou `cais service proxy on`): o container sobe de novo na porta
nova.

```sh
cais setup config --http-port 8080 --https-port 8443
cais service proxy on        # aplica: o proxy volta nas portas novas
```

Com HTTPS fora da 443, o redirecionamento de HTTP leva pra porta certa
(`http://site:8080` -> `https://site:8443`). O Let's Encrypt, porem, valida
pela porta 80 da internet: fora de 80/443 ele nao emite certificado e os
sites abrem com aviso. A aba Setup e o `cais setup` avisam.

## Acesso aos bancos

Os bancos nao tem painel web: so escutam no loopback da VPS (`127.0.0.1:5432` e
`:3306`), e a administracao e por tunel SSH, com o cliente que preferir no seu
computador. Nenhuma porta nova fica aberta na internet:

```sh
ssh -N -L 5432:127.0.0.1:5432 -L 3306:127.0.0.1:3306 voce@vps
# e conecte em localhost:5432 (postgres) e localhost:3306 (root)
```

O `cais setup` mostra esse comando e onde ler as senhas root.

## Avisos por e-mail

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

---

[Voltar pra documentacao](README.md)
