// Package backup faz o backup dos bancos e do estado do cais e o leva pro
// Google Drive.
//
// Cada backup e um arquivo so (cais-<host>-<data>.tar.gz.age): um tar
// compactado com os dumps (postgres/<db>.sql, mysql/<db>.sql) e o estado do
// cais (state/: config, apps sem o codigo, secrets), criptografado com age
// pra uma chave publica. A privada fica fora da VPS: quem invade a VPS nao
// abre os backups que ja estao no Drive. Decisoes:
//
//   - Um arquivo por backup: a retencao apaga um arquivo, o restore baixa um.
//   - O dump roda dentro do container do banco (docker exec pg_dump /
//     mysqldump): a VPS nao precisa dos clientes instalados, e a versao do
//     dump e a do servidor.
//   - Cada dump passa por um arquivo temporario (o tar precisa do tamanho
//     antes do conteudo) numa pasta 0700 do root, apagada no fim.
//   - Drive com o escopo drive.file: o cais so ve os arquivos que ele
//     mesmo criou (a pasta de backups), nada mais do Drive da pessoa.
//   - Login pelo fluxo de dispositivo (codigo + google.com/device): funciona
//     pela TUI via SSH sem colar nada de volta.
package backup
