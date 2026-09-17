// capi/templates.go
//go:build cgo

// The Template Lab: test a template, sweep the database, and edit it.
//
// The contract is ../include/omegacat/templates.h.
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"encoding/json"
	"errors"

	"github.com/scottpeterman/omegacatqt/internal/capturedial"
	"github.com/scottpeterman/omegacatqt/internal/capturerun"
	"github.com/scottpeterman/omegacatqt/internal/tfsmlab"
)

// templatesPath is the database a call works on: the one named, or the one a
// capture with no templates_path uses (~/.omegacat/tfsm_templates.db, copied
// from the shipped database on first use). The same function a capture calls,
// so both name the same file -- and share one engine.
func templatesPath(p *C.char) (string, error) {
	path := ""
	if p != nil {
		path = C.GoString(p)
	}
	return capturedial.TemplatesPath(capturerun.Params{TemplatesPath: path})
}

//export omegacat_templates_default_path
func omegacat_templates_default_path() *C.char {
	path, err := templatesPath(nil)
	if err != nil {
		setErr("templates_default_path: %v", err)
		return nil
	}
	clearErr()
	return cstr(path)
}

//export omegacat_templates_platforms
func omegacat_templates_platforms(db *C.char) *C.char {
	path, err := templatesPath(db)
	if err != nil {
		setErr("templates_platforms: %v", err)
		return nil
	}
	ps, err := tfsmlab.Platforms(path)
	if err != nil {
		setErr("templates_platforms: %v", err)
		return nil
	}
	return jsonOut("templates_platforms", ps)
}

//export omegacat_templates_list
func omegacat_templates_list(db, platform, query *C.char) *C.char {
	path, err := templatesPath(db)
	if err != nil {
		setErr("templates_list: %v", err)
		return nil
	}
	items, err := tfsmlab.List(path, goStr(platform), goStr(query))
	if err != nil {
		setErr("templates_list: %v", err)
		return nil
	}
	return jsonOut("templates_list", items)
}

//export omegacat_templates_get
func omegacat_templates_get(db *C.char, rowid C.longlong) *C.char {
	path, err := templatesPath(db)
	if err != nil {
		setErr("templates_get: %v", err)
		return nil
	}
	row, err := tfsmlab.Get(path, int64(rowid))
	if err != nil {
		setErr("templates_get: %v", err)
		return nil
	}
	return jsonOut("templates_get", row)
}

type labTestRequest struct {
	Raw     string `json:"raw_output"`
	Content string `json:"textfsm_content"`
	Command string `json:"command"`
	Clean   *bool  `json:"clean"`
}

//export omegacat_templates_test
func omegacat_templates_test(reqJSON *C.char) *C.char {
	var req labTestRequest
	if err := decode(reqJSON, &req); err != nil {
		setErr("templates_test: %v", err)
		return nil
	}
	clean := req.Clean == nil || *req.Clean // admin.py: clean defaults to true
	return jsonOut("templates_test", tfsmlab.Test(req.Content, req.Raw, req.Command, clean))
}

type labSweepRequest struct {
	TemplatesPath string `json:"templates_path"`
	Raw           string `json:"raw_output"`
	Platform      string `json:"platform"`
	Command       string `json:"command"`
}

//export omegacat_templates_sweep
func omegacat_templates_sweep(reqJSON *C.char) *C.char {
	var req labSweepRequest
	if err := decode(reqJSON, &req); err != nil {
		setErr("templates_sweep: %v", err)
		return nil
	}
	path, err := capturedial.TemplatesPath(capturerun.Params{TemplatesPath: req.TemplatesPath})
	if err != nil {
		setErr("templates_sweep: %v", err)
		return nil
	}
	eng, err := engineFor(path)
	if err != nil {
		setErr("templates_sweep: %v", err)
		return nil
	}
	return jsonOut("templates_sweep", tfsmlab.RunSweep(eng, req.Raw, req.Platform, req.Command))
}

type labSaveRequest struct {
	TemplatesPath string `json:"templates_path"`
	RowID         int64  `json:"rowid"`
	Name          string `json:"cli_command"`
	Content       string `json:"textfsm_content"`
}

type labSaved struct {
	RowID  int64  `json:"rowid"`
	ID     int64  `json:"id"`
	Name   string `json:"cli_command"`
	Reload string `json:"reload_error,omitempty"`
}

//export omegacat_templates_save
func omegacat_templates_save(reqJSON *C.char) *C.char {
	var req labSaveRequest
	if err := decode(reqJSON, &req); err != nil {
		setErr("templates_save: %v", err)
		return nil
	}
	path, err := capturedial.TemplatesPath(capturerun.Params{TemplatesPath: req.TemplatesPath})
	if err != nil {
		setErr("templates_save: %v", err)
		return nil
	}
	out := labSaved{RowID: req.RowID, Name: req.Name}
	if req.RowID > 0 {
		out.ID, err = tfsmlab.Update(path, req.RowID, req.Name, req.Content)
	} else {
		out.RowID, out.ID, err = tfsmlab.Create(path, req.Name, req.Content)
	}
	if err != nil {
		setErr("%v", err)
		return nil
	}
	// Saved is saved. A reload that fails leaves captures on the previous
	// templates until one succeeds, which the Lab reports rather than hides.
	if rerr := reloadShared(path); rerr != nil {
		out.Reload = rerr.Error()
	}
	return jsonOut("templates_save", out)
}

//export omegacat_templates_delete
func omegacat_templates_delete(db *C.char, rowid C.longlong) C.int {
	path, err := templatesPath(db)
	if err != nil {
		setErr("templates_delete: %v", err)
		return -1
	}
	if err := tfsmlab.Delete(path, int64(rowid)); err != nil {
		if errors.Is(err, tfsmlab.ErrNotFound) {
			setErr("template %d not found", int64(rowid))
		} else {
			setErr("templates_delete: %v", err)
		}
		return -1
	}
	if err := reloadShared(path); err != nil {
		setErr("deleted, but the template engine did not reload: %v", err)
		return 1
	}
	clearErr()
	return 0
}

func goStr(p *C.char) string {
	if p == nil {
		return ""
	}
	return C.GoString(p)
}

func decode(p *C.char, v any) error {
	if p == nil {
		return errors.New("no request")
	}
	return json.Unmarshal([]byte(C.GoString(p)), v)
}
