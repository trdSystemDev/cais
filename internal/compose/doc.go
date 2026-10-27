// Package compose faz o parse dos docker-compose.yml (compose-go) e recusa o
// que daria escalada pro host. Substitui o antigo deploy.sh (forced command),
// cujas regras valem aqui, agora sobre o YAML parseado e nao sobre texto:
//
//   - o arquivo precisa ter services
//   - nada de privileged, cap_add, network_mode: host, pid: host
//   - nenhum volume com o docker.sock nem bind da raiz do host (/)
//   - todo "file:" de secret precisa ser ./secrets/<nome>, relativo e sem
//     "..": nunca o secret de outro app nem arquivo arbitrario do host
//   - um compose novo substitui o do app, mas secrets/ nunca e tocado
package compose
