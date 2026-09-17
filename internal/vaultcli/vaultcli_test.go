// internal/vaultcli/vaultcli_test.go
package vaultcli

import "testing"

func TestBindingsPath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/home/u/.omegacat/vault.json", "/home/u/.omegacat/vault.bindings.json"},
		{"/home/u/.omegacat/vault", "/home/u/.omegacat/vault.bindings.json"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := BindingsPath(tc.in); got != tc.want {
			t.Errorf("BindingsPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
