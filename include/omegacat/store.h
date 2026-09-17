/* include/omegacat/store.h
 *
 * The capture store, browsable with no run. A store outlives every run that
 * wrote to it, and opening one to read last night's config with nothing
 * capturing is an ordinary session -- so a store is its own handle.
 *
 *     long long s = omegacat_store_open("/Users/lab/captures");
 *     char *devs  = omegacat_store_devices(s);
 *     char *types = omegacat_store_types(s, "lab-r1");
 *     long long n;
 *     char *cfg   = omegacat_store_read(s, "lab-r1", "running-config", file, &n);
 *     ... omegacat_free each ...
 *     omegacat_store_close(s);
 *
 * On disk:
 *
 *     <root>/devices/<slug>/device.json
 *     <root>/devices/<slug>/<type>/<timestamp>.txt
 *     <root>/devices/<slug>/<type>/<timestamp>.parsed.json   ARP and MAC tables
 *     <root>/devices/<slug>/<type>/history.jsonl
 *
 * Devices are named by their canonical name everywhere on this surface, never
 * by slug or path. A capture that came back unchanged writes no file but does
 * append a history line, so "attempts" moves on every run and "stored" moves
 * only when the device's content changed. A device whose last attempt is a
 * month old is not being captured, whatever its file count says.
 *
 * THREADING. Synchronous local file reads, safe from any thread. The lists are
 * small. omegacat_store_read returns a whole capture (up to 16 MiB) and is the
 * one worth calling off the GUI thread for a large config.
 *
 * Several handles on one root are fine, and a store may be browsed while a
 * capture writes to it: files are written atomically and history is appended,
 * so a read sees the old state or the new one.
 *
 * ERRORS. -1 or NULL, with the reason in omegacat_last_error().
 */

#ifndef OMEGACAT_STORE_H
#define OMEGACAT_STORE_H

#include <omegacat/omegacat.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Opens a store, creating <root>/devices if it does not exist. Returns a
 * handle > 0, or -1. */
long long omegacat_store_open(const char *root);

/* 0, or -1 on a bad handle. */
int omegacat_store_close(long long store);

/* The root the handle was opened on. Free it. */
char *omegacat_store_root(long long store);

/* {"devices": [{"canonical","aliases":[...],"platform","first_seen_ms",
 *               "last_seen_ms"}],
 *  "unreadable": ["<directory>", ...]}
 * sorted by canonical name. unreadable names device directories whose
 * device.json is missing or damaged; they are reported beside the devices that
 * did read rather than failing the whole list. */
char *omegacat_store_devices(long long store);

/* The capture types held for one device, newest activity first:
 *   [{"type","attempts","stored","last_ms","bytes","sha256","file"}]
 * bytes, sha256 and file describe the newest stored file; file is what
 * omegacat_store_read takes. */
char *omegacat_store_types(long long store, const char *canonical);

/* Every recorded attempt for a device and type, oldest first:
 *   [{"at_ms","command","sha256","bytes","file","unchanged"}]
 * An unchanged attempt names the existing file that matched. */
char *omegacat_store_history(long long store, const char *canonical, const char *type);

/* One stored file. Returns its bytes followed by a NUL, and writes the length
 * WITHOUT the NUL to *length (which may be NULL). The length is the answer:
 * the NUL is there so text can be used as a C string, not a promise the
 * content has none. Free it. */
char *omegacat_store_read(long long store, const char *canonical, const char *type,
                          const char *file, long long *length);

/* The structured parse of one stored file, or the JSON literal null when there
 * is none (a type that is not parsed, or a capture from before parsing).
 *
 *   {"status": "parsed" | "no-match", "template", "score", "hint", "tried",
 *    "header": [...], "records": [{VALUE: "..." | [...]}],
 *    "parsed_at_ms", "raw_file", "raw_sha256"}
 *
 * header is the template's Value names in order -- the columns. A value is a
 * string, or an array of strings for a List value. A "no-match" carries the
 * closest template and its score and no records.
 *
 * raw_sha256 is the digest of the file the parse was made from. Compare it with
 * the file's sha256 from omegacat_store_types or _history before showing it:
 * a mismatch is a stale parse and should not be presented as this file's.
 *
 * A parse is rewritten on every capture of its type, unchanged ones included,
 * so an edited template applies to the next run without re-collecting. */
char *omegacat_store_parsed(long long store, const char *canonical, const char *type,
                            const char *file);

/* A line diff of two stored versions of a static capture type, older first
 * (file names as omegacat_store_history gives them). context is the number of
 * unchanged lines around each change; negative means 3.
 *
 *   {"from","to","identical","added","removed",
 *    "hunks":[{"a_start","a_len","b_start","b_len",
 *              "lines":[{"op":"="|"-"|"+","a","b","text"}]}]}
 *
 * a and b are 1-based line numbers in the older and newer file, absent where
 * the line is not in that side.
 *
 * Changed lines matching the device platform's ignore rules
 * (omegacat_diff_ignore_path) carry "ignored": true, are left out of added and
 * removed, are counted in "ignored", and never open a hunk. "rules_path" is
 * the file used; "rules_warning", when present, names patterns that did not
 * compile (the rest applied). Display only: what is stored is unaffected. Only running-config, startup-config and
 * inventory are diffed; other types are refused, as are two versions that
 * differ in more than 4000 lines. Free it. */
char *omegacat_store_diff(long long store, const char *canonical, const char *type,
                          const char *older_file, const char *newer_file, int context);

/* The diff ignore rules file, ~/.omegacat/diff-ignore.yaml by default and
 * written with defaults the first time a diff needs it. A non-NULL, non-empty
 * path replaces it for this process. Returns the file in use. Free it. */
char *omegacat_diff_ignore_path(const char *path);

/* 1 when omegacat_store_diff accepts this type, else 0. */
int omegacat_store_diffable(const char *type);

#ifdef __cplusplus
} /* extern "C" */
#endif

#endif /* OMEGACAT_STORE_H */
