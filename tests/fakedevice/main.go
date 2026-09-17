// tests/fakedevice/main.go
//
// Runs fake lab devices on loopback for probes that are not Go: capi_probe
// captures from them through the C surface. Not a tool -- it lives under tests/
// so the build scripts never ship it.
//
//	fakedevice    start the devices; print one "ready" line of JSON on stdout;
//	              run until stdout goes away
//
// Each device listens on 127.0.0.1 at a port the OS picks, so this needs no
// privileges and no extra addresses. The ready line:
//
//	{"ready":true,"user":"admin","password":"lab-password",
//	 "devices":[{"name":"lab-r1","addr":"127.0.0.1:53211","platform":"cisco_ios"},...]}
//
// It exits when stdout closes. A probe starts it with popen and never has to
// stop it: when the probe exits, the pipe closes, the next heartbeat write
// fails, and this process ends with it -- on POSIX and Windows alike, and even
// if the probe crashed.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/scottpeterman/omegacatqt/internal/fakedev"
)

// labARP is IOS "show ip arp" as the device prints it, so the probe can see a
// capture parsed end to end.
const labARP = `Protocol  Address          Age (min)  Hardware Addr   Type   Interface
Internet  172.16.1.1              -   0c1d.5e2f.0001  ARPA   GigabitEthernet0/0
Internet  172.16.1.2              3   0c1d.5e2f.0002  ARPA   GigabitEthernet0/0
Internet  172.16.2.1             12   0c1d.5e2f.0101  ARPA   GigabitEthernet0/1
`

const (
	user     = "admin"
	password = "lab-password"
)

type device struct {
	Name     string `json:"name"`
	Addr     string `json:"addr"`
	Platform string `json:"platform"`
}

func main() {
	type spec struct {
		cfg      fakedev.Config
		platform string
	}
	specs := []spec{
		{fakedev.IOS("lab-r1"), "cisco_ios"},
		{fakedev.EOS("lab-spine-1"), "arista_eos"},
	}

	ready := struct {
		Ready    bool     `json:"ready"`
		User     string   `json:"user"`
		Password string   `json:"password"`
		Devices  []device `json:"devices"`
	}{Ready: true, User: user, Password: password}

	for _, s := range specs {
		cfg := s.cfg
		if s.platform == "cisco_ios" {
			cfg.Commands["show ip arp"] = labARP
		}
		cfg.Username = user
		cfg.Password = password
		cfg.AcceptAnyPassword = false
		srv, err := fakedev.Start(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fakedevice: %v\n", err)
			os.Exit(3)
		}
		defer srv.Close()
		name := cfg.Prompt[:len(cfg.Prompt)-1]
		ready.Devices = append(ready.Devices, device{Name: name, Addr: srv.Addr(), Platform: s.platform})
	}

	line, err := json.Marshal(ready)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakedevice: %v\n", err)
		os.Exit(3)
	}
	if _, err := fmt.Println(string(line)); err != nil {
		os.Exit(0)
	}

	// Heartbeat: the only way to learn the reader is gone is to write to it.
	for {
		time.Sleep(200 * time.Millisecond)
		if _, err := os.Stdout.Write([]byte("\n")); err != nil {
			return
		}
	}
}
