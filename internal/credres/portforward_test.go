package credres

import "testing"

// Two devices behind port-forwards on one host must stay two records, and an
// address with a port must never outrank a real name as the label.
func TestPortForwardedTargetsAreSeparateRecords(t *testing.T) {
	b := NewMemoryBindings()
	if err := b.Record("cred-a", "127.0.0.1:2201"); err != nil {
		t.Fatal(err)
	}
	if err := b.Record("cred-b", "127.0.0.1:2202"); err != nil {
		t.Fatal(err)
	}
	if n := b.Len(); n != 2 {
		t.Fatalf("%d records for two port-forwarded devices, want 2", n)
	}
	a, _ := b.Resolve("127.0.0.1:2201")
	for _, alias := range a.Aliases {
		if alias == "127" || alias == "127.0.0.1:2202" {
			t.Errorf("record for :2201 carries alias %q: %v", alias, a.Aliases)
		}
	}

	if err := b.Bind("lab-r1", "127.0.0.1:2201"); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.Resolve("127.0.0.1:2201"); got.Canonical != "lab-r1" || got.CredID != "cred-a" {
		t.Errorf("after binding the prompt name: %+v; want label lab-r1, pin cred-a", got)
	}
}
