package docker

import "testing"

func TestParseStats(t *testing.T) {
	out := `{"BlockIO":"0B / 0B","CPUPerc":"12.50%","Container":"a1","ID":"a1","MemPerc":"1.00%","MemUsage":"512MiB / 16GiB","Name":"medko-web-1","NetIO":"1kB / 2kB","PIDs":"5"}
{"CPUPerc":"150.00%","MemUsage":"1.5GiB / 2GiB","Name":"postgres"}
{"CPUPerc":"--","MemUsage":"0B / 0B","Name":"subindo"}
`
	list, err := parseStats(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("%+v", list)
	}
	if l := list[0]; l.Name != "medko-web-1" || l.CPU != 12.5 || l.Mem != 512<<20 || l.MemLimit != 16<<30 {
		t.Errorf("primeiro: %+v", l)
	}
	if l := list[1]; l.CPU != 150 || l.Mem != 1536<<20 {
		t.Errorf("segundo: %+v", l)
	}
	if l := list[2]; l.CPU != 0 || l.Mem != 0 {
		t.Errorf("sem medida: %+v", l)
	}
	if _, err := parseStats("nao json"); err == nil {
		t.Error("aceitou saida quebrada")
	}
	if list, err := parseStats(""); err != nil || len(list) != 0 {
		t.Errorf("vazio: %v %v", list, err)
	}
}

func TestParseBytes(t *testing.T) {
	for in, want := range map[string]uint64{"800kB": 800000, "1.5GiB": 1536 << 20, "0B": 0, " 2MB ": 2000000, "x": 0} {
		if got := parseBytes(in); got != want {
			t.Errorf("%q: %d, queria %d", in, got, want)
		}
	}
}
