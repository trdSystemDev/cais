// Package secrets gerencia secrets centrais e por app: gerar, copiar,
// propagar e normalizar permissoes.
//
// Secrets ficam em texto puro em /var/lib/cais (config.AppSecretsDir e
// config.SecretsDir), e o compose do app os usa via "secrets: file:
// ./secrets/<nome>", que o Docker monta em /run/secrets/<nome>. Decisoes:
//
//   - Texto puro, nao criptografado em repouso: numa VPS sem TPM a chave
//     ficaria no mesmo disco, nao protege contra root e arrisca perder
//     todos os secrets numa migracao. A protecao e o /var/lib/cais 0700
//     do root; pastas de secrets 0700, arquivos 0444 (a permissao do
//     arquivo vale dentro do container, que pode rodar sem ser root).
//   - O arquivo precisa continuar no disco: o bind mount e refeito a cada
//     start do container (restart policy, reboot), e senhas de banco tem
//     que ser as mesmas na proxima subida.
//   - Nunca secret em "environment:": vaza no docker inspect, em processos
//     filhos e em dumps. Apps leem /run/secrets/<nome> ou usam *_FILE.
//   - O valor nunca sai do daemon por estado, log ou TUI: so o nome.
//   - Backups saem criptografados com age para uma chave publica; a
//     privada fica fora da VPS.
//   - Trocar um secret exige recriar o container: o bind mount de arquivo
//     prende o inode, e a gravacao atomica (rename) cria outro.
//
// O acesso passa por uma interface Store, pra um backend criptografado
// poder entrar depois sem mudar quem usa.
package secrets
