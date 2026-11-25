# Setup e configuracao

O Setup so guarda a configuracao da VPS (GitHub App, avisos, backups): salvar nao sobe nada. Os servicos (proxy, bancos, S3) se ligam na aba **Infra** ou com `cais service`, e a config de cada um fica no card dele, na Infra: certificados e portas no proxy, o endereco do S3 no Garage.

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

A aba **Setup** e dividida em sub-abas de tela cheia: **Geral** (o GitHub
App), **Avisos** (e-mail pela Brevo) e **Backups** (a
configuracao dos [backups](backups.md)). ←/→ troca a sub-aba; enter ou tab
entra nos campos e esc volta. Geral e Avisos sao o mesmo formulario: o Salvar
de qualquer uma grava tudo.
A aba **Infra** funciona como a de Apps: os cards (so o estado) ficam a
esquerda e, a direita, o servico selecionado em duas secoes. Clique num card
ou use ↑/↓; ←/→ troca a secao; enter ou tab entra nos botoes e esc volta.

- **Geral**: **Ligar/Desligar** (grava na config e sobe ou derruba o
  servico), **Aplicar** quando a config salva muda um servico ligado (ex: o
  e-mail do Let's Encrypt mudado pelo CLI muda o proxy; tecla `a`),
  **Iniciar/Parar** e **Reiniciar** (so os containers: o servico continua
  ligado). Embaixo, a config do servico, com um Salvar que ja aplica com
  ele ligado: no proxy, os **certificados** (Let's Encrypt com o e-mail dos
  avisos, ou a CA local com o dominio do teste) e as **portas**; no Garage, o
  **endereco do S3**. E o **Novo banco** no Postgres/MySQL e o **Novo
  bucket** no Garage, com a lista dos ja criados.
- **Logs**: os logs do container (ultimas 200 linhas, atualizadas a cada 2
  segundos). Enter entra e ←/→ troca o container nas stacks com mais de um;
  PgUp/PgDown ou a roda do mouse rola, e End volta as mensagens recentes.

Pelo CLI: `cais service <nome> on` de novo aplica, e `start`, `stop` e
`restart` iniciam, param e reiniciam.
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
  dns: cloudflare               # opcional: o Let's Encrypt prova pelo DNS
  wildcards: [dev.exemplo.com.br] # opcional (com dns): curinga *.dev.exemplo.com.br
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

## Certificados pelo DNS (Cloudflare) e Tailscale

Por padrao o Let's Encrypt confere cada dominio acessando o site pela porta
80. Isso nao funciona pra dominio que a internet nao alcanca, como apps de
dev so na VPN: um `*.dev.exemplo.com.br` apontando pro IP do Tailscale da
VPS (`100.x.y.z`). Pra esses, o proxy prova pelo **DNS da Cloudflare**: o
Traefik cria um registro TXT com um token da API e o Let's Encrypt confere
o TXT, sem acessar a VPS.

1. Na Cloudflare, **My Profile > API Tokens**: um token com
   **Zone > DNS > Edit** na zona do dominio (o modelo "Edit zone DNS").
2. Na aba **Infra > proxy**, **Emissor: Let's Encrypt (DNS Cloudflare)**,
   cole o **Token** (vai pro secret `cloudflare_dns_token`, so leitura no
   container pelo `CF_DNS_API_TOKEN_FILE`; nunca no compose nem no `docker
   inspect`) e, se quiser, os **Curingas**. **Salvar e aplicar**.
   Pelo CLI: `cais setup secret cloudflare_dns_token` e
   `cais setup config --dns cloudflare --wildcards dev.exemplo.com.br`.

Com `dev.exemplo.com.br` nos curingas, o Traefik tira um certificado so pra
`dev.exemplo.com.br` e `*.dev.exemplo.com.br` assim que sobe, e os apps
debaixo dele ja nascem com HTTPS, sem pedir outro (e sem o nome de cada um
aparecer nos registros publicos de certificados). Pelo DNS, **todo** dominio
dos apps precisa estar na Cloudflare, na conta do token: dominio de cliente
fora dela fica sem certificado. As portas do proxy tambem deixam de importar
pro Let's Encrypt.

O Tailscale em si e instalado na VPS (`curl -fsSL
https://tailscale.com/install.sh | sh` e `sudo tailscale up`); a checagem
de seguranca conta como esperados o WireGuard dele (41641/udp) e o que
escuta so nos IPs da tailnet (100.64.0.0/10). O registro DNS aponta pro IP
do Tailscale da VPS; ao reinstalar a VPS, o IP volta pelo painel do
Tailscale (Edit machine IP) ou troque o registro.

## Portas do proxy

O proxy sobe nas portas 80 e 443 do host. Pra testar (ou numa migracao, com
outro proxy ainda nas 80/443), da pra usar outras: na aba **Infra**, secao
**Geral** do proxy, ou pelo CLI. Na TUI, **Salvar e
aplicar** ja sobe o proxy de novo nas portas novas; pelo CLI, aplique com
`cais service proxy on`. Com o proxy desligado, as portas valem quando ele
ligar.

```sh
cais setup config --http-port 8080 --https-port 8443
cais service proxy on        # aplica: o proxy volta nas portas novas
```

Com HTTPS fora da 443, o redirecionamento de HTTP leva pra porta certa
(`http://site:8080` -> `https://site:8443`). O Let's Encrypt, porem, valida
pela porta 80 da internet: fora de 80/443 ele nao emite certificado e os
sites abrem com aviso. O painel de portas, a aba Setup e o `cais setup` avisam.

### Migrar de um nginx, app por app

Com um nginx ja nas 80/443 da VPS, da pra passar os sites pro cais aos
poucos: o nginx continua na frente e, a cada app migrado, passa a repassar o
dominio dele pro Traefik do cais, que entrega pro container.

1. Suba o proxy em outras portas, **atras do nginx** (na Infra, marque
   **Outro proxy** no painel de portas):

   ```sh
   cais setup config --http-port 8080 --https-port 8443 --behind-proxy
   cais service proxy on
   ```

   Assim as portas escutam so no `127.0.0.1` (nada novo aberto na internet:
   o docker publica por cima do `ufw`) e o Traefik aceita o `X-Forwarded-For`
   do nginx, pros apps verem o IP de quem acessa.

2. Faca o deploy do app no cais e troque o site dele no nginx:

   ```nginx
   server {
       listen 443 ssl;
       server_name app.exemplo.com.br;
       # ssl_certificate ... (o do nginx continua)

       client_max_body_size 0;
       location / {
           proxy_pass https://127.0.0.1:8443;
           proxy_ssl_server_name on;          # o Traefik escolhe o app pelo nome
           proxy_ssl_name $host;
           proxy_set_header Host $host;
           proxy_set_header X-Real-IP $remote_addr;
           proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
           proxy_set_header X-Forwarded-Proto https;
           proxy_http_version 1.1;            # websocket
           proxy_set_header Upgrade $http_upgrade;
           proxy_set_header Connection $connection_upgrade;
           proxy_read_timeout 30m;
       }
   }

   server {
       listen 80;
       server_name app.exemplo.com.br;
       # o certbot do nginx acha o arquivo dele; o resto vai pro Traefik
       location /.well-known/acme-challenge/ {
           root /var/www/certbot;             # a pasta do seu certbot
           try_files $uri @cais;
       }
       location @cais {
           proxy_pass http://127.0.0.1:8080;
           proxy_set_header Host $host;
       }
       location / { return 301 https://$host$request_uri; }
   }
   ```

   O `$connection_upgrade` vem do `map` padrao de websocket no `http {}`:

   ```nginx
   map $http_upgrade $connection_upgrade { default upgrade; '' close; }
   ```

   Repasse so o `/.well-known/acme-challenge/` da porta 80, nunca o site
   todo: com HTTPS fora da 443 o Traefik redirecionaria o navegador pra
   `https://site:8443`. Esse repasse e o que deixa o Let's Encrypt emitir o
   certificado do cais ja durante a migracao; sem ele, as tentativas falham
   e, depois de 5 falhas numa hora, o Let's Encrypt bloqueia o dominio por
   uma hora.

3. `nginx -t && systemctl reload nginx`, e o app ja esta no cais. Repita
   pros outros.

4. Com todos migrados, a virada: desligue o nginx e volte o proxy pras
   80/443, sem o atras do nginx. O Traefik ja tem os certificados, entao a
   troca dura so a subida do proxy:

   ```sh
   systemctl disable --now nginx
   cais setup config --http-port 80 --https-port 443 --behind-proxy=false
   cais service proxy on
   ```

## Requisicoes no dashboard

O Dashboard mostra as requisicoes que passam pelo proxy: quantas por segundo
agora, o grafico dos ultimos minutos, a porcentagem de erros 5xx, o total
desde que o proxy subiu e os services do Traefik que mais recebem (o nome e
o do router no compose do app). Os numeros vem dos contadores do proprio
Traefik, lidos pelo daemon por `docker exec`: nenhuma porta nova. O
redirecionamento do `http` pro `https` nao conta (seria a mesma visita duas
vezes).

Proxy que ja estava no ar antes desta versao: aplique de novo na aba Infra
(proxy > Aplicar, ou `cais service proxy on`) pra ligar os contadores. O
Traefik reinicia e os contadores voltam a zero.

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
