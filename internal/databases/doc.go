// Package databases cria o database e o usuario de cada app nas instancias
// compartilhadas de Postgres e MySQL (Provision), pelo docker exec com o SQL
// no stdin. Idempotente; o usuario de um app nao conecta no database de
// outro. A ligacao com o app (rede, secret e variaveis) fica em apps.
package databases
