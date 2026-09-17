// capi/vault.go
//go:build cgo

// C surface for the credential vault, ported from omegassh's capi/vault.go:
// same calls, same codes, same rule -- metadata comes out, secrets only go in.
// The vault file format is shared, so a vault written by ocvault, Omega or
// Pathfinder opens here.
//
// Shared with capi.go on purpose: setErr and omegacat_last_error carry detail
// text for both halves, and omegacat_free is the single deallocator. The
// handle registry is NOT shared: runs and vaults are different things, and a
// vault handle passed to omegacat_progress is a bug that gets caught.
//
// A capture resolves its credentials inside Go from the vault handle it is
// given (omegacat_capture_open), which is what keeps every password and key
// passphrase off the C++ heap.
//
// The file format is omegamaps', SNMP credentials included, and a vault that
// holds them opens, lists and edits here. Store refuses to ADD or REPLACE one:
// OmegaCat captures over SSH and never offers an SNMP credential, so creating
// one here would be a record nothing in this application uses. There is no getter for secret
// material here, and there must never be one.
//
// The contract is ../include/omegacat/vault.h.
package main

/*
#include <stdlib.h>

typedef enum {
    OMEGACAT_VAULT_OK                      = 0,
    OMEGACAT_VAULT_ERR_BAD_HANDLE          = 1,
    OMEGACAT_VAULT_ERR_BAD_ARGUMENT        = 2,
    OMEGACAT_VAULT_ERR_LOCKED              = 10,
    OMEGACAT_VAULT_ERR_NOT_FOUND           = 11,
    OMEGACAT_VAULT_ERR_EXISTS              = 12,
    OMEGACAT_VAULT_ERR_WRONG_PASSWORD      = 13,
    OMEGACAT_VAULT_ERR_CORRUPT             = 14,
    OMEGACAT_VAULT_ERR_IO                  = 15,
    OMEGACAT_VAULT_ERR_WEAK_PASSWORD       = 16,
    OMEGACAT_VAULT_ERR_CRED_NOT_FOUND      = 20,
    OMEGACAT_VAULT_ERR_DUPLICATE_NAME      = 21,
    OMEGACAT_VAULT_ERR_EMPTY_NAME          = 22,
    OMEGACAT_VAULT_ERR_SNMP_DEFAULT        = 23,
    OMEGACAT_VAULT_ERR_KEYRING_NO_ENTRY    = 30,
    OMEGACAT_VAULT_ERR_KEYRING_UNAVAILABLE = 31,
    OMEGACAT_VAULT_ERR_KEYRING_STALE       = 32,
    OMEGACAT_VAULT_ERR_NEEDS_PASSWORD      = 33,
    OMEGACAT_VAULT_ERR_INTERNAL            = 99
} omegacat_vault_err;
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/scottpeterman/omegacatqt/internal/vault"
	"github.com/scottpeterman/omegacatqt/internal/vaultcli"
)

// masterEnvVar is the last resort for a run with no keyring and no human,
// shared with the command-line tools so one variable serves both.
const masterEnvVar = vaultcli.MasterEnvVar

// ---------------------------------------------------------------------------
// Handle registry
//
// Separate from capi.go's reg for the reason in the file comment. Same shape,
// so the two read alike.

var (
	vreg    = map[int64]*vault.Vault{}
	vregMu  sync.Mutex
	vNextID int64 = 1
)

func vlookup(h C.longlong) *vault.Vault {
	vregMu.Lock()
	defer vregMu.Unlock()
	return vreg[int64(h)]
}

// ---------------------------------------------------------------------------
// Error mapping

var (
	errKeyringUnavailable = errors.New("os keyring could not be reached")
	errKeyringStale       = errors.New("the stored keyring entry no longer unlocks this vault")
	errNeedsPassword      = errors.New("vault master password required")
)

// vaultErr maps a Go error to the C enum and records its text via the shared
// setErr, so omegacat_last_error carries the detail.
//
// The keyring range is checked first and separately. "Could not reach the
// keyring" and "the vault refused the password" are the two conditions this
// function exists to keep apart: a UI told WRONG_PASSWORD sends the user to
// retype something they never typed.
func vaultErr(err error) C.omegacat_vault_err {
	if err == nil {
		// Clear rather than leave the previous message readable. A caller that
		// checks omegacat_last_error after an OK return should get nothing,
		// not the failure before it.
		clearErr()
		return C.OMEGACAT_VAULT_OK
	}
	setErr("%v", err)

	switch {
	case errors.Is(err, vaultcli.ErrNoKeyringEntry):
		// Not reachable through the exported surface today: quietMaster treats
		// "nothing filed" as a fall-through rather than a failure, which is
		// the behaviour we want. Kept because the number is part of a stable
		// enum, and mapped rather than left to land on INTERNAL if some future
		// call does surface it.
		return C.OMEGACAT_VAULT_ERR_KEYRING_NO_ENTRY
	case errors.Is(err, errKeyringUnavailable):
		return C.OMEGACAT_VAULT_ERR_KEYRING_UNAVAILABLE
	case errors.Is(err, errKeyringStale):
		return C.OMEGACAT_VAULT_ERR_KEYRING_STALE
	case errors.Is(err, errNeedsPassword):
		return C.OMEGACAT_VAULT_ERR_NEEDS_PASSWORD

	case errors.Is(err, vault.ErrVaultLocked):
		return C.OMEGACAT_VAULT_ERR_LOCKED
	case errors.Is(err, vault.ErrVaultNotFound):
		return C.OMEGACAT_VAULT_ERR_NOT_FOUND
	case errors.Is(err, vault.ErrVaultExists):
		return C.OMEGACAT_VAULT_ERR_EXISTS
	case errors.Is(err, vault.ErrWrongPassword):
		return C.OMEGACAT_VAULT_ERR_WRONG_PASSWORD
	case errors.Is(err, vault.ErrCredNotFound):
		return C.OMEGACAT_VAULT_ERR_CRED_NOT_FOUND
	case errors.Is(err, vault.ErrDuplicateName):
		return C.OMEGACAT_VAULT_ERR_DUPLICATE_NAME
	case errors.Is(err, vault.ErrEmptyName):
		return C.OMEGACAT_VAULT_ERR_EMPTY_NAME
	case errors.Is(err, vault.ErrSNMPDefault):
		return C.OMEGACAT_VAULT_ERR_SNMP_DEFAULT
	case errors.Is(err, os.ErrPermission), errors.Is(err, os.ErrNotExist):
		return C.OMEGACAT_VAULT_ERR_IO
	}

	// The vault reports the rest as formatted strings rather than sentinels.
	// Matching on text is unpleasant, and the right fix is sentinels in
	// vault/ -- until then, being explicit here beats returning INTERNAL for
	// an ordinary corrupt file.
	msg := err.Error()
	switch {
	case strings.Contains(msg, "must be at least"):
		return C.OMEGACAT_VAULT_ERR_WEAK_PASSWORD
	case strings.Contains(msg, "corrupt"), strings.Contains(msg, "unsupported vault version"):
		return C.OMEGACAT_VAULT_ERR_CORRUPT
	case strings.Contains(msg, "failed to write"), strings.Contains(msg, "failed to read"),
		strings.Contains(msg, "failed to commit"):
		return C.OMEGACAT_VAULT_ERR_IO
	}
	return C.OMEGACAT_VAULT_ERR_INTERNAL
}

func vaultBadHandle() C.omegacat_vault_err {
	setErr("no such vault handle")
	return C.OMEGACAT_VAULT_ERR_BAD_HANDLE
}

// ---------------------------------------------------------------------------
// Marshalling

// outString writes a Go string to a caller-owned C buffer, freed with
// omegacat_free. Writing nothing when out is nil lets a caller that only wants
// the status code pass one.
func outString(out **C.char, s string) {
	if out == nil {
		return
	}
	*out = C.CString(s)
}

// metaJSON is the outbound record. Separate from vault.Meta on purpose:
// vault.Meta has no JSON tags and is free to change shape, while this is a
// wire format that C++ parses.
type metaJSON struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Username    string     `json:"username"`
	AuthLabel   string     `json:"auth_label"`
	Description string     `json:"description"`
	Priority    int        `json:"priority"`
	Tags        []string   `json:"tags"`
	Scope       scopeJSON  `json:"scope"`
	IsDefault   bool       `json:"is_default"`
	Disabled    bool       `json:"disabled"`
	LastUsed    *time.Time `json:"last_used,omitempty"`
	HasSecret   bool       `json:"has_secret"`
	AuthType    string     `json:"auth_type"`
	IsSNMP      bool       `json:"is_snmp"`

	SNMPAuthProtocol string `json:"snmp_auth_protocol,omitempty"`
	SNMPPrivProtocol string `json:"snmp_priv_protocol,omitempty"`
}

type scopeJSON struct {
	DomainSuffix string   `json:"domain_suffix"`
	CIDRs        []string `json:"cidrs"`
	Platforms    []string `json:"platforms"`
}

// Empty slices rather than null: a C++ parser handling both is two code paths
// where one will do.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func toMetaJSON(m vault.Meta) metaJSON {
	out := metaJSON{
		ID:          m.ID,
		Name:        m.Name,
		Username:    m.Username,
		AuthLabel:   m.AuthLabel,
		Description: m.Description,
		Priority:    m.Priority,
		Tags:        nonNil(m.Tags),
		Scope: scopeJSON{
			DomainSuffix: m.Scope.DomainSuffix,
			CIDRs:        nonNil(m.Scope.CIDRs),
			Platforms:    nonNil(m.Scope.Platforms),
		},
		IsDefault: m.IsDefault,
		Disabled:  m.Disabled,
		HasSecret: m.HasSecret,
	}
	if !m.LastUsed.IsZero() {
		t := m.LastUsed.UTC()
		out.LastUsed = &t
	}
	return out
}

// redact builds the outbound view from a full credential. It is the only thing
// standing between vault.Get, which returns secrets, and the boundary -- so it
// stays one function that every read path goes through.
func redact(c vault.Credential) metaJSON {
	m := toMetaJSON(vault.Meta{
		ID:          c.ID,
		Name:        c.Name,
		Username:    c.Username,
		AuthLabel:   c.Method().String(),
		Description: c.Description,
		Priority:    c.Priority,
		Tags:        c.Tags,
		Scope:       c.Scope,
		IsDefault:   c.IsDefault,
		Disabled:    c.Disabled,
		LastUsed:    c.LastUsed,
		HasSecret:   c.Password != "" || c.KeyPassphrase != "" || c.KeyPath != "",
	})
	// What the credentials table needs and vault.Meta does not carry: the
	// stored auth type, whether it is SNMP, and a v3 credential's algorithm
	// names. None of it is secret.
	m.AuthType = c.AuthType
	m.IsSNMP = c.IsSNMP()
	m.SNMPAuthProtocol = c.SNMPAuthProtocol
	m.SNMPPrivProtocol = c.SNMPPrivProtocol
	return m
}

// credJSON is the inbound record. It carries secret fields; nothing ever
// serializes one back out.
type credJSON struct {
	ID            string    `json:"id,omitempty"`
	Name          string    `json:"name"`
	Username      string    `json:"username"`
	AuthType      string    `json:"auth_type"`
	Password      string    `json:"password,omitempty"`
	KeyPath       string    `json:"key_path,omitempty"`
	KeyPassphrase string    `json:"key_passphrase,omitempty"`
	Description   string    `json:"description,omitempty"`
	Priority      int       `json:"priority,omitempty"`
	Tags          []string  `json:"tags,omitempty"`
	Scope         scopeJSON `json:"scope,omitempty"`
	IsDefault     bool      `json:"is_default,omitempty"`
	Disabled      bool      `json:"disabled,omitempty"`

	SNMPAuthProtocol string `json:"snmp_auth_protocol,omitempty"`
	SNMPPrivProtocol string `json:"snmp_priv_protocol,omitempty"`
}

func (c credJSON) toCredential() vault.Credential {
	return vault.Credential{
		ID:            c.ID,
		Name:          c.Name,
		Username:      c.Username,
		AuthType:      c.AuthType,
		Password:      c.Password,
		KeyPath:       c.KeyPath,
		KeyPassphrase: c.KeyPassphrase,
		Description:   c.Description,
		Priority:      c.Priority,
		Tags:          c.Tags,
		Scope: vault.Scope{
			DomainSuffix: c.Scope.DomainSuffix,
			CIDRs:        c.Scope.CIDRs,
			Platforms:    c.Scope.Platforms,
		},
		IsDefault:        c.IsDefault,
		Disabled:         c.Disabled,
		SNMPAuthProtocol: c.SNMPAuthProtocol,
		SNMPPrivProtocol: c.SNMPPrivProtocol,
	}
}

// ---------------------------------------------------------------------------
// Error names

var vaultErrorNames = map[C.omegacat_vault_err]*C.char{}

func init() {
	for code, name := range map[C.omegacat_vault_err]string{
		C.OMEGACAT_VAULT_OK:                      "OMEGACAT_VAULT_OK",
		C.OMEGACAT_VAULT_ERR_BAD_HANDLE:          "OMEGACAT_VAULT_ERR_BAD_HANDLE",
		C.OMEGACAT_VAULT_ERR_BAD_ARGUMENT:        "OMEGACAT_VAULT_ERR_BAD_ARGUMENT",
		C.OMEGACAT_VAULT_ERR_LOCKED:              "OMEGACAT_VAULT_ERR_LOCKED",
		C.OMEGACAT_VAULT_ERR_NOT_FOUND:           "OMEGACAT_VAULT_ERR_NOT_FOUND",
		C.OMEGACAT_VAULT_ERR_EXISTS:              "OMEGACAT_VAULT_ERR_EXISTS",
		C.OMEGACAT_VAULT_ERR_WRONG_PASSWORD:      "OMEGACAT_VAULT_ERR_WRONG_PASSWORD",
		C.OMEGACAT_VAULT_ERR_CORRUPT:             "OMEGACAT_VAULT_ERR_CORRUPT",
		C.OMEGACAT_VAULT_ERR_IO:                  "OMEGACAT_VAULT_ERR_IO",
		C.OMEGACAT_VAULT_ERR_WEAK_PASSWORD:       "OMEGACAT_VAULT_ERR_WEAK_PASSWORD",
		C.OMEGACAT_VAULT_ERR_CRED_NOT_FOUND:      "OMEGACAT_VAULT_ERR_CRED_NOT_FOUND",
		C.OMEGACAT_VAULT_ERR_DUPLICATE_NAME:      "OMEGACAT_VAULT_ERR_DUPLICATE_NAME",
		C.OMEGACAT_VAULT_ERR_EMPTY_NAME:          "OMEGACAT_VAULT_ERR_EMPTY_NAME",
		C.OMEGACAT_VAULT_ERR_SNMP_DEFAULT:        "OMEGACAT_VAULT_ERR_SNMP_DEFAULT",
		C.OMEGACAT_VAULT_ERR_KEYRING_NO_ENTRY:    "OMEGACAT_VAULT_ERR_KEYRING_NO_ENTRY",
		C.OMEGACAT_VAULT_ERR_KEYRING_UNAVAILABLE: "OMEGACAT_VAULT_ERR_KEYRING_UNAVAILABLE",
		C.OMEGACAT_VAULT_ERR_KEYRING_STALE:       "OMEGACAT_VAULT_ERR_KEYRING_STALE",
		C.OMEGACAT_VAULT_ERR_NEEDS_PASSWORD:      "OMEGACAT_VAULT_ERR_NEEDS_PASSWORD",
		C.OMEGACAT_VAULT_ERR_INTERNAL:            "OMEGACAT_VAULT_ERR_INTERNAL",
	} {
		// Allocated once and never freed: the header promises the caller does
		// not free these, so they must outlive every call.
		vaultErrorNames[code] = C.CString(name)
	}
}

//export omegacat_vault_error_name
func omegacat_vault_error_name(code C.omegacat_vault_err) *C.char {
	if s, ok := vaultErrorNames[code]; ok {
		return s
	}
	return vaultErrorNames[C.OMEGACAT_VAULT_ERR_INTERNAL]
}

// ---------------------------------------------------------------------------
// Handles

//export omegacat_vault_open
func omegacat_vault_open(path *C.char) C.longlong {
	p := C.GoString(path)
	if p == "" {
		setErr("vault path is required")
		return -1
	}
	vregMu.Lock()
	defer vregMu.Unlock()
	h := vNextID
	vNextID++
	vreg[h] = vault.New(p)
	clearErr()
	return C.longlong(h)
}

//export omegacat_vault_close
func omegacat_vault_close(h C.longlong) {
	vregMu.Lock()
	v := vreg[int64(h)]
	delete(vreg, int64(h))
	vregMu.Unlock()
	if v != nil {
		v.Lock()
	}
}

//export omegacat_vault_exists
func omegacat_vault_exists(h C.longlong) C.int {
	v := vlookup(h)
	if v == nil || !v.Exists() {
		return 0
	}
	return 1
}

//export omegacat_vault_is_locked
func omegacat_vault_is_locked(h C.longlong) C.int {
	v := vlookup(h)
	if v == nil || v.IsLocked() {
		// A handle we do not have is not one you can read through.
		return 1
	}
	return 0
}

//export omegacat_vault_path
func omegacat_vault_path(h C.longlong, out **C.char) C.omegacat_vault_err {
	v := vlookup(h)
	if v == nil {
		return vaultBadHandle()
	}
	outString(out, v.Path())
	clearErr()
	return C.OMEGACAT_VAULT_OK
}

// ---------------------------------------------------------------------------
// Unlocking

//export omegacat_vault_create
func omegacat_vault_create(h C.longlong, master *C.char) C.omegacat_vault_err {
	v := vlookup(h)
	if v == nil {
		return vaultBadHandle()
	}
	return vaultErr(v.Create(C.GoString(master)))
}

//export omegacat_vault_unlock
func omegacat_vault_unlock(h C.longlong, master *C.char) C.omegacat_vault_err {
	v := vlookup(h)
	if v == nil {
		return vaultBadHandle()
	}
	return vaultErr(v.Unlock(C.GoString(master)))
}

//export omegacat_vault_unlock_quiet
func omegacat_vault_unlock_quiet(h C.longlong) C.omegacat_vault_err {
	v := vlookup(h)
	if v == nil {
		return vaultBadHandle()
	}
	master, fromKeyring, err := quietMaster(v.Path())
	if err != nil {
		return vaultErr(err)
	}
	err = v.Unlock(master)
	if err != nil && fromKeyring && errors.Is(err, vault.ErrWrongPassword) {
		// The distinction this whole surface exists for. Nobody typed
		// anything, so "wrong password" is a lie that sends the user to the
		// wrong fix.
		return vaultErr(fmt.Errorf("%w: %s", errKeyringStale, v.Path()))
	}
	return vaultErr(err)
}

// quietMaster is the source chain: OS keyring, then the environment. It never
// prompts -- a GUI has no terminal to prompt on, and blocking on one nobody is
// watching is worse than failing.
//
// The keyring wins over the environment deliberately: an entry an operator
// filed on purpose should not be shadowed by a variable inherited from a
// parent process.
//
// An unreachable keyring is not fatal on its own. The common cause is a
// headless session with no D-Bus, which is precisely when the environment
// variable earns its keep -- so the reason is carried forward and reported
// only if nothing else answers.
func quietMaster(path string) (master string, fromKeyring bool, err error) {
	var unreachable error
	if path != "" {
		switch m, kerr := vaultcli.KeyringGet(path); {
		case kerr == nil:
			return m, true, nil
		case errors.Is(kerr, vaultcli.ErrNoKeyringEntry):
			// Nothing filed, or deliberately disabled. Fall through.
		default:
			unreachable = fmt.Errorf("%w: %v", errKeyringUnavailable, kerr)
		}
	}
	if m, ok := os.LookupEnv(masterEnvVar); ok && m != "" {
		return m, false, nil
	}
	if unreachable != nil {
		return "", false, unreachable
	}
	return "", false, errNeedsPassword
}

//export omegacat_vault_lock
func omegacat_vault_lock(h C.longlong) {
	if v := vlookup(h); v != nil {
		v.Lock()
	}
}

//export omegacat_vault_change_master
func omegacat_vault_change_master(h C.longlong, oldMaster, newMaster *C.char) C.omegacat_vault_err {
	v := vlookup(h)
	if v == nil {
		return vaultBadHandle()
	}
	return vaultErr(v.ChangeMasterPassword(C.GoString(oldMaster), C.GoString(newMaster)))
}

// ---------------------------------------------------------------------------
// Credentials

//export omegacat_vault_list
func omegacat_vault_list(h C.longlong, out **C.char) C.omegacat_vault_err {
	v := vlookup(h)
	if v == nil {
		return vaultBadHandle()
	}
	metas, err := v.List()
	if err != nil {
		return vaultErr(err)
	}
	// List gives the order; each record then goes out through redact, the one
	// path every read takes, so the list and meta calls cannot disagree.
	records := make([]metaJSON, 0, len(metas))
	for _, m := range metas {
		c, err := v.Get(m.ID)
		if err != nil {
			return vaultErr(err)
		}
		records = append(records, redact(c))
	}
	blob, err := json.Marshal(records)
	if err != nil {
		return vaultErr(err)
	}
	outString(out, string(blob))
	clearErr()
	return C.OMEGACAT_VAULT_OK
}

//export omegacat_vault_meta
func omegacat_vault_meta(h C.longlong, idOrName *C.char, out **C.char) C.omegacat_vault_err {
	v := vlookup(h)
	if v == nil {
		return vaultBadHandle()
	}
	cred, err := v.Get(C.GoString(idOrName))
	if err != nil {
		return vaultErr(err)
	}
	blob, err := json.Marshal(redact(cred))
	if err != nil {
		return vaultErr(err)
	}
	outString(out, string(blob))
	clearErr()
	return C.OMEGACAT_VAULT_OK
}

//export omegacat_vault_store
func omegacat_vault_store(h C.longlong, jsonIn *C.char, idOut **C.char) C.omegacat_vault_err {
	v := vlookup(h)
	if v == nil {
		return vaultBadHandle()
	}
	var rec credJSON
	if err := json.Unmarshal([]byte(C.GoString(jsonIn)), &rec); err != nil {
		setErr("credential json: %v", err)
		return C.OMEGACAT_VAULT_ERR_BAD_ARGUMENT
	}
	cred := rec.toCredential()
	if cred.IsSNMP() {
		setErr("%s: omegacat captures over SSH and never offers an SNMP credential; add it with omegamaps", cred.AuthType)
		return C.OMEGACAT_VAULT_ERR_BAD_ARGUMENT
	}

	if cred.ID == "" {
		added, err := v.Add(cred)
		if err != nil {
			return vaultErr(err)
		}
		outString(idOut, added.ID)
		clearErr()
		return C.OMEGACAT_VAULT_OK
	}
	// The wire format has no last_used field -- it is outbound-only, since a
	// caller has no business setting it -- so a replace would reset it to
	// zero and "when did this credential last work" would restart every time
	// somebody edited a password. Carry it across here rather than teaching
	// credJSON a field the UI would then have to round-trip correctly.
	if existing, err := v.Get(cred.ID); err == nil {
		cred.LastUsed = existing.LastUsed
	}

	if err := v.Update(cred); err != nil {
		return vaultErr(err)
	}
	outString(idOut, cred.ID)
	clearErr()
	return C.OMEGACAT_VAULT_OK
}

//export omegacat_vault_update_meta
func omegacat_vault_update_meta(h C.longlong, jsonIn *C.char) C.omegacat_vault_err {
	v := vlookup(h)
	if v == nil {
		return vaultBadHandle()
	}
	var rec credJSON
	if err := json.Unmarshal([]byte(C.GoString(jsonIn)), &rec); err != nil {
		setErr("credential json: %v", err)
		return C.OMEGACAT_VAULT_ERR_BAD_ARGUMENT
	}
	if rec.ID == "" {
		setErr("update_meta needs an id; use omegacat_vault_store to add")
		return C.OMEGACAT_VAULT_ERR_BAD_ARGUMENT
	}
	// Secret fields in the payload are not merely ignored, they are refused.
	// A caller sending material here believes it is being stored, and the one
	// thing this call guarantees is that material does not change -- so the
	// disagreement gets reported rather than absorbed.
	if rec.Password != "" || rec.KeyPath != "" || rec.KeyPassphrase != "" ||
		rec.AuthType != "" {
		setErr("update_meta does not carry credential material or auth type; use omegacat_vault_store")
		return C.OMEGACAT_VAULT_ERR_BAD_ARGUMENT
	}
	return vaultErr(v.UpdateMetadata(rec.toCredential()))
}

//export omegacat_vault_delete
func omegacat_vault_delete(h C.longlong, id *C.char) C.omegacat_vault_err {
	v := vlookup(h)
	if v == nil {
		return vaultBadHandle()
	}
	return vaultErr(v.Delete(C.GoString(id)))
}

//export omegacat_vault_rename
func omegacat_vault_rename(h C.longlong, id, newName *C.char) C.omegacat_vault_err {
	v := vlookup(h)
	if v == nil {
		return vaultBadHandle()
	}
	// Read-modify-write rather than a rename in vault: Get returns the whole
	// record, secrets included, so Update puts them back unchanged. That is
	// only safe because both ends of it are on this side of the boundary --
	// which is exactly why this call exists instead of leaving a C++ caller to
	// do it with store, where the secret it never received would be lost.
	cred, err := v.Get(C.GoString(id))
	if err != nil {
		return vaultErr(err)
	}
	cred.Name = C.GoString(newName)
	return vaultErr(v.Update(cred))
}

//export omegacat_vault_set_default
func omegacat_vault_set_default(h C.longlong, id *C.char) C.omegacat_vault_err {
	v := vlookup(h)
	if v == nil {
		return vaultBadHandle()
	}
	return vaultErr(v.SetDefault(C.GoString(id)))
}

//export omegacat_vault_clear_default
func omegacat_vault_clear_default(h C.longlong) C.omegacat_vault_err {
	v := vlookup(h)
	if v == nil {
		return vaultBadHandle()
	}
	return vaultErr(v.ClearDefault())
}

//export omegacat_vault_default_name
func omegacat_vault_default_name(h C.longlong, out **C.char) C.omegacat_vault_err {
	v := vlookup(h)
	if v == nil {
		return vaultBadHandle()
	}
	if v.IsLocked() {
		return vaultErr(vault.ErrVaultLocked)
	}
	outString(out, v.DefaultName())
	clearErr()
	return C.OMEGACAT_VAULT_OK
}

//export omegacat_vault_set_disabled
func omegacat_vault_set_disabled(h C.longlong, id *C.char, disabled C.int) C.omegacat_vault_err {
	v := vlookup(h)
	if v == nil {
		return vaultBadHandle()
	}
	return vaultErr(v.SetDisabled(C.GoString(id), disabled != 0))
}

// ---------------------------------------------------------------------------
// Keyring

type keyringStatusJSON struct {
	Disabled  bool   `json:"disabled"`
	Available bool   `json:"available"`
	HasEntry  bool   `json:"has_entry"`
	Account   string `json:"account"`
	Error     string `json:"error"`
}

//export omegacat_vault_keyring_status
func omegacat_vault_keyring_status(h C.longlong, out **C.char) C.omegacat_vault_err {
	v := vlookup(h)
	if v == nil {
		return vaultBadHandle()
	}
	st := vaultcli.Keyring(v.Path())
	rec := keyringStatusJSON{
		Disabled:  st.Disabled,
		Available: st.Available,
		HasEntry:  st.HasEntry,
		Account:   st.Account,
	}
	if st.Err != nil {
		rec.Error = st.Err.Error()
	}
	blob, err := json.Marshal(rec)
	if err != nil {
		return vaultErr(err)
	}
	outString(out, string(blob))
	// An unreachable keyring is the status being reported, not a failure to
	// report it, so this returns OK with the reason in the payload.
	clearErr()
	return C.OMEGACAT_VAULT_OK
}

//export omegacat_vault_keyring_set
func omegacat_vault_keyring_set(h C.longlong, master *C.char) C.omegacat_vault_err {
	v := vlookup(h)
	if v == nil {
		return vaultBadHandle()
	}
	if err := vaultcli.KeyringSet(v.Path(), C.GoString(master)); err != nil {
		return vaultErr(fmt.Errorf("%w: %v", errKeyringUnavailable, err))
	}
	clearErr()
	return C.OMEGACAT_VAULT_OK
}

//export omegacat_vault_keyring_clear
func omegacat_vault_keyring_clear(h C.longlong) C.omegacat_vault_err {
	v := vlookup(h)
	if v == nil {
		return vaultBadHandle()
	}
	if err := vaultcli.KeyringClear(v.Path()); err != nil {
		return vaultErr(fmt.Errorf("%w: %v", errKeyringUnavailable, err))
	}
	clearErr()
	return C.OMEGACAT_VAULT_OK
}
