package host

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Os acessos por SSH (Setup > Acessos e o cais host access): quem entra e
// por qual chave. Sao dois jeitos de entrar:
//   - admin: usuario proprio no grupo sudo, com shell (um por pessoa);
//   - so a TUI: a chave no usuario cais (ssh cais@vps cai direto na TUI,
//     sem shell), que tambem recebe as chaves dos admins.
// As chaves do root nao entram pelo SSH depois do harden, mas o harden as
// copia pros admins e pro cais: tirar uma chave tira de todo lugar, senao
// o proximo harden (ou o Corrigir) a traria de volta.

// SSHKey e uma chave publica e onde ela entra.
type SSHKey struct {
	Type        string `json:"type"`
	Comment     string `json:"comment,omitempty"`
	Fingerprint string `json:"fingerprint"` // SHA256:..., como o ssh-keygen -l
	// Key e a linha da chave publica (sem as opcoes do authorized_keys):
	// publica, serve pra dar outro acesso a mesma chave.
	Key string `json:"key,omitempty"`
	// Users sao as contas com a chave (root, os admins, cais).
	Users []string `json:"users"`
}

// Admin e um usuario do grupo sudo.
type Admin struct {
	Name string `json:"name"`
	Keys int    `json:"keys"`
}

// Access e quem entra na VPS pelo SSH.
type Access struct {
	Keys   []SSHKey `json:"keys,omitempty"`
	Admins []Admin  `json:"admins,omitempty"`
	// TUI: o usuario cais existe (install.sh --ssh): da pra dar acesso so a
	// TUI.
	TUI bool `json:"tui,omitempty"`
}

// AccessRequest e dar acesso a uma chave: so a TUI (Admin vazio) ou como o
// admin Admin (criado se faltar; Password e a senha do sudo, obrigatoria
// pra admin novo ou sem senha).
type AccessRequest struct {
	Key      string `json:"key"`
	Admin    string `json:"admin,omitempty"`
	Password string `json:"password,omitempty"`
}

var keyTypes = []string{"ssh-ed25519", "ssh-rsa", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384",
	"ecdsa-sha2-nistp521", "sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com"}

// ParseKey confere uma chave publica (uma linha "tipo base64 comentario") e
// devolve a linha limpa e a chave.
func ParseKey(s string) (string, SSHKey, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", SSHKey{}, errors.New("cole a chave publica da pessoa (a linha do ~/.ssh/id_ed25519.pub)")
	}
	if strings.Contains(s, "PRIVATE KEY") {
		return "", SSHKey{}, errors.New("isso e a chave PRIVADA: nunca passe ela pra ninguem. Peca a publica (o arquivo .pub, ex: ~/.ssh/id_ed25519.pub)")
	}
	if strings.ContainsAny(s, "\n\r") {
		return "", SSHKey{}, errors.New("uma chave por vez (uma linha so)")
	}
	f := strings.Fields(s)
	if len(f) < 2 || !slices.Contains(keyTypes, f[0]) {
		return "", SSHKey{}, errors.New("nao parece uma chave publica SSH: e uma linha que comeca com ssh-ed25519 (ou ssh-rsa, ecdsa-...), do arquivo .pub")
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil || len(blob) < 20 {
		return "", SSHKey{}, errors.New("chave publica incompleta ou quebrada: copie a linha inteira do .pub")
	}
	sum := sha256.Sum256(blob)
	line := strings.Join(f, " ")
	k := SSHKey{Type: f[0], Comment: strings.Join(f[2:], " "),
		Fingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), Key: line}
	return line, k, nil
}

// accessUsers sao as contas cujas chaves entram (ou sao copiadas): root, os
// admins e o cais. Devolve a pasta de cada uma.
func accessUsers(ctx context.Context, sys System) (map[string]string, []string) {
	homes := map[string]string{"root": "/root"}
	for _, u := range sudoers(ctx, sys) {
		if home, ok := userHome(ctx, sys, u); ok {
			homes[u] = home
		}
	}
	if home, ok := hasCaisUser(ctx, sys); ok {
		homes[CaisUser] = home
	}
	return homes, sudoers(ctx, sys)
}

// sudoers sao os usuarios do grupo sudo.
func sudoers(ctx context.Context, sys System) []string {
	out, _ := sys.Run(ctx, "getent", "group", "sudo")
	f := strings.Split(strings.TrimSpace(out), ":")
	if len(f) != 4 || f[3] == "" {
		return nil
	}
	return strings.Split(f[3], ",")
}

func readKeys(sys System, home string) []string {
	b, err := sys.ReadFile(home + "/.ssh/authorized_keys")
	if err != nil {
		return nil
	}
	return parseKeys(string(b))
}

// ListAccess le quem entra pelo SSH: as chaves (e em quais contas) e os
// admins.
func ListAccess(ctx context.Context, sys System) (Access, error) {
	if sys.Geteuid() != 0 {
		return Access{}, errors.New("precisa de root")
	}
	homes, admins := accessUsers(ctx, sys)
	var a Access
	_, a.TUI = homes[CaisUser]
	byFP := map[string]int{}
	order := append([]string{"root"}, admins...)
	if a.TUI {
		order = append(order, CaisUser)
	}
	for _, u := range order {
		for _, line := range readKeys(sys, homes[u]) {
			_, k, err := ParseKey(keyPart(line))
			if err != nil {
				continue
			}
			if i, ok := byFP[k.Fingerprint]; ok {
				if !slices.Contains(a.Keys[i].Users, u) {
					a.Keys[i].Users = append(a.Keys[i].Users, u)
				}
				continue
			}
			k.Users = []string{u}
			byFP[k.Fingerprint] = len(a.Keys)
			a.Keys = append(a.Keys, k)
		}
	}
	for _, u := range admins {
		a.Admins = append(a.Admins, Admin{Name: u, Keys: len(readKeys(sys, homes[u]))})
	}
	return a, nil
}

// keyPart tira as opcoes do comeco da linha do authorized_keys
// (command="...",no-pty ssh-ed25519 ...): a chave comeca no tipo.
func keyPart(line string) string {
	for _, t := range keyTypes {
		if i := strings.Index(line, t+" "); i >= 0 {
			return line[i:]
		}
	}
	return line
}

// AddAccess da acesso a uma chave: so a TUI (no usuario cais) ou como admin
// (criado se faltar, no sudo, com a senha do sudo; a chave tambem vai pro
// cais, como a dos outros admins). Devolve o que fez.
func AddAccess(ctx context.Context, sys System, req AccessRequest) (string, error) {
	if sys.Geteuid() != 0 {
		return "", errors.New("precisa de root")
	}
	line, key, err := ParseKey(req.Key)
	if err != nil {
		return "", err
	}
	label := cmpOr(key.Comment, key.Fingerprint)
	if req.Admin == "" {
		home, ok := hasCaisUser(ctx, sys)
		if !ok {
			return "", errors.New("sem o usuario cais (o install.sh rodou sem --ssh): de acesso como admin, ou reinstale com --ssh")
		}
		changed, err := addKey(ctx, sys, CaisUser, home, line)
		if err != nil {
			return "", err
		}
		if !changed {
			return label + " ja entrava na TUI: nada mudou", nil
		}
		return label + " entra na TUI: ssh " + CaisUser + "@<vps>", nil
	}

	u := req.Admin
	switch {
	case u == "root" || u == CaisUser:
		return "", errors.New("o admin precisa ser um usuario proprio (root e cais nao servem)")
	case !validUser.MatchString(u):
		return "", errors.New("nome de usuario invalido: use minusculas, numeros, _ e -, comecando por letra")
	case strings.ContainsAny(req.Password, "\n\r"):
		return "", errors.New("a senha do sudo nao pode ter quebra de linha")
	case req.Password != "" && len(req.Password) < 8:
		return "", errors.New("senha do sudo curta: use pelo menos 8 caracteres")
	}
	_, exists := userHome(ctx, sys, u)
	if exists {
		out, _ := sys.Run(ctx, "passwd", "-S", u)
		if f := strings.Fields(out); (len(f) < 2 || f[1] != "P") && req.Password == "" {
			return "", errors.New("o " + u + " esta sem senha: defina a senha do sudo dele")
		}
	} else {
		if req.Password == "" {
			return "", errors.New("admin novo: defina a senha do sudo dele (o SSH continua so por chave)")
		}
		if _, err := sys.Run(ctx, "adduser", "--disabled-password", "--gecos", "", u); err != nil {
			return "", err
		}
	}
	// removido antes (expirado): volta
	if _, err := sys.Run(ctx, "usermod", "--expiredate", "", u); err != nil {
		return "", err
	}
	out, _ := sys.Run(ctx, "id", "-nG", u)
	wasAdmin := exists && slices.Contains(strings.Fields(out), "sudo")
	if _, err := adminGroups(ctx, sys, u); err != nil {
		return "", err
	}
	if req.Password != "" {
		if _, err := sys.RunInput(ctx, u+":"+req.Password+"\n", "chpasswd"); err != nil {
			return "", errors.New("senha do sudo: " + firstLine(err))
		}
	}
	home, ok := userHome(ctx, sys, u)
	if !ok {
		return "", errors.New("usuario " + u + " nao encontrado depois de criar")
	}
	keyAdded, err := addKey(ctx, sys, u, home, line)
	if err != nil {
		return "", err
	}
	// os admins entram tambem na TUI (como no harden)
	if home, ok := hasCaisUser(ctx, sys); ok {
		if _, err := addKey(ctx, sys, CaisUser, home, line); err != nil {
			return "", err
		}
	}
	verb := "ja era admin"
	switch {
	case !exists:
		verb = "admin criado"
	case !wasAdmin:
		verb = "virou admin"
	case !keyAdded && req.Password == "":
		return fmt.Sprintf("%s ja era admin com essa chave: nada mudou", u), nil
	case !keyAdded:
		return fmt.Sprintf("%s ja era admin com essa chave: so a senha do sudo mudou", u), nil
	}
	return fmt.Sprintf("%s: %s, entra com ssh %s@<vps> (sudo com a senha dele)", u, verb, u), nil
}

// addKey poe a linha no authorized_keys de user (dono e permissoes certos:
// com outros, o sshd recusa a chave calado). Diz se mudou.
func addKey(ctx context.Context, sys System, user, home, line string) (bool, error) {
	_, key, _ := ParseKey(line)
	current := readKeys(sys, home)
	for _, l := range current {
		if _, k, err := ParseKey(keyPart(l)); err == nil && k.Fingerprint == key.Fingerprint {
			return false, nil
		}
	}
	return true, writeKeys(ctx, sys, user, home, append(current, line))
}

func writeKeys(ctx context.Context, sys System, user, home string, keys []string) error {
	dir, file := home+"/.ssh", home+"/.ssh/authorized_keys"
	if _, err := sys.Run(ctx, "install", "-d", "-m", "700", dir); err != nil {
		return err
	}
	data := ""
	if len(keys) > 0 {
		data = strings.Join(keys, "\n") + "\n"
	}
	if err := sys.WriteFile(file, []byte(data), 0o600); err != nil {
		return err
	}
	_, err := sys.Run(ctx, "chown", "-R", user+":", dir)
	return err
}

// dropKeys tira de user as chaves com essas impressoes digitais. Diz se
// mudou.
func dropKeys(ctx context.Context, sys System, user, home string, fps []string) (bool, error) {
	current := readKeys(sys, home)
	keep := slices.DeleteFunc(slices.Clone(current), func(l string) bool {
		_, k, err := ParseKey(keyPart(l))
		return err == nil && slices.Contains(fps, k.Fingerprint)
	})
	if len(keep) == len(current) {
		return false, nil
	}
	return true, writeKeys(ctx, sys, user, home, keep)
}

// RemoveKey tira a chave de todo lugar (root, admins e cais). Recusa se
// nenhum admin ficaria com chave: o SSH so entra por chave, e todo mundo
// ficaria trancado fora.
func RemoveKey(ctx context.Context, sys System, fingerprint string) (string, error) {
	if sys.Geteuid() != 0 {
		return "", errors.New("precisa de root")
	}
	a, err := ListAccess(ctx, sys)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(a.Keys, func(k SSHKey) bool { return k.Fingerprint == fingerprint })
	if i < 0 {
		return "", errors.New("chave nao encontrada (ja saiu?)")
	}
	key := a.Keys[i]
	if err := keepsAnAdmin(ctx, sys, a, []string{fingerprint}, ""); err != nil {
		return "", err
	}
	homes, _ := accessUsers(ctx, sys)
	for _, u := range key.Users {
		if _, err := dropKeys(ctx, sys, u, homes[u], []string{fingerprint}); err != nil {
			return "", err
		}
	}
	return cmpOr(key.Comment, key.Fingerprint) + " nao entra mais (saiu de " + strings.Join(key.Users, ", ") + ")", nil
}

// RemoveAdmin tira o admin: sai do sudo, a conta expira (o SSH recusa ate
// com chave) e as chaves dele saem do authorized_keys dele e do cais (e do
// root) quando nenhum outro admin as usa. A pasta dele fica.
func RemoveAdmin(ctx context.Context, sys System, user string) (string, error) {
	if sys.Geteuid() != 0 {
		return "", errors.New("precisa de root")
	}
	a, err := ListAccess(ctx, sys)
	if err != nil {
		return "", err
	}
	if !slices.ContainsFunc(a.Admins, func(ad Admin) bool { return ad.Name == user }) {
		return "", errors.New(user + " nao e admin (nao esta no grupo sudo)")
	}
	// as chaves so dele: as que outro admin tambem tem ficam
	var fps []string
	for _, k := range a.Keys {
		if !slices.Contains(k.Users, user) {
			continue
		}
		shared := slices.ContainsFunc(k.Users, func(u string) bool {
			return u != user && u != "root" && u != CaisUser
		})
		if !shared {
			fps = append(fps, k.Fingerprint)
		}
	}
	if err := keepsAnAdmin(ctx, sys, a, fps, user); err != nil {
		return "", err
	}
	if _, err := sys.Run(ctx, "gpasswd", "-d", user, "sudo"); err != nil {
		return "", err
	}
	if out, _ := sys.Run(ctx, "id", "-nG", user); slices.Contains(strings.Fields(out), "docker") {
		if _, err := sys.Run(ctx, "gpasswd", "-d", user, "docker"); err != nil {
			return "", err
		}
	}
	if _, err := sys.Run(ctx, "usermod", "--expiredate", "1", user); err != nil {
		return "", err
	}
	homes, _ := accessUsers(ctx, sys)
	if home, ok := userHome(ctx, sys, user); ok {
		if _, err := writeKeysIfAny(ctx, sys, user, home); err != nil {
			return "", err
		}
	}
	for _, u := range []string{"root", CaisUser} {
		if home, ok := homes[u]; ok && len(fps) > 0 {
			if _, err := dropKeys(ctx, sys, u, home, fps); err != nil {
				return "", err
			}
		}
	}
	return fmt.Sprintf("%s nao e mais admin: saiu do sudo, a conta expirou e %d chave(s) sairam (a pasta /home/%s fica)", user, len(fps), user), nil
}

// writeKeysIfAny esvazia o authorized_keys do usuario, se tiver chave.
func writeKeysIfAny(ctx context.Context, sys System, user, home string) (bool, error) {
	if len(readKeys(sys, home)) == 0 {
		return false, nil
	}
	return true, writeKeys(ctx, sys, user, home, nil)
}

// keepsAnAdmin recusa a remocao que deixaria ninguem com chave pra
// administrar: os admins (fora o que sai) e o root, se o SSH ainda deixa
// ele entrar.
func keepsAnAdmin(ctx context.Context, sys System, a Access, fps []string, leaving string) error {
	homes, _ := accessUsers(ctx, sys)
	var who []string
	for _, ad := range a.Admins {
		if ad.Name != leaving {
			who = append(who, ad.Name)
		}
	}
	if eff, err := sshdConfig(ctx, sys, ""); err == nil && eff["permitrootlogin"] != "no" {
		who = append(who, "root")
	}
	for _, u := range who {
		for _, l := range readKeys(sys, homes[u]) {
			if _, k, err := ParseKey(keyPart(l)); err == nil && !slices.Contains(fps, k.Fingerprint) {
				return nil
			}
		}
	}
	return errors.New("ninguem ficaria com chave pra administrar a VPS (o SSH so entra por chave): todo mundo ficaria trancado fora. " +
		"De acesso de admin a outra chave antes")
}

// AdminNeedsPassword diz se dar acesso de admin a user pede a senha do
// sudo: admin novo ou sem senha.
func AdminNeedsPassword(ctx context.Context, sys System, user string) bool {
	if _, ok := userHome(ctx, sys, user); !ok {
		return true
	}
	out, _ := sys.Run(ctx, "passwd", "-S", user)
	f := strings.Fields(out)
	return len(f) < 2 || f[1] != "P"
}

// adminGroups poe o admin no sudo e, com o Docker instalado, no docker
// (roda o docker sem sudo; o grupo docker ja e root na pratica, como o
// sudo). Diz se mudou.
func adminGroups(ctx context.Context, sys System, user string) (bool, error) {
	out, _ := sys.Run(ctx, "id", "-nG", user)
	have := strings.Fields(out)
	groups := []string{"sudo"}
	if g, err := sys.Run(ctx, "getent", "group", "docker"); err == nil && strings.HasPrefix(g, "docker:") {
		groups = append(groups, "docker")
	}
	changed := false
	for _, g := range groups {
		if slices.Contains(have, g) {
			continue
		}
		if _, err := sys.Run(ctx, "usermod", "-aG", g, user); err != nil {
			return changed, err
		}
		changed = true
	}
	return changed, nil
}
