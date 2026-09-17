// internal/sessions/session_test.go
//
// The invariants worth pinning are the ones that fail quietly: a secret
// reaching the file, a telnet node dialing 22, a deliberate false being
// normalized back to true.
package sessions

import (
	"strings"
	"testing"
)

func TestMarshalCarriesNoSecrets(t *testing.T) {
	n := Defaults()
	n.Name = "lab-r1"
	n.Host = "lab-r1.lab.example"
	n.Username = "admin"
	n.Credential = "lab-readonly"
	n.Password = "hunter2-should-never-be-written"
	n.KeyPassphrase = "passphrase-should-never-be-written"
	n.Jump.Host = "lab-jump1.lab.example"
	n.Jump.Password = "jump-secret-should-never-be-written"
	n.Jump.KeyPassphrase = "jump-passphrase-should-never-be-written"

	out, err := Marshal(n)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{
		n.Password, n.KeyPassphrase, n.Jump.Password, n.Jump.KeyPassphrase,
	} {
		if strings.Contains(string(out), secret) {
			t.Fatalf("secret material reached the session file:\n%s", out)
		}
	}
	// The reference must survive, or the node loads with no way to auth.
	if !strings.Contains(string(out), "lab-readonly") {
		t.Fatalf("credential reference missing from the file:\n%s", out)
	}
}

func TestTelnetKeepsItsOwnDefaultPort(t *testing.T) {
	n, err := Unmarshal([]byte("name: lab-console1\ntransport: telnet\nhost: lab-console1.lab.example\n"))
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if n.Port != 23 {
		t.Fatalf("telnet default port = %d, want 23", n.Port)
	}
}

func TestSSHDefaultPort(t *testing.T) {
	n, err := Unmarshal([]byte("name: lab-r1\ntransport: ssh\nhost: lab-r1.lab.example\n"))
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if n.Port != 22 {
		t.Fatalf("ssh default port = %d, want 22", n.Port)
	}
}

func TestExplicitPortSurvives(t *testing.T) {
	// A container console on a non-standard port is the case the whole
	// Port field exists for.
	n, err := Unmarshal([]byte("transport: ssh\nhost: lab-r1.lab.example\nport: 2201\n"))
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if n.Port != 2201 {
		t.Fatalf("port = %d, want 2201", n.Port)
	}
	if got := n.Target(); got != "lab-r1.lab.example:2201" {
		t.Fatalf("target = %q", got)
	}
}

func TestDeliberateCRLFFalseSurvivesRoundTrip(t *testing.T) {
	n := Defaults()
	n.Transport = TransportTelnet
	n.Host = "lab-console1.lab.example"
	n.SetCRLF(false)

	out, err := Marshal(n)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := Unmarshal(out)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.CRLF() {
		t.Fatalf("telnet CRLF came back true; a deliberate false was lost:\n%s", out)
	}
}

func TestUnsetCRLFDefaultsOn(t *testing.T) {
	n, err := Unmarshal([]byte("transport: telnet\nhost: lab-console1.lab.example\n"))
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !n.CRLF() {
		t.Fatal("unset telnet CRLF should default to on")
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	n := Defaults()
	n.Transport = TransportSSH
	n.Host = ""
	n.Port = 0
	n.Username = ""
	n.AuthType = "magic-beans"

	errs := n.Validate()
	if len(errs) < 3 {
		t.Fatalf("want at least 3 field errors, got %d: %v", len(errs), errs)
	}
	seen := map[string]bool{}
	for _, e := range errs {
		seen[e.Field] = true
	}
	for _, want := range []string{"host", "username", "auth_type"} {
		if !seen[want] {
			t.Errorf("no error reported for %q; got %v", want, errs)
		}
	}
	// Port 0 is filled in by Normalize, so it is not an error.
	if seen["port"] {
		t.Errorf("port 0 should normalize to the transport default, not fail")
	}
}

func TestValidateIgnoresFieldsTheTransportDoesNotUse(t *testing.T) {
	// An SSH node carrying a nonsense baud rate is a leftover from when
	// the node was serial, not an error.
	n := Defaults()
	n.Host = "lab-r1.lab.example"
	n.Username = "admin"
	n.Baud = -1
	n.Parity = "quantum"
	if errs := n.Validate(); len(errs) != 0 {
		t.Fatalf("ssh node failed on serial fields: %v", errs)
	}

	n.Transport = TransportSerial
	n.SerialPort = "/dev/ttyUSB0"
	errs := n.Validate()
	if len(errs) != 2 {
		t.Fatalf("serial node: want 2 errors (baud, parity), got %v", errs)
	}
}

func TestSerialNodeNeedsNoHost(t *testing.T) {
	n := Defaults()
	n.Transport = TransportSerial
	n.SerialPort = "/dev/ttyUSB0"
	if errs := n.Validate(); len(errs) != 0 {
		t.Fatalf("serial node required network fields: %v", errs)
	}
	if got := n.Target(); got != "/dev/ttyUSB0 9600 8N1" {
		t.Fatalf("target = %q", got)
	}
}

func TestVaultCredentialSubstitutesForUsernameAndKey(t *testing.T) {
	n := Defaults()
	n.Host = "lab-r1.lab.example"
	n.AuthType = AuthPublicKey
	n.Credential = "lab-readonly"
	if errs := n.Validate(); len(errs) != 0 {
		t.Fatalf("a vault reference should cover username and key path: %v", errs)
	}
}

func TestInsecureHostKeyIsNeverTheDefault(t *testing.T) {
	if Defaults().HostKeyPolicy != HostKeyTOFU {
		t.Fatalf("default host-key policy = %q, want %q",
			Defaults().HostKeyPolicy, HostKeyTOFU)
	}
}

func TestAntiIdleThreeStates(t *testing.T) {
	cases := []struct {
		mode      AntiIdleMode
		wantValue bool
		wantSet   bool
	}{
		{AntiIdleInherit, false, false},
		{AntiIdleOn, true, true},
		{AntiIdleOff, false, true},
	}
	for _, c := range cases {
		v, set := AntiIdleSpec{Mode: c.mode}.Enabled()
		if v != c.wantValue || set != c.wantSet {
			t.Errorf("%q => (%v,%v), want (%v,%v)", c.mode, v, set, c.wantValue, c.wantSet)
		}
	}
}

func TestAntiIdleIntervalFloor(t *testing.T) {
	n := Defaults()
	n.Host = "lab-r1.lab.example"
	n.Username = "admin"
	n.AntiIdle = AntiIdleSpec{Mode: AntiIdleOn, IntervalSec: 3}
	errs := n.Validate()
	if len(errs) != 1 || errs[0].Field != "anti_idle.interval_sec" {
		t.Fatalf("want one interval error, got %v", errs)
	}
}

func TestNameFallsBackToTarget(t *testing.T) {
	n := Defaults()
	n.Host = "lab-r1.lab.example"
	n.Username = "admin"
	n = n.Normalize()
	if n.Name != "admin@lab-r1.lab.example" {
		t.Fatalf("name = %q", n.Name)
	}
}

func TestUnknownKeysDoNotFailTheLoad(t *testing.T) {
	// An older file, or a newer one, must still open.
	n, err := Unmarshal([]byte("transport: ssh\nhost: lab-r1.lab.example\nsome_future_key: 42\n"))
	if err != nil {
		t.Fatalf("unmarshal rejected an unknown key: %v", err)
	}
	if n.Host != "lab-r1.lab.example" {
		t.Fatalf("host = %q", n.Host)
	}
}

// A new session is created for a device, and a device does not run an SSH
// agent. Blank-in-a-file still means agent, which is a different statement.
func TestANewSessionDefaultsToPasswordAuth(t *testing.T) {
	if got := Defaults().AuthType; got != AuthPassword {
		t.Errorf("Defaults().AuthType = %q, want %q", got, AuthPassword)
	}
	if got := (Node{}).Normalize().AuthType; got != AuthAgent {
		t.Errorf("a blank node normalizes to %q, want %q", got, AuthAgent)
	}
}
