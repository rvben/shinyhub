package deploy

import "testing"

func TestValidateCommandTemplate(t *testing.T) {
	cases := []struct {
		name    string
		cmd     []string
		wantErr bool
	}{
		{"all known placeholders", []string{"x", "{port}", "{host}", "{data_dir}"}, false},
		{"unknown placeholder", []string{"x", "{prot}"}, true},
		{"unknown lowercase word", []string{"{foo}"}, true},
		{"uppercase inert", []string{"${VAR}", "{X}"}, false},
		{"empty element", []string{"x", ""}, true},
		{"empty command", []string{}, true},
	}
	for _, c := range cases {
		err := validateCommandTemplate(c.cmd)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, wantErr = %v", c.name, err, c.wantErr)
		}
	}
}

func TestSubstituteCommand_PerReplicaFreshSlice(t *testing.T) {
	tpl := []string{"serve", "--port", "{port}", "--host", "{host}", "--data", "{data_dir}"}
	a := substituteCommand(tpl, 5001, "127.0.0.1")
	b := substituteCommand(tpl, 5002, "0.0.0.0")
	if a[2] != "5001" || b[2] != "5002" {
		t.Fatalf("ports: %v / %v", a, b)
	}
	if a[4] != "127.0.0.1" || b[4] != "0.0.0.0" {
		t.Fatalf("hosts: %v / %v", a, b)
	}
	if a[6] != "data" || b[6] != "data" {
		t.Fatalf("data_dir: %v / %v", a, b)
	}
	if tpl[2] != "{port}" {
		t.Fatal("template must never be mutated")
	}
}
