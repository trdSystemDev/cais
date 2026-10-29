// Package buckets cria os buckets do Garage (S3 da infra) e a chave de
// acesso de cada um, pelo CLI do Garage dentro do container (docker exec).
// Cada bucket tem a propria chave, que so acessa ele. A ligacao com o app
// (secrets) fica em apps.
package buckets
