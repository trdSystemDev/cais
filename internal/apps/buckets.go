package apps

import (
	"errors"
	"fmt"
	"slices"
)

// BucketLink liga o app a um bucket do Garage. No deploy o cais entrega ao
// app as variaveis S3_<BUCKET>_ACCESS_KEY, _SECRET_KEY e _ENDPOINT (- do
// nome vira _).
type BucketLink struct {
	Name string `yaml:"name" json:"name"`
}

// ID e como o cais mostra o bucket (s3/fotos).
func (l BucketLink) ID() string { return "s3/" + l.Name }

// GarageEndpoint e o S3 do Garage na garage_network (regiao garage,
// path-style).
const GarageEndpoint = "http://garage:3900"

// Vars sao os nomes das variaveis que o cais entrega pro bucket ligado (ex:
// S3_FOTOS_ACCESS_KEY, S3_FOTOS_SECRET_KEY, S3_FOTOS_ENDPOINT).
func (l BucketLink) Vars() []string {
	p := envPrefix("s3_" + l.Name)
	return []string{p + "_ACCESS_KEY", p + "_SECRET_KEY", p + "_ENDPOINT"}
}

func (l BucketLink) env(keyID, secretKey string) map[string]string {
	v := l.Vars()
	return map[string]string{v[0]: keyID, v[1]: secretKey, v[2]: GarageEndpoint}
}

// BucketCredentials devolve o Key ID e a Secret key de um bucket do Garage.
type BucketCredentials func(name string) (keyID, secretKey string, err error)

// LinkBucket liga o app a um bucket; vale no proximo deploy. O app nao pode
// ter variavel com os nomes das do bucket.
func (m Manager) LinkBucket(name string, l BucketLink) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	if m.BucketCredentials == nil {
		return errors.New("buckets do Garage indisponiveis")
	}
	if _, _, err := m.BucketCredentials(l.Name); err != nil {
		return err
	}
	if slices.ContainsFunc(a.Buckets, func(x BucketLink) bool { return x.Name == l.Name }) {
		return nil
	}
	if err := m.varsFree(name, l.Vars()); err != nil {
		return err
	}
	a.Buckets = append(a.Buckets, l)
	return m.save(a)
}

// UnlinkBucket desliga o app do bucket (o bucket e os arquivos ficam); as
// variaveis dele saem no proximo deploy.
func (m Manager) UnlinkBucket(name, bucket string) error {
	a, err := m.Get(name)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(a.Buckets, func(x BucketLink) bool { return x.Name == bucket })
	if i < 0 {
		return fmt.Errorf("o app %s nao usa o bucket %s", name, bucket)
	}
	a.Buckets = slices.Delete(a.Buckets, i, i+1)
	return m.save(a)
}
