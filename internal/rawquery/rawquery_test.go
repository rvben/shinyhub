package rawquery

import "testing"

func TestPlatformParameterEditsPreserveAppBytes(t *testing.T) {
	const app = "_inputs_&q=a%20b&x=%2f&x=2&state=a;b&bad=%ZZ&&"
	for _, raw := range []string{app, "platform=old&" + app, app + "&plat%66orm=old", "platform=old&" + app + "&platform=other"} {
		if got := Delete(raw, "platform"); got != app {
			t.Errorf("Delete(%q)=%q", raw, got)
		}
		if got := Set(raw, "platform", "a b"); got != app+"&platform=a+b" {
			t.Errorf("Set(%q)=%q", raw, got)
		}
	}
	if got := Set("", "platform", "1"); got != "platform=1" {
		t.Fatal(got)
	}
}
