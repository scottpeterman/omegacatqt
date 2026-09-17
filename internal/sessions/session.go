// internal/sessions/session.go
//
// What one saved connection is, as data.
//
// This is the model the connection manager edits and the dial layer consumes.
// It imports no toolkit, which is the whole point: every rule about what a
// session may contain — which fields a transport needs, what a legal baud rate
// is, what must never be written to disk — is testable without a display, and
// the same rules apply whether the node came from the dialog, from a YAML
// file, or from a map import.
//
// # One node, three transports
//
// SSH, telnet and serial are one type rather than three. The terminal already
// treats them as one (they all satisfy term.Transport), and splitting the model
// would mean the form, the tree and the dial layer each carrying a three-way
// switch over types that share most of their fields. The Transport field says
// which subset is live; Validate only checks the fields that transport uses.
//
// # Secrets are not part of the file
//
// Password and KeyPassphrase are `yaml:"-"`. A session file carries a
// credential *reference* — a vault name or id — and nothing else that would be
// a secret if it leaked. The two secret fields exist so an ad-hoc connection
// typed into the dialog can be dialed without inventing a second struct; they
// live for as long as the process does and are never marshalled.
//
// A credential reference is stored by NAME wherever the user gave one. An
// opaque vault id refers to a row that does not exist on another machine,
// while a name either resolves there or produces a message that means
// something. The vault's own lookup accepts either, so a node written by an
// older tool still resolves.
package sessions

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Transport is which of the three connection types a node uses.
type Transport string

const (
	TransportSSH    Transport = "ssh"
	TransportTelnet Transport = "telnet"
	TransportSerial Transport = "serial"
)

// Transports is the display order, and the set Validate accepts.
var Transports = []Transport{TransportSSH, TransportTelnet, TransportSerial}

// IsNetwork reports whether this transport dials a host and port. Serial does
// not, which is the only structural difference between the three.
func (t Transport) IsNetwork() bool {
	return t == TransportSSH || t == TransportTelnet
}

// DefaultPort is the port this transport uses when none was given. Serial has
// none and returns 0.
func (t Transport) DefaultPort() int {
	switch t {
	case TransportSSH:
		return 22
	case TransportTelnet:
		return 23
	default:
		return 0
	}
}

// Auth types. AuthAgent means the SSH agent, then whatever the dial layer's
// prompt offers; it is the sensible default for an interactive session because
// it needs no fields filled in at all.
const (
	AuthPassword  = "password"
	AuthPublicKey = "publickey"
	AuthAgent     = "agent"
)

// AuthTypes is the display order for the auth selector.
var AuthTypes = []string{AuthAgent, AuthPassword, AuthPublicKey}

// Host-key policy names. These are the session-level spelling of
// sshcore.HostKeyPolicy; the mapping lives in the dial layer so this package
// stays free of the SSH stack.
//
// Insecure is deliberately offered. Lab gear gets rebuilt, and a policy that
// cannot be relaxed for a device you own teaches people to answer yes on
// reflex to the one prompt that matters. What it must never become is the
// default: a key that CHANGED is a different event from a key never seen, and
// only Insecure stops distinguishing them.
const (
	HostKeyStrict   = "strict"
	HostKeyTOFU     = "tofu"
	HostKeyInsecure = "insecure"
)

// HostKeyPolicies is the display order for the policy selector.
var HostKeyPolicies = []string{HostKeyTOFU, HostKeyStrict, HostKeyInsecure}

// Serial framing choices, as offered in the form.
var (
	BaudRates = []int{300, 1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200}
	DataBits  = []int{5, 6, 7, 8}
	Parities  = []string{"none", "odd", "even", "mark", "space"}
	StopBits  = []string{"1", "1.5", "2"}
)

// AntiIdleMode is a three-state per-session override: take the application
// setting, or force it on or off for this device. Two-state would lose the
// difference between "off" and "not specified", and the device that needs it
// forced is exactly the one behind an exec-timeout nobody else hits.
type AntiIdleMode string

const (
	AntiIdleInherit AntiIdleMode = "inherit"
	AntiIdleOn      AntiIdleMode = "on"
	AntiIdleOff     AntiIdleMode = "off"
)

// AntiIdleModes is the display order for the anti-idle selector.
var AntiIdleModes = []AntiIdleMode{AntiIdleInherit, AntiIdleOn, AntiIdleOff}

// AntiIdleSpec is the per-session anti-idle setting.
type AntiIdleSpec struct {
	Mode AntiIdleMode `yaml:"mode,omitempty"`

	// IntervalSec overrides the application interval when non-zero. Zero
	// means inherit the interval even when Mode forces enabled state.
	IntervalSec int `yaml:"interval_sec,omitempty"`
}

// Enabled reports the override value and whether one was set at all.
func (a AntiIdleSpec) Enabled() (value, set bool) {
	switch a.Mode {
	case AntiIdleOn:
		return true, true
	case AntiIdleOff:
		return false, true
	default:
		return false, false
	}
}

// JumpSpec is a single bastion hop in front of an SSH session.
//
// One hop, not a chain: the crawler's jump layer resolves chains from a
// route-map, and a hand-edited session file that needed three hops would be
// re-implementing that badly. A node that needs a chain names a route-map
// target instead, which is a later feature and not this field.
type JumpSpec struct {
	Host     string `yaml:"host,omitempty"`
	Port     int    `yaml:"port,omitempty"`
	Username string `yaml:"username,omitempty"`

	// Credential is a vault reference, same rules as Node.Credential.
	Credential string `yaml:"credential,omitempty"`
	KeyPath    string `yaml:"key_path,omitempty"`

	// Secrets, never marshalled. See the package comment. The json tag is
	// not decoration: internal/ui persists a quick-connect node to
	// launches.json, and an untagged field would be written under its Go
	// name.
	Password      string `yaml:"-" json:"-"`
	KeyPassphrase string `yaml:"-" json:"-"`
}

// InUse reports whether a bastion was configured. An empty host means direct.
func (j JumpSpec) InUse() bool { return strings.TrimSpace(j.Host) != "" }

// Node is one saved connection.
//
// Field order here is the order the form presents them, which is deliberate:
// when the two drift, the field that gets forgotten in the dialog is the one
// nobody can see is missing.
type Node struct {
	Name      string    `yaml:"name"`
	Transport Transport `yaml:"transport"`

	// Ephemeral marks a connection that was typed rather than saved. It is
	// the entire difference between "quick connect" and "edit session":
	// the same form, the same model, one flag. TetherSSH grew two dialogs
	// for this and they drifted in features.
	Ephemeral bool `yaml:"-"`

	// --- network transports (ssh, telnet) ---

	Host string `yaml:"host,omitempty"`

	// Port is carried explicitly rather than being folded into Host. A
	// hand-written session file is exactly where someone writes
	// host:2201 for a container console, and a model that only has a host
	// string turns that into a DNS lookup for a name with a colon in it.
	Port int `yaml:"port,omitempty"`

	// --- ssh ---

	Username string `yaml:"username,omitempty"`

	// Credential references a vault entry by name (preferred) or id. It is
	// resolved at dial time, and a reference that resolves to nothing must
	// load fine and simply not connect yet — never an import error.
	Credential string `yaml:"credential,omitempty"`

	AuthType string `yaml:"auth_type,omitempty"`
	KeyPath  string `yaml:"key_path,omitempty"`

	// Secrets, never marshalled. See the package comment, and the note on
	// JumpSpec's pair about the json tag.
	Password      string `yaml:"-" json:"-"`
	KeyPassphrase string `yaml:"-" json:"-"`

	Jump JumpSpec `yaml:"jump,omitempty"`

	// LegacyAlgorithms appends the old KEX/cipher/MAC set. Required for
	// gear that predates the modern defaults, off by default.
	LegacyAlgorithms bool `yaml:"legacy_algorithms,omitempty"`

	// Platform is the platform a person set for this device, and when it is
	// set it is the authority: capture uses it instead of classifying the
	// device, while still disabling that platform's paging. Only an editor
	// writes it -- an import never does -- so a value here was chosen, not
	// guessed. Empty means detect. DeviceType below is the guess.
	Platform string `yaml:"platform,omitempty"`

	HostKeyPolicy  string `yaml:"host_key_policy,omitempty"`
	KnownHostsPath string `yaml:"known_hosts,omitempty"`

	ConnectTimeoutSec int `yaml:"connect_timeout_sec,omitempty"`

	// --- telnet ---

	// TelnetCRLF expands a lone CR to CR LF on write. A pointer because
	// the useful default is true, and a plain bool cannot tell "false on
	// purpose" from "absent from the file" — which is the same reason
	// telnetx.Config carries its own crlfSet flag. nil means the default.
	// Read it through CRLF().
	TelnetCRLF *bool `yaml:"telnet_crlf,omitempty"`

	// --- serial ---

	SerialPort string `yaml:"serial_port,omitempty"`
	Baud       int    `yaml:"baud,omitempty"`
	DataBits   int    `yaml:"data_bits,omitempty"`
	Parity     string `yaml:"parity,omitempty"`
	StopBits   string `yaml:"stop_bits,omitempty"`

	// --- terminal ---

	// TermType is the TERM value requested (SSH) or answered to a telnet
	// TTYPE subnegotiation. Serial ignores it: there is nobody to tell.
	TermType string `yaml:"term_type,omitempty"`

	// TerminalTheme names a palette from the theme registry. Empty means
	// the application default. The application chrome is NOT set here —
	// chrome is light or dark for the whole app, and a per-session chrome
	// would repaint the window when you changed tabs.
	TerminalTheme string `yaml:"terminal_theme,omitempty"`

	FontSize        int `yaml:"font_size,omitempty"`
	ScrollbackLines int `yaml:"scrollback_lines,omitempty"`

	// PasteLineDelayMs paces multi-line paste, in milliseconds between
	// lines. Zero inherits the application setting; NEGATIVE means no
	// pacing on this session, which is the only way to say that once the
	// application default is non-zero.
	PasteLineDelayMs int `yaml:"paste_line_delay_ms,omitempty"`

	// PasteWarnLines is the line count at or above which a paste to this
	// session asks for confirmation. Zero inherits the application
	// setting; NEGATIVE means never ask for this device, which is the only
	// way to say that once the application default is non-zero. A negative
	// value is deliberately not a validation error — a console server or a
	// jump box somebody pastes into all day is a legitimate thing to
	// exempt.
	PasteWarnLines int `yaml:"paste_warn_lines,omitempty"`

	// ConsoleBaud is the speed of the ASYNC LINE on the far side of this
	// session, and it exists for reverse-telnet console access.
	//
	// The connection to a console server is TCP and arrives at LAN speed;
	// the console server then clocks those bytes onto a serial line at
	// this rate, with no flow control back to us. Its buffer is small, so
	// a single long line pasted in one write overruns it and the excess is
	// dropped — silently, and looking exactly like a device that mangled
	// the command. Setting this paces a paste to the rate the line can
	// actually carry.
	//
	// Zero means send at full speed, which is right for SSH and for a
	// terminal server that buffers properly. It is deliberately separate
	// from Baud above: that one CONFIGURES a local serial port, this one
	// only describes a line somebody else owns.
	//
	// NEGATIVE means full speed EXPLICITLY, following the same convention
	// as PasteLineDelayMs. It matters only on a serial session, where zero
	// falls back to Baud (see PasteBaud) and so cannot express "this port
	// is fine at full speed".
	ConsoleBaud int `yaml:"console_baud,omitempty"`

	LogEnabled bool `yaml:"log_enabled,omitempty"`

	AntiIdle AntiIdleSpec `yaml:"anti_idle,omitempty"`

	// --- inventory metadata ---
	//
	// Free text, never used to pick a code path. A crawl import fills these
	// in. DeviceType is the crawl's platform guess: capture compares it with
	// what the device reports and says so when they differ, and a re-import
	// may refresh it. The platform capture acts on is Platform above when a
	// person set one, and the live fingerprint otherwise.
	Vendor     string `yaml:"vendor,omitempty"`
	Model      string `yaml:"model,omitempty"`
	DeviceType string `yaml:"device_type,omitempty"`
	Notes      string `yaml:"notes,omitempty"`
}

// PasteBaud is the line speed a paste to this session must be paced at, or
// zero for full speed.
//
// A serial session already carries its speed in Baud, so it is used when
// ConsoleBaud says nothing: the port is the async line in that case, and
// making somebody configure the same number twice is how the two end up
// disagreeing. For every other transport the only source is ConsoleBaud,
// because nothing about a TCP connection reveals what is on the other side of
// the terminal server.
func (n Node) PasteBaud() int {
	if n.ConsoleBaud < 0 {
		return 0
	}
	if n.ConsoleBaud > 0 {
		return n.ConsoleBaud
	}
	if n.Transport == TransportSerial && n.Baud > 0 {
		return n.Baud
	}
	return 0
}

// CRLF resolves the telnet newline setting, defaulting to on.
func (n Node) CRLF() bool { return n.TelnetCRLF == nil || *n.TelnetCRLF }

// SetCRLF records an explicit choice, so a deliberate false survives a
// round trip through YAML and through Normalize.
func (n *Node) SetCRLF(v bool) { n.TelnetCRLF = &v }

// Defaults returns a node for a session being created now: SSH, port 22,
// PASSWORD auth, trust on first use.
//
// Password rather than agent because of what a new session usually is. The
// dialog is reached fastest by Quick Connect, Quick Connect is aimed at a
// device rather than a server, and a switch does not run an SSH agent -- so
// the agent default meant choosing the selector every single time, and the
// failure when it was forgotten was an authentication error rather than a
// visible mistake.
//
// Normalize still fills a BLANK auth type with agent, and the difference is
// deliberate: a saved file that never mentions auth is not making the same
// statement as a dialog somebody just opened, and changing what an existing
// file means is not a default change, it is a migration.
func Defaults() Node {
	return Node{AuthType: AuthPassword}.Normalize()
}

// Normalize fills in anything left blank and trims whitespace, so a node
// loaded from a hand-edited file behaves the same as one built by Defaults.
//
// It does not correct bad values into good ones — an unusable baud rate stays
// unusable and Validate reports it. Silently repairing input is how a form
// ends up saving something the user did not choose.
func (n Node) Normalize() Node {
	n.Name = strings.TrimSpace(n.Name)
	n.Host = strings.TrimSpace(n.Host)
	n.Username = strings.TrimSpace(n.Username)
	n.Credential = strings.TrimSpace(n.Credential)
	n.Platform = strings.TrimSpace(n.Platform)
	n.KeyPath = strings.TrimSpace(n.KeyPath)
	n.SerialPort = strings.TrimSpace(n.SerialPort)
	n.KnownHostsPath = strings.TrimSpace(n.KnownHostsPath)
	n.Jump.Host = strings.TrimSpace(n.Jump.Host)
	n.Jump.Username = strings.TrimSpace(n.Jump.Username)
	n.Jump.Credential = strings.TrimSpace(n.Jump.Credential)
	n.Jump.KeyPath = strings.TrimSpace(n.Jump.KeyPath)

	if n.Transport == "" {
		n.Transport = TransportSSH
	}
	if n.Transport.IsNetwork() && n.Port == 0 {
		n.Port = n.Transport.DefaultPort()
	}
	// Everything below is filled in regardless of the chosen transport.
	// The form shows one transport at a time but holds all three sets of
	// fields, and a hidden group that reads 0 baud becomes a visible group
	// that reads 0 baud the moment somebody changes the selector.
	if n.AuthType == "" {
		n.AuthType = AuthAgent
	}
	if n.HostKeyPolicy == "" {
		n.HostKeyPolicy = HostKeyTOFU
	}
	if n.ConnectTimeoutSec == 0 {
		n.ConnectTimeoutSec = 20
	}
	if n.Jump.InUse() && n.Jump.Port == 0 {
		n.Jump.Port = 22
	}
	if n.Baud == 0 {
		n.Baud = 9600
	}
	if n.DataBits == 0 {
		n.DataBits = 8
	}
	if n.Parity == "" {
		n.Parity = "none"
	}
	if n.StopBits == "" {
		n.StopBits = "1"
	}
	// FontSize and ScrollbackLines are deliberately NOT filled in. Zero
	// means "inherit the application setting", and there is no way to say
	// that once a default has been written into the node — a session
	// saved today would pin 12pt forever and stop following the app.
	if n.TermType == "" {
		n.TermType = "xterm-256color"
	}
	if n.AntiIdle.Mode == "" {
		n.AntiIdle.Mode = AntiIdleInherit
	}
	if n.Name == "" {
		n.Name = n.Label()
	}
	return n
}

// Label is the human name for this node: the given name if it has one, and
// otherwise what it connects to. A quick connect with no name still shows
// something recognisable in a tab and in a log line.
func (n Node) Label() string {
	if s := strings.TrimSpace(n.Name); s != "" {
		return s
	}
	return n.Target()
}

// Target describes the destination in one line, in the shape that transport is
// usually written down: user@host:port, host:port, or the serial framing.
func (n Node) Target() string {
	switch n.Transport {
	case TransportSerial:
		if n.SerialPort == "" {
			return "serial"
		}
		return fmt.Sprintf("%s %d %d%s%s", n.SerialPort, n.Baud, n.DataBits,
			parityLetter(n.Parity), n.StopBits)
	case TransportTelnet:
		return joinHostPort(n.Host, n.Port, TransportTelnet.DefaultPort())
	default:
		hp := joinHostPort(n.Host, n.Port, TransportSSH.DefaultPort())
		if n.Username != "" {
			return n.Username + "@" + hp
		}
		return hp
	}
}

func parityLetter(p string) string {
	if p == "" {
		return "N"
	}
	return strings.ToUpper(p[:1])
}

func joinHostPort(host string, port, def int) string {
	if host == "" {
		return ""
	}
	if port == 0 || port == def {
		return host
	}
	return host + ":" + strconv.Itoa(port)
}

// FieldError names the field that is wrong and why.
type FieldError struct {
	Field   string
	Message string
}

func (e FieldError) Error() string { return e.Field + ": " + e.Message }

// Validate reports EVERY problem rather than the first one, each tagged with
// the field it belongs to, so a form can mark all the bad inputs at once. A
// validator that returns one error at a time turns filling in a dialog into a
// guessing game played one round trip per mistake.
//
// It checks only the fields the chosen transport uses. A leftover serial baud
// rate on an SSH node is not an error; it is a field the user last touched
// when this node was something else.
func (n Node) Validate() []FieldError { return n.ValidateFor(false) }

// ValidateFor is Validate with one thing the node cannot know: whether the
// caller has a credential store that supplies a DEFAULT for sessions naming
// none.
//
// It exists because the two auth rules below decide on Credential != "", and
// an imported node has neither a username nor a credential -- so with a
// default available, Validate rejected at the door exactly the nodes the
// default was built to serve, before anything had a chance to look one up.
//
// Passing true DEFERS those two rules; it does not delete them. The gap is
// caught after the lookup, where the answer is actually known, and reported as
// "no username" rather than as an authentication failure.
func (n Node) ValidateFor(credentialDefault bool) []FieldError {
	n = n.Normalize()
	var errs []FieldError
	add := func(field, msg string) { errs = append(errs, FieldError{field, msg}) }

	// authMayComeFromTheStore is true when something other than this node
	// might supply the username or the key: a credential it names, or a
	// default standing behind a blank field.
	authMayComeFromTheStore := n.Credential != "" || credentialDefault

	if !knownTransport(n.Transport) {
		add("transport", fmt.Sprintf("unknown transport %q", n.Transport))
		return errs
	}

	if n.Transport.IsNetwork() {
		if n.Host == "" {
			add("host", "required")
		} else if strings.ContainsAny(n.Host, " \t") {
			add("host", "contains whitespace")
		}
		if n.Port < 1 || n.Port > 65535 {
			add("port", "must be 1-65535")
		}
	}

	if n.Transport == TransportSSH {
		switch n.AuthType {
		case AuthAgent, AuthPassword:
		case AuthPublicKey:
			if n.KeyPath == "" && !authMayComeFromTheStore {
				add("key_path", "required for public-key auth unless a vault credential supplies one")
			}
		default:
			add("auth_type", fmt.Sprintf("unknown auth type %q", n.AuthType))
		}
		if n.Username == "" && !authMayComeFromTheStore {
			add("username", "required unless a vault credential supplies one")
		}
		switch n.HostKeyPolicy {
		case HostKeyStrict, HostKeyTOFU, HostKeyInsecure:
		default:
			add("host_key_policy", fmt.Sprintf("unknown policy %q", n.HostKeyPolicy))
		}
		if n.ConnectTimeoutSec < 1 {
			add("connect_timeout_sec", "must be at least 1")
		}
		if n.Jump.InUse() {
			if n.Jump.Port < 1 || n.Jump.Port > 65535 {
				add("jump.port", "must be 1-65535")
			}
			if n.Jump.Username == "" && n.Jump.Credential == "" {
				add("jump.username", "required unless a vault credential supplies one")
			}
		}
	}

	if n.Transport == TransportSerial {
		if n.SerialPort == "" {
			add("serial_port", "required")
		}
		if n.Baud < 1 {
			add("baud", "must be positive")
		}
		if n.DataBits < 5 || n.DataBits > 8 {
			add("data_bits", "must be 5-8")
		}
		if !containsString(Parities, strings.ToLower(n.Parity)) {
			add("parity", "must be one of "+strings.Join(Parities, ", "))
		}
		if !containsString(StopBits, n.StopBits) {
			add("stop_bits", "must be one of "+strings.Join(StopBits, ", "))
		}
	}

	if n.FontSize != 0 && (n.FontSize < 6 || n.FontSize > 72) {
		add("font_size", "must be 6-72")
	}
	if n.ScrollbackLines < 0 {
		add("scrollback_lines", "must not be negative")
	}
	// PasteLineDelayMs is deliberately unchecked below zero: negative is how
	// a session says "no paste pacing here", the same convention the rest of
	// the per-session numbers use for an explicit off.
	if n.ConsoleBaud < 0 {
		add("console_baud", "must not be negative")
	}
	if enabled, set := n.AntiIdle.Enabled(); set && enabled && n.AntiIdle.IntervalSec != 0 && n.AntiIdle.IntervalSec < 10 {
		add("anti_idle.interval_sec", "must be at least 10 seconds")
	}

	return errs
}

// Err folds Validate's result into a single error, or nil when the node is
// usable. For callers that only want to know whether to proceed.
func (n Node) Err() error { return n.ErrFor(false) }

// ErrFor is Err with ValidateFor's credential-default question. See ValidateFor.
func (n Node) ErrFor(credentialDefault bool) error {
	errs := n.ValidateFor(credentialDefault)
	if len(errs) == 0 {
		return nil
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, e.Error())
	}
	return fmt.Errorf("invalid session: %s", strings.Join(parts, "; "))
}

func knownTransport(t Transport) bool {
	for _, k := range Transports {
		if k == t {
			return true
		}
	}
	return false
}

func containsString(list []string, s string) bool {
	for _, k := range list {
		if k == s {
			return true
		}
	}
	return false
}

// Marshal renders a node as YAML, exactly as it would be written to a session
// file. The secret fields are tagged out, so this is also what the connection
// manager shows when asked what saving would store — proving by inspection
// that a password typed into the dialog does not reach disk.
func Marshal(n Node) ([]byte, error) {
	return yaml.Marshal(n.Normalize())
}

// Unmarshal reads a node from YAML and normalizes it. Unknown keys are
// ignored, which is how an older file opens in a newer build.
func Unmarshal(data []byte) (Node, error) {
	// Zero value first, not Defaults(): a telnet node that omits its port
	// must get 23, and starting from an SSH-shaped default would hand it
	// 22 with nothing to say the file had asked for anything.
	var n Node
	if err := yaml.Unmarshal(data, &n); err != nil {
		return Node{}, err
	}
	return n.Normalize(), nil
}
