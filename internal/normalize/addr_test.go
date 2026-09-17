package normalize

import "testing"

func TestAddrOfAcceptsAddressesWithAndWithoutPorts(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"172.16.1.2", true},
		{"172.16.1.2:2222", true},
		{"2001:db8::1", true},
		{"[2001:db8::1]:22", true},
		{"lab-r1", false},
		{"lab-r1.lab.local", false},
		{"lab-r1:2222", false},
		{"", false},
	} {
		if _, got := AddrOf(tc.in); got != tc.want {
			t.Errorf("AddrOf(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// The short name of an address with a port is the whole thing. Splitting
// 127.0.0.1:2201 at the first dot gives "127", shared by every port-forwarded
// device on the host.
func TestShortNameKeepsAnAddressWithAPortWhole(t *testing.T) {
	for _, in := range []string{"127.0.0.1:2201", "[2001:db8::1]:22", "172.16.1.2"} {
		if got := ShortName(in); got != in {
			t.Errorf("ShortName(%q) = %q, want it whole", in, got)
		}
	}
	if got := ShortName("lab-r1.lab.local"); got != "lab-r1" {
		t.Errorf("ShortName(lab-r1.lab.local) = %q, want lab-r1", got)
	}
}
