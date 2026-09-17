/* include/omegacat/omegacat.h
 *
 * Stable C surface over the omegacat capture run model. Hand-written: cgo
 * emits a header too (libomegacat.h, in the build directory) and it is an
 * artifact, not the contract.
 *
 * Other surfaces, each its own header and its own handle namespace:
 *   omegacat/vault.h    credentials; a capture takes an unlocked vault handle
 *   omegacat/store.h    browse what a store holds, with no run
 *   omegacat/inventory.h  the device inventory, and importing omegamaps map.json
 *   omegacat/search.h   search a store
 *
 * WHAT A RUN IS. A capture -- or the scripted demo -- as a handle whose state
 * is pulled: progress in one consistent read, and the rows and decisions that
 * changed after a sequence number. Nothing after the open depends on where the
 * run came from, so a view built against the demo works against a live
 * capture unchanged.
 *
 * A row is one (device, capture type) pair, not one device. A device whose
 * running-config stored and whose inventory failed is not a failed device, and
 * a device that could not be reached still owns one failed row per selected
 * type -- which is how it stays distinguishable from a device nobody asked
 * about. Key rows on (identity, type).
 *
 * DELIVERY MODEL. No callback into C. Each run owns a notifier;
 * omegacat_notify_handle returns its readable end -- an fd on POSIX, a socket
 * on Windows -- which the caller watches with QSocketNotifier. When it becomes
 * readable, drain it, then pull:
 *
 *     progress  = omegacat_progress(h);           // FIRST
 *     rows      = omegacat_rows_since(h, rseq);   // then these
 *     decisions = omegacat_decisions_since(h, dseq);
 *
 * Order matters at the end of a run. If progress says finished, the run can no
 * longer change, so rows and decisions read after it are complete. Read the
 * other way round, the last changes can land between the row read and a
 * "finished" progress read, and a caller that stops pulling on finished loses
 * them. One wake covers any number of changes; always pull everything since
 * the last seq rather than counting wakes.
 *
 * A running pair's duration moves without any event, so a view showing
 * "running for 12s" polls omegacat_progress on a timer while the run is
 * unfinished. That is the only reason to call without a wake.
 *
 * THREADING. Every call is safe from any thread and none blocks on the
 * network. omegacat_capture_open returns at once; name resolution, the session
 * file and every connection happen on the run's own goroutines.
 *
 * RESULTS are JSON, UTF-8, owned by the caller and released with
 * omegacat_free. Field names are snake_case and stable; fields may be added,
 * and a reader ignores what it does not know. Arrays are present and never
 * null.
 *
 *   progress        {"seq", "elapsed_ms", "finished", "total", "settled",
 *                    "counts": {"devices","devices_failed","stored",
 *                               "unchanged","not_applicable","failed",
 *                               "running","bytes_stored","new_host_keys",
 *                               "cred_rejections"},
 *                    "running": [row]}   longest-running first
 *   rows_since      {"seq", "rows": [row]}   rows changed after seq
 *   decisions_since {"seq", "decisions": [{"seq","at_ms","kind","identity",
 *                    "type","name","platform","detail","text"}]}
 *
 *   row  {"seq","identity","name","display","type","platform","state",
 *         "command","bytes","sha256","path","file","detail","duration_ms",
 *         "parse_status","template","parse_score","parse_records"}
 *
 * state is one of running, stored, unchanged, "not applicable", failed.
 * stored, unchanged and "not applicable" are all success; unchanged is the
 * common answer on a schedule, not a warning.
 *
 * identity is the run's key for the device -- the string the device list
 * used -- and never changes. name is the canonical name once the binding
 * store or the device's own prompt supplied one, and is what the store files
 * the capture under; display is name, or identity until there is one.
 *
 * file is set once the pair has a file on disk: on stored, the file written;
 * on unchanged, the existing file that matched. (name, type, file) is exactly
 * what omegacat_store_read takes. path is the same file's full path.
 *
 * parse_status is present only for a type that is parsed (ARP and MAC tables):
 * "parsed" with the template, its score and the record count, or "no-match"
 * with the closest template and its score. The records themselves are read
 * with omegacat_store_parsed. A parse never changes state: a table that no
 * template reads is still "stored".
 *
 * total grows as devices are visited; settled / total is the fraction of
 * pairs done so far, not a forecast.
 *
 * ERRORS. A failing call returns -1 or NULL and leaves a message in
 * omegacat_last_error(), which is per OS thread and cleared by the next
 * successful call on that thread.
 */

#ifndef OMEGACAT_OMEGACAT_H
#define OMEGACAT_OMEGACAT_H

#ifdef __cplusplus
extern "C" {
#endif

/* The library's version string, as the command-line tools report it. */
char *omegacat_version(void);

/* Releases any char* this library returned. NULL is fine. */
void omegacat_free(void *p);

/* The last failure on the calling thread, or "" -- never NULL. Free it. */
char *omegacat_last_error(void);

/* The capture types a request can name, as a JSON array:
 *   [{"type","description","keep","expensive","default","platforms":[...]}]
 * keep is the retained versions per device (0 = unlimited). default marks the
 * types a request with no "types" captures. */
char *omegacat_types(void);

/* ---------------------------------------------------------------------------
 * Opening a run
 */

/* The request a form starts from, as JSON: the same defaults the command line
 * uses, so the window and the CLI cannot drift apart on what "default" means.
 *
 *   {"devices":[], "types":["running-config"], "concurrency":5,
 *    "expensive_concurrency":1, "timeout_ms":60000, "host_keys":"strict"}
 *
 * The full request, every field optional except where noted:
 *
 *   devices               ["172.16.1.2", "lab-r1.lab.local:2222", ...]
 *                         entries may carry newlines and # comments, as a
 *                         device-list file does
 *   device_file           path to a device list, read when the run starts
 *   session_file, match   a session inventory and the globs that select from
 *                         it ("*" is all; empty selects nothing)
 *   session_keys          or: exact transport:host:port keys selecting from
 *                         session_file (omegacat/inventory.h)
 *   types                 capture type names; empty means the default set
 *   store_path            REQUIRED: the store root
 *   concurrency, expensive_concurrency, timeout_ms
 *   domains               suffixes stripped when deriving an identity
 *   cred_tags             offer only credentials carrying all of these
 *   host_keys             "strict" (default) or "tofu". There is no insecure
 *                         mode: it would also stop noticing a key that changed.
 *   known_hosts_path      default ~/.ssh/known_hosts
 *   legacy                enable SHA-1 KEX, CBC ciphers and ssh-rsa for old gear
 *   log_path              default <store_path>/logs/capture-<UTC stamp>.log
 *   no_parse              store ARP and MAC tables without parsing them
 *   templates_path        TextFSM template database; default
 *                         ~/.omegacat/tfsm_templates.db, copied from the
 *                         shipped database the first time it is needed
 *
 * At least one of devices, device_file or session_file is required. */
char *omegacat_capture_defaults(void);

/* Validates a request without running it. Returns a JSON array of problems,
 * "[]" when there are none: [{"field","message"}], field named as in the
 * request, so a form can mark the widget. NULL only for JSON that does not
 * parse. */
char *omegacat_capture_validate(const char *request_json);

/* Starts a capture. vault is a handle from omegacat/vault.h, unlocked; it is
 * REQUIRED, because credentials never cross this surface -- they are resolved
 * inside Go per device. The vault must stay open and unlocked for as long as
 * the run is; locking it mid-run fails the devices not yet dialed.
 *
 * Returns a run handle > 0, or -1 when the request is invalid (every problem
 * is in omegacat_last_error), the vault is locked, or the log cannot be
 * created. A problem found once the run is under way -- a session file that
 * no longer parses, a device list that resolves to nothing -- ends the run
 * with state "failed" and the reason in omegacat_run_result.
 *
 * A run that parses opens the template database the first time it is needed
 * and keeps it for the life of the process, per path: opening compiles every
 * template, a few seconds, paid once. A database that will not open fails the
 * run (state "failed") rather than quietly storing tables unparsed; set
 * no_parse to capture without it. */
long long omegacat_capture_open(long long vault, const char *request_json);

/* Plays the scripted demo run -- every state, a device that never answers, a
 * Junos box with no startup-config -- with step_ms between events; 0 delivers
 * everything at once. For building and checking views with no lab. Its rows'
 * paths are not real files. */
long long omegacat_demo_open(int step_ms);

/* ---------------------------------------------------------------------------
 * A run, once open
 */

/* The notifier's readable end, as above. Owned by the run; closed by
 * omegacat_close. -1 on a bad handle. */
long long omegacat_notify_handle(long long run);

char *omegacat_progress(long long run);
char *omegacat_rows_since(long long run, unsigned long long seq);
char *omegacat_decisions_since(long long run, unsigned long long seq);

/* How the run ended and what it was:
 *   {"kind":"capture"|"demo", "state":"running"|"done"|"cancelled"|"failed",
 *    "error", "store_path", "log_path", "devices", "types":[...],
 *    "skipped":[...], "notes":{identity: note}}
 * devices, types, skipped and notes are filled once the run has resolved its
 * device list, which is shortly after it starts. skipped names sessions a
 * pattern matched that capture cannot visit; notes are per-device identity
 * decisions, such as a CGNAT address that became a name.
 * state is final before progress reports finished, so a caller that reads
 * this on "finished" always sees how the run ended. */
char *omegacat_run_result(long long run);

/* Asks the run to stop. Devices not yet dialed fail with the cancellation;
 * commands already on the wire finish or time out. The run then finishes
 * normally, with state "cancelled". 0, or -1 on a bad handle. */
int omegacat_cancel(long long run);

/* Cancels if needed and releases the handle and its notifier. The run's
 * goroutines wind down on their own; nothing about the run can be read
 * afterwards. 0, or -1 on a bad handle. */
int omegacat_close(long long run);

#ifdef __cplusplus
} /* extern "C" */
#endif

#endif /* OMEGACAT_OMEGACAT_H */
