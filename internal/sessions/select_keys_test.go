package sessions

import "testing"

// Two devices behind one address, told apart only by port: a pattern on the
// address takes both, a key takes the one it names.
func TestSelectKeysTellsPortForwardedDevicesApart(t *testing.T) {
	var tr Tree
	for _, n := range []Node{
		{Name: "lab-r1", Transport: TransportSSH, Host: "127.0.0.1", Port: 2201},
		{Name: "lab-spine-1", Transport: TransportSSH, Host: "127.0.0.1", Port: 2202},
		{Name: "lab-r2", Transport: TransportSSH, Host: "172.16.1.2"},
	} {
		if err := tr.Add("Lab", n.Normalize()); err != nil {
			t.Fatal(err)
		}
	}
	if got := tr.Select([]string{"127.0.0.1"}); len(got) != 2 {
		t.Fatalf("precondition: the address pattern should take both forwarded devices, got %d", len(got))
	}

	got := tr.SelectKeys([]string{"SSH:127.0.0.1:2202", "ssh:172.16.1.2:22", "ssh:10.9.9.9:22"})
	if len(got) != 2 || got[0].Node.Name != "lab-spine-1" || got[1].Node.Name != "lab-r2" {
		t.Fatalf("want lab-spine-1 then lab-r2 in tree order, got %+v", got)
	}
	if tr.SelectKeys(nil) != nil || tr.SelectKeys([]string{"  "}) != nil {
		t.Fatal("no keys must select nothing")
	}
}
