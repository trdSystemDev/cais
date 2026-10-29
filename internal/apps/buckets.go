package apps

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// BucketLink liga o app a um bucket do Garage. No deploy o cais grava a
// chave do bucket nos secrets ${CAIS_SECRETS}/s3_<bucket>_access_key e
// s3_<bucket>_secret_key (- do nome vira _).
type BucketLink struct {
	Name string `yaml:"name" json:"name"`
}

// ID e como o cais mostra o bucket (s3/fotos).
func (l BucketLink) ID() string { return "s3/" + l.Name }

func (l BucketLink) prefix() string { return "s3_" + strings.ReplaceAll(l.Name, "-", "_") }

// AccessKeySecret e o secret com o Key ID (ex: s3_fotos_access_key).
func (l BucketLink) AccessKeySecret() string { return l.prefix() + "_access_key" }

// SecretKeySecret e o secret com a Secret key (ex: s3_fotos_secret_key).
func (l BucketLink) SecretKeySecret() string { return l.prefix() + "_secret_key" }

// Secrets sao os secrets que o cais grava pro bucket ligado.
func (l BucketLink) Secrets() []string { return []string{l.AccessKeySecret(), l.SecretKeySecret()} }

// BucketCredentials devolve o Key ID e a Secret key de um bucket do Garage.
type BucketCredentials func(name string) (keyID, secretKey string, err error)

// LinkBucket liga o app a um bucket. O app nao pode ter os secrets do
// bucket gravados a mao.
func (m Manager) LinkBucket(name string, l BucketLink) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	if m.BucketCredentials == nil {
		return errors.New("buckets do Garage indisponiveis")
	}
	id, secret, err := m.BucketCredentials(l.Name)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(a.Buckets, func(x BucketLink) bool { return x.Name == l.Name }) {
		return nil
	}
	if err := m.secretsFree(name, l.Secrets()); err != nil {
		return err
	}
	a.Buckets = append(a.Buckets, l)
	if err := m.save(a); err != nil {
		return err
	}
	return m.putSecrets(name, l.Secrets(), id, secret)
}

// UnlinkBucket desliga o app do bucket (o bucket e os arquivos ficam) e
// apaga os secrets dele; vale no proximo deploy.
func (m Manager) UnlinkBucket(name, bucket string) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(a.Buckets, func(x BucketLink) bool { return x.Name == bucket })
	if i < 0 {
		return fmt.Errorf("o app %s nao usa o bucket %s", name, bucket)
	}
	l := a.Buckets[i]
	a.Buckets = slices.Delete(a.Buckets, i, i+1)
	if err := m.save(a); err != nil {
		return err
	}
	return m.deleteSecrets(name, l.Secrets())
}
