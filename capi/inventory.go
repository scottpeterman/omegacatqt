// capi/inventory.go
//go:build cgo

// The inventory: OmegaCat's own session file, filled from omegamaps' map.json.
//
// Synchronous local file reads and one atomic rewrite, safe from any thread.
// The contract is ../include/omegacat/inventory.h.
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"encoding/json"

	"github.com/scottpeterman/omegacatqt/internal/inventory"
)

// pathOrDefault is the inventory a call works on: the one named, or
// ~/.omegacat/inventory.yaml for NULL or "".
func pathOrDefault(p *C.char) string {
	if p == nil {
		return inventory.DefaultPath()
	}
	if s := C.GoString(p); s != "" {
		return s
	}
	return inventory.DefaultPath()
}

//export omegacat_inventory_default_path
func omegacat_inventory_default_path() *C.char {
	clearErr()
	return cstr(inventory.DefaultPath())
}

//export omegacat_inventory_load
func omegacat_inventory_load(path *C.char) *C.char {
	v, err := inventory.Load(pathOrDefault(path))
	if err != nil {
		setErr("inventory_load: %v", err)
		return nil
	}
	return jsonOut("inventory_load", v)
}

//export omegacat_inventory_import_map
func omegacat_inventory_import_map(path, mapPath, folder *C.char) *C.char {
	if mapPath == nil || C.GoString(mapPath) == "" {
		setErr("inventory_import_map: no map path")
		return nil
	}
	f := ""
	if folder != nil {
		f = C.GoString(folder)
	}
	res, err := inventory.ImportMap(pathOrDefault(path), C.GoString(mapPath), f)
	if err != nil {
		setErr("inventory_import_map: %v", err)
		return nil
	}
	return jsonOut("inventory_import_map", res)
}

//export omegacat_inventory_remove_folder
func omegacat_inventory_remove_folder(path, folder *C.char) C.int {
	if folder == nil {
		setErr("inventory_remove_folder: no folder")
		return -1
	}
	if err := inventory.RemoveFolder(pathOrDefault(path), C.GoString(folder)); err != nil {
		setErr("inventory_remove_folder: %v", err)
		return -1
	}
	clearErr()
	return 0
}

//export omegacat_inventory_apply
func omegacat_inventory_apply(path, opsJSON *C.char) *C.char {
	if opsJSON == nil {
		setErr("inventory_apply: no edits")
		return nil
	}
	var ops []inventory.Op
	if err := json.Unmarshal([]byte(C.GoString(opsJSON)), &ops); err != nil {
		setErr("inventory_apply: edits are not a JSON array of edits: %v", err)
		return nil
	}
	res, err := inventory.Apply(pathOrDefault(path), ops)
	if err != nil {
		setErr("inventory_apply: %v", err)
		return nil
	}
	return jsonOut("inventory_apply", res)
}

//export omegacat_inventory_platforms
func omegacat_inventory_platforms() *C.char {
	return jsonOut("inventory_platforms", inventory.Platforms())
}
