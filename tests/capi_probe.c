/* tests/capi_probe.c
 *
 * Drives the C surface the way the Qt application will, with no Qt: a vault
 * made and filled through vault.h, a live capture from fake devices through
 * the notifier and the pull calls, the same capture again to prove dedup, the
 * store browsed and read through store.h, a search through search.h, and the
 * demo run. Plain C, so what it proves is the ABI and nothing a C++ wrapper
 * might be papering over.
 *
 *   capi_probe [path/to/fakedevice [path/to/tfsm_templates.db]]
 *
 * Without the fakedevice path, the live half is skipped (loudly) and the demo
 * and error-path checks still run. Exit status is the number of failed checks.
 *
 * JSON is checked with substring and integer lookups rather than a parser.
 * That is enough to prove the calls answer what the headers promise; the
 * values themselves are tested in Go, beside the code that computes them.
 */

#include <omegacat/omegacat.h>
#include <omegacat/search.h>
#include <omegacat/store.h>
#include <omegacat/templates.h>
#include <omegacat/vault.h>

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#ifdef _WIN32
#include <direct.h>
#include <process.h>
#include <winsock2.h>
#include <windows.h>
#define popen _popen
#define pclose _pclose
#else
#include <sys/select.h>
#include <sys/types.h>
#include <unistd.h>
#endif

static int failures = 0;

static void check(int ok, const char *what) {
    printf("%s  %s\n", ok ? "ok  " : "FAIL", what);
    if (!ok) failures++;
}

static void checkf(int ok, const char *fmt, const char *arg) {
    char buf[512];
    snprintf(buf, sizeof buf, fmt, arg);
    check(ok, buf);
}

/* ---------------------------------------------------------------------------
 * Small JSON helpers: enough for assertions, deliberately not a parser. */

/* The integer after the first "key": at or after from. -1 when absent. */
static long long json_int_from(const char *json, const char *key, const char **after) {
    char needle[128];
    snprintf(needle, sizeof needle, "\"%s\":", key);
    const char *p = json ? strstr(json, needle) : NULL;
    if (!p) return -1;
    p += strlen(needle);
    char *end;
    long long v = strtoll(p, &end, 10);
    if (end == p) return -1;
    if (after) *after = end;
    return v;
}

static long long json_int(const char *json, const char *key) {
    return json_int_from(json, key, NULL);
}

/* Whether "key":true appears. */
static int json_true(const char *json, const char *key) {
    char needle[128];
    snprintf(needle, sizeof needle, "\"%s\":true", key);
    return json && strstr(json, needle) != NULL;
}

static int count(const char *hay, const char *needle) {
    int n = 0;
    size_t len = strlen(needle);
    for (const char *p = hay; p && (p = strstr(p, needle)) != NULL; p += len) n++;
    return n;
}

/* The string value of the first "key":"..." (no escapes handled; the values
 * this probe reads are names and file names). Caller frees. */
static char *json_str(const char *json, const char *key) {
    char needle[128];
    snprintf(needle, sizeof needle, "\"%s\":\"", key);
    const char *p = json ? strstr(json, needle) : NULL;
    if (!p) return NULL;
    p += strlen(needle);
    const char *q = strchr(p, '"');
    if (!q) return NULL;
    char *out = malloc((size_t)(q - p) + 1);
    memcpy(out, p, (size_t)(q - p));
    out[q - p] = 0;
    return out;
}

static void print_error(const char *what) {
    char *e = omegacat_last_error();
    printf("      %s: %s\n", what, e);
    omegacat_free(e);
}

/* ---------------------------------------------------------------------------
 * Notifier: wait for a wake, drain it. Returns 1 on a wake, 0 on timeout. */

static int wait_wake(long long handle, int timeout_ms) {
#ifdef _WIN32
    SOCKET s = (SOCKET)handle;
    fd_set rd;
    FD_ZERO(&rd);
    FD_SET(s, &rd);
    struct timeval tv = {timeout_ms / 1000, (timeout_ms % 1000) * 1000};
    int r = select(0, &rd, NULL, NULL, &tv);
    if (r <= 0) return 0;
    char buf[256];
    while (recv(s, buf, sizeof buf, 0) > 0) {}
    return 1;
#else
    int fd = (int)handle;
    fd_set rd;
    FD_ZERO(&rd);
    FD_SET(fd, &rd);
    struct timeval tv = {timeout_ms / 1000, (timeout_ms % 1000) * 1000};
    int r = select(fd + 1, &rd, NULL, NULL, &tv);
    if (r <= 0) return 0;
    char buf[256];
    while (read(fd, buf, sizeof buf) > 0) {}
    return 1;
#endif
}

/* ---------------------------------------------------------------------------
 * A run, pulled to the end the way a view pulls it: on each wake, progress
 * first, then rows and decisions since the last seq. Returns the final
 * progress JSON (caller frees) and fills the merged row and decision counts. */

typedef struct {
    int wakes;
    int row_updates;
    int decisions;
    int saw_running_progress;
} pull_stats;

static char *pull_to_end(long long run, int timeout_s, pull_stats *stats) {
    long long notify = omegacat_notify_handle(run);
    unsigned long long rseq = 0, dseq = 0;
    char *progress = NULL;
    time_t deadline = time(NULL) + timeout_s;
    memset(stats, 0, sizeof *stats);

    for (;;) {
        if (wait_wake(notify, 250)) stats->wakes++;

        omegacat_free(progress);
        progress = omegacat_progress(run);
        if (!progress) return NULL;
        int finished = json_true(progress, "finished");
        if (!finished) stats->saw_running_progress = 1;

        char *rows = omegacat_rows_since(run, rseq);
        if (rows) {
            stats->row_updates += count(rows, "\"identity\":");
            long long next = json_int(rows, "seq");
            if (next >= 0) rseq = (unsigned long long)next;
            omegacat_free(rows);
        }
        char *decs = omegacat_decisions_since(run, dseq);
        if (decs) {
            stats->decisions += count(decs, "\"kind\":");
            long long next = json_int(decs, "seq");
            if (next >= 0) dseq = (unsigned long long)next;
            omegacat_free(decs);
        }

        if (finished) return progress;
        if (time(NULL) > deadline) {
            printf("      run did not finish within %ds; last progress: %s\n", timeout_s, progress);
            return progress;
        }
    }
}

/* ---------------------------------------------------------------------------
 * Temp directory */

static int make_temp_dir(char *out, size_t size) {
#ifdef _WIN32
    char base[MAX_PATH];
    if (!GetTempPathA(sizeof base, base)) return 0;
    snprintf(out, size, "%somegacat-probe-%d-%lld", base, _getpid(), (long long)time(NULL));
    return _mkdir(out) == 0;
#else
    const char *tmp = getenv("TMPDIR");
    snprintf(out, size, "%s/omegacat-probe-XXXXXX", tmp && *tmp ? tmp : "/tmp");
    return mkdtemp(out) != NULL;
#endif
}

/* ---------------------------------------------------------------------------
 * The demo and the error paths: no devices needed. */

static void probe_basics(void) {
    char *v = omegacat_version();
    check(v && *v, "version is a non-empty string");
    omegacat_free(v);

    char *types = omegacat_types();
    check(types && strstr(types, "\"type\":\"running-config\"") && strstr(types, "\"default\":true"),
          "types lists running-config and marks a default");
    omegacat_free(types);

    char *defaults = omegacat_capture_defaults();
    check(defaults && strstr(defaults, "\"host_keys\":\"strict\"") && json_int(defaults, "timeout_ms") > 0,
          "capture defaults: strict host keys and a timeout");
    omegacat_free(defaults);

    char *probs = omegacat_capture_validate("{\"devices\":[],\"host_keys\":\"insecure\"}");
    check(probs && strstr(probs, "\"field\":\"devices\"") && strstr(probs, "\"field\":\"store_path\"") &&
              strstr(probs, "\"field\":\"host_keys\""),
          "validate names devices, store_path and host_keys by request field");
    omegacat_free(probs);

    probs = omegacat_capture_validate("{\"devices\":[\"lab-r1\"],\"store_path\":\"/tmp/x\"}");
    check(probs && strcmp(probs, "[]") == 0, "a complete request validates clean");
    omegacat_free(probs);

    check(omegacat_capture_open(0, "{\"devices\":[\"lab-r1\"],\"store_path\":\"/tmp/x\"}") == -1,
          "capture_open refuses to run without a vault");
    char *err = omegacat_last_error();
    check(err && strstr(err, "vault"), "and says the vault is why");
    omegacat_free(err);

    check(omegacat_progress(424242) == NULL, "a bad run handle is refused");
    check(omegacat_store_devices(424242) == NULL, "a bad store handle is refused");

    /* The demo, paced, pulled like a view. */
    long long demo = omegacat_demo_open(2);
    check(demo > 0, "demo opens");
    pull_stats st;
    char *progress = pull_to_end(demo, 30, &st);
    check(progress && json_true(progress, "finished"), "demo finishes");
    check(st.wakes > 0, "the notifier woke during the demo");
    check(st.saw_running_progress, "progress was seen before finished");

    char *all = omegacat_rows_since(demo, 0);
    int rows = count(all, "\"identity\":");
    long long total = json_int(progress, "total");
    check(rows > 0 && rows == total, "rows_since(0) has every row progress counts");
    check(strstr(all, "\"state\":\"not applicable\"") && strstr(all, "\"state\":\"failed\"") &&
              strstr(all, "\"state\":\"stored\"") && strstr(all, "\"state\":\"unchanged\""),
          "demo rows include every terminal state");
    check(!strstr(all, "\"state\":\"running\""), "no row is left running");
    omegacat_free(all);
    check(st.decisions > 0, "decisions were delivered");

    char *res = omegacat_run_result(demo);
    check(res && strstr(res, "\"kind\":\"demo\"") && strstr(res, "\"state\":\"done\""),
          "demo result: kind demo, state done");
    omegacat_free(res);
    omegacat_free(progress);
    check(omegacat_close(demo) == 0, "demo closes");
    check(omegacat_close(demo) == -1, "a closed handle is gone");

    /* Cancel mid-run. */
    long long slow = omegacat_demo_open(50);
    wait_wake(omegacat_notify_handle(slow), 1000);
    check(omegacat_cancel(slow) == 0, "demo cancels");
    progress = pull_to_end(slow, 10, &st);
    res = omegacat_run_result(slow);
    check(res && strstr(res, "\"state\":\"cancelled\""), "a cancelled demo reports cancelled");
    omegacat_free(res);
    omegacat_free(progress);
    omegacat_close(slow);
}

/* ---------------------------------------------------------------------------
 * The live half. */

static long long capture_once(long long vault, const char *request, const char *label,
                              char **progress_out) {
    long long run = omegacat_capture_open(vault, request);
    checkf(run > 0, "%s: capture opens", label);
    if (run <= 0) {
        print_error("capture_open");
        return -1;
    }
    pull_stats st;
    char *progress = pull_to_end(run, 60, &st);
    checkf(progress && json_true(progress, "finished"), "%s: capture finishes", label);
    *progress_out = progress;
    return run;
}

static void probe_live(const char *fakedevice, const char *templates) {
    char dir[1024];
    if (!make_temp_dir(dir, sizeof dir)) {
        check(0, "make a temp directory");
        return;
    }
    printf("      working in %s\n", dir);

    /* The devices. */
    FILE *lab = popen(fakedevice, "r");
    char line[4096];
    if (!lab || !fgets(line, sizeof line, lab) || !strstr(line, "\"ready\":true")) {
        check(0, "fakedevice starts and reports ready");
        return;
    }
    check(1, "fakedevice starts and reports ready");

    /* The ready line's device addresses, as a devices array. */
    char devices[1024] = "[";
    const char *p = line;
    int ndev = 0;
    while ((p = strstr(p, "\"addr\":\"")) != NULL) {
        p += 8;
        const char *q = strchr(p, '"');
        if (!q) break;
        size_t used = strlen(devices);
        snprintf(devices + used, sizeof devices - used, "%s\"%.*s\"", ndev ? "," : "", (int)(q - p), p);
        ndev++;
        p = q;
    }
    strncat(devices, "]", sizeof devices - strlen(devices) - 1);
    check(ndev == 2, "two fake devices");
    char *user = json_str(line, "user");
    char *password = json_str(line, "password");

    /* A vault, through the vault surface. */
    char path[1200];
    snprintf(path, sizeof path, "%s/vault.json", dir);
    long long vault = omegacat_vault_open(path);
    check(vault > 0, "vault opens");
    check(omegacat_vault_create(vault, "lab-master-passphrase") == OMEGACAT_VAULT_OK, "vault is created");

    char cred[1024];
    snprintf(cred, sizeof cred,
             "{\"name\":\"lab\",\"username\":\"%s\",\"auth_type\":\"password\",\"password\":\"%s\",\"tags\":[\"lab\"]}",
             user, password);
    char *id = NULL;
    check(omegacat_vault_store(vault, cred, &id) == OMEGACAT_VAULT_OK && id && *id, "a password credential is stored");
    omegacat_free(id);
    id = NULL;
    check(omegacat_vault_store(vault, "{\"name\":\"ro\",\"auth_type\":\"snmp-v2c\",\"password\":\"public\"}", &id) ==
              OMEGACAT_VAULT_ERR_BAD_ARGUMENT && id == NULL,
          "an SNMP credential is refused");
    char *list = NULL;
    check(omegacat_vault_list(vault, &list) == OMEGACAT_VAULT_OK && list && !strstr(list, password),
          "list carries no secret material");
    omegacat_free(list);

    /* Capture, twice. */
    char store[1200];
    snprintf(store, sizeof store, "%s/store", dir);
    char request[4096];
    snprintf(request, sizeof request,
             "{\"devices\":%s,\"types\":[\"running-config\",\"inventory\"],\"store_path\":\"%s\","
             "\"host_keys\":\"tofu\",\"known_hosts_path\":\"%s/known_hosts\",\"cred_tags\":[\"lab\"],"
             "\"timeout_ms\":10000,\"no_parse\":true}",
             devices, store, dir);

    char *progress = NULL;
    long long run = capture_once(vault, request, "first run", &progress);
    if (run > 0) {
        long long stored = json_int(progress, "stored");
        long long failed = json_int(progress, "failed");
        long long keys = json_int(progress, "new_host_keys");
        printf("      first run: %s\n", progress);
        check(failed == 0, "first run: nothing failed");
        check(stored == 2, "first run: both running-configs stored");
        check(keys == 2, "first run: both host keys trusted on first contact");

        char *rows = omegacat_rows_since(run, 0);
        check(count(rows, "\"identity\":") == 4, "first run: four rows, one per (device, type)");
        check(strstr(rows, "\"name\":\"lab-r1\"") && strstr(rows, "\"name\":\"lab-spine-1\""),
              "first run: devices named from their prompts");
        check(strstr(rows, "\"file\":\"") != NULL, "first run: stored rows carry a file");
        omegacat_free(rows);

        char *res = omegacat_run_result(run);
        check(res && strstr(res, "\"state\":\"done\"") && json_int(res, "devices") == 2,
              "first run: result done, two devices");
        char *log_path = json_str(res, "log_path");
        FILE *logf = log_path ? fopen(log_path, "r") : NULL;
        check(logf != NULL, "first run: the log file exists");
        if (logf) fclose(logf);
        free(log_path);
        omegacat_free(res);
        omegacat_free(progress);
        omegacat_close(run);
    }

    /* Strict now: the keys are known. */
    char strict[4096];
    snprintf(strict, sizeof strict,
             "{\"devices\":%s,\"types\":[\"running-config\",\"inventory\"],\"store_path\":\"%s\","
             "\"host_keys\":\"strict\",\"known_hosts_path\":\"%s/known_hosts\",\"timeout_ms\":10000}",
             devices, store, dir);
    run = capture_once(vault, strict, "second run", &progress);
    if (run > 0) {
        printf("      second run: %s\n", progress);
        check(json_int(progress, "unchanged") == 2 && json_int(progress, "stored") == 0,
              "second run: both configs unchanged, nothing stored");
        check(json_int(progress, "failed") == 0, "second run: strict host keys accept the recorded keys");
        omegacat_free(progress);
        omegacat_close(run);
    }

    /* ARP, parsed at capture time against the given template database. */
    if (templates) {
        char arpreq[4096];
        snprintf(arpreq, sizeof arpreq,
                 "{\"devices\":%s,\"types\":[\"arp-table\"],\"store_path\":\"%s\","
                 "\"host_keys\":\"strict\",\"known_hosts_path\":\"%s/known_hosts\","
                 "\"timeout_ms\":10000,\"templates_path\":\"%s\"}",
                 devices, store, dir, templates);
        run = capture_once(vault, arpreq, "arp run", &progress);
        if (run > 0) {
            char *rows = omegacat_rows_since(run, 0);
            check(rows && strstr(rows, "\"parse_status\":\"parsed\"") && strstr(rows, "\"template\":\"cisco_ios_show_"),
                  "arp run: the IOS table is parsed, with its template on the row");
            check(rows && json_int(rows, "parse_records") == 3, "arp run: three records parsed");
            omegacat_free(rows);
            omegacat_free(progress);
            omegacat_close(run);
        }
    } else {
        printf("SKIP  arp parsing: no template database path given\n");
    }

    /* The store. */
    long long s = omegacat_store_open(store);
    check(s > 0, "store opens");
    char *devs = omegacat_store_devices(s);
    check(devs && strstr(devs, "\"canonical\":\"lab-r1\"") && strstr(devs, "\"canonical\":\"lab-spine-1\""),
          "store lists both devices by canonical name");
    omegacat_free(devs);

    char *types = omegacat_store_types(s, "lab-r1");
    check(types && strstr(types, "\"type\":\"running-config\""), "store types for lab-r1 include running-config");
    char *file = NULL;
    const char *rc = types ? strstr(types, "\"type\":\"running-config\"") : NULL;
    if (rc) {
        file = json_str(rc, "file");
        check(json_int(rc, "attempts") == 2 && json_int(rc, "stored") == 1,
              "running-config: two attempts, one stored version");
    }
    omegacat_free(types);

    char *hist = omegacat_store_history(s, "lab-r1", "running-config");
    check(hist && count(hist, "\"at_ms\":") == 2 && strstr(hist, "\"unchanged\":true"),
          "history: two attempts, the second unchanged");
    omegacat_free(hist);

    if (file) {
        long long n = -1;
        char *content = omegacat_store_read(s, "lab-r1", "running-config", file, &n);
        check(content && n > 0 && (long long)strlen(content) == n && strstr(content, "hostname lab-r1"),
              "read returns the config, length matches, NUL-terminated");
        omegacat_free(content);
        free(file);
    }
    if (templates) {
        char *atypes = omegacat_store_types(s, "lab-r1");
        const char *at = atypes ? strstr(atypes, "\"type\":\"arp-table\"") : NULL;
        char *afile = at ? json_str(at, "file") : NULL;
        char *parsed = afile ? omegacat_store_parsed(s, "lab-r1", "arp-table", afile) : NULL;
        check(parsed && strstr(parsed, "\"status\":\"parsed\"") && count(parsed, "\"ADDRESS\":") + count(parsed, "\"IP_ADDRESS\":") >= 3 &&
                  strstr(parsed, "0c1d.5e2f.0101"),
              "store_parsed returns the ARP records for the stored file");
        char *none = omegacat_store_parsed(s, "lab-r1", "running-config", "x.txt");
        check(none && strcmp(none, "null") == 0, "store_parsed is null for a type that is not parsed");
        omegacat_free(none);
        omegacat_free(parsed);
        free(afile);
        omegacat_free(atypes);
    }
    check(omegacat_store_read(s, "lab-r1", "running-config", "../../vault.json", NULL) == NULL,
          "read refuses a path outside the store");

    /* Search. */
    long long q = omegacat_search_open(s, "{\"query\":\"HOSTNAME\"}");
    check(q > 0, "search opens");
    long long qn = omegacat_search_notify_handle(q);
    char *sp = NULL;
    time_t deadline = time(NULL) + 20;
    for (;;) {
        wait_wake(qn, 250);
        omegacat_free(sp);
        sp = omegacat_search_progress(q);
        if (json_true(sp, "finished") || time(NULL) > deadline) break;
    }
    check(json_true(sp, "finished"), "search finishes");
    omegacat_free(sp);
    char *sr = omegacat_search_result(q);
    check(sr && count(sr, "\"device\":") >= 2 && strstr(sr, "\"device\":\"lab-r1\""),
          "case-insensitive search finds hostname in both devices");
    check(sr && strstr(sr, "\"capped\":false"), "search result is not capped");
    omegacat_free(sr);
    omegacat_search_close(q);
    check(omegacat_search_open(s, "{\"query\":\"   \"}") == -1, "an empty query is refused");

    omegacat_store_close(s);
    omegacat_vault_close(vault);
    free(user);
    free(password);
    /* lab is left open on purpose: fakedevice exits when this process does. */
    (void)lab;
}


/* ---------------------------------------------------------------------------
 * The Template Lab, on a copy of the shipped template database: a test, a
 * sweep, a save the next sweep sees without reopening anything, and a delete.
 * Never the user's ~/.omegacat copy. */

static int copy_file(const char *from, const char *to) {
    FILE *in = fopen(from, "rb");
    if (!in) return 0;
    FILE *out = fopen(to, "wb");
    if (!out) { fclose(in); return 0; }
    char buf[65536];
    size_t n;
    int ok = 1;
    while ((n = fread(buf, 1, sizeof buf, in)) > 0)
        if (fwrite(buf, 1, n, out) != n) { ok = 0; break; }
    fclose(in);
    if (fclose(out) != 0) ok = 0;
    return ok;
}

/* Lab output and a template written without backslashes, so the JSON below
 * needs no second layer of escaping. */
#define LAB_RAW "lab-leaf-1#show lab widgets\\n172.16.10.1  0:00:12  001c.7300.0001  Vlan10\\n172.16.10.20  0:01:40  001c.7300.0020  Vlan10\\n172.16.20.1  0:00:03  001c.7300.0101  Vlan20\\nlab-leaf-1#"
#define LAB_TPL "Value ADDRESS ([0-9.]+)\\nValue MAC ([0-9a-f.]+)\\nValue VLAN (Vlan[0-9]+)\\n\\nStart\\n  ^${ADDRESS} +[^ ]+ +${MAC} +${VLAN} -> Record\\n"

static void probe_templates(const char *seed) {
    char dir[512], db[640], req[4096];
    if (!make_temp_dir(dir, sizeof dir)) {
        check(0, "templates: temp dir");
        return;
    }
    snprintf(db, sizeof db, "%s/tfsm_templates.db", dir);
    check(copy_file(seed, db), "templates: seed database copied");

    char *plats = omegacat_templates_platforms(db);
    check(plats && strstr(plats, "{\"platform\":\"juniper_junos\",\"count\":16}"),
          "templates: platforms counted as netlapse counts them");
    omegacat_free(plats);

    snprintf(req, sizeof req,
             "{\"raw_output\":\"" LAB_RAW "\",\"textfsm_content\":\"" LAB_TPL "\",\"command\":\"show lab widgets\"}");
    char *t = omegacat_templates_test(req);
    check(t && json_true(t, "success") && json_int(t, "record_count") == 3,
          "templates: an ad-hoc template parses cleaned output");
    omegacat_free(t);
    snprintf(req, sizeof req,
             "{\"raw_output\":\"" LAB_RAW "\",\"textfsm_content\":\"Value A ([0-9\\nStart\\n\",\"command\":\"\"}");
    t = omegacat_templates_test(req);
    check(t && strstr(t, "\"compiled\":false") && strstr(t, "\"error_type\":\"compile\""),
          "templates: a broken template is a compile-error result, not a NULL");
    omegacat_free(t);

    snprintf(req, sizeof req,
             "{\"templates_path\":\"%s\",\"raw_output\":\"" LAB_RAW "\",\"platform\":\"arista_eos\",\"command\":\"show lab widgets\"}", db);
    char *sw = omegacat_templates_sweep(req);
    check(sw && strstr(sw, "\"candidates\":[]") && strstr(sw, "\"filter\":\"arista_eos_show_lab_widgets\""),
          "templates: before the save, the sweep has no candidate for the command");
    omegacat_free(sw);

    char save[4096];
    snprintf(save, sizeof save,
             "{\"templates_path\":\"%s\",\"cli_command\":\"arista_eos_show_lab_widgets\",\"textfsm_content\":\"" LAB_TPL "\"}", db);
    char *sv = omegacat_templates_save(save);
    long long rowid = sv ? json_int(sv, "rowid") : 0;
    check(sv && rowid > 0 && json_int(sv, "id") > 0 && !strstr(sv, "reload_error"),
          "templates: a new template saves with a rowid and an id");
    omegacat_free(sv);
    char *dup = omegacat_templates_save(save);
    char *dupErr = omegacat_last_error();
    check(dup == NULL && dupErr && strstr(dupErr, "already exists"), "templates: a second template under one name is refused");
    omegacat_free(dup);
    omegacat_free(dupErr);

    sw = omegacat_templates_sweep(req);
    check(sw && strstr(sw, "\"template\":\"arista_eos_show_lab_widgets\"") && json_true(sw, "success"),
          "templates: the same engine sweeps with the saved template, no reopen");
    omegacat_free(sw);

    char *got = omegacat_templates_get(db, rowid);
    check(got && strstr(got, "\"cli_command\":\"arista_eos_show_lab_widgets\""), "templates: get by rowid");
    omegacat_free(got);
    char *list = omegacat_templates_list(db, "arista_eos", "LAB_WIDGETS");
    check(list && count(list, "\"rowid\":") == 1, "templates: list filters by platform and query");
    omegacat_free(list);

    check(omegacat_templates_delete(db, rowid) == 0, "templates: delete");
    check(omegacat_templates_delete(db, rowid) == -1, "templates: deleting it again is refused");
    sw = omegacat_templates_sweep(req);
    check(sw && strstr(sw, "\"candidates\":[]"), "templates: after the delete, the engine no longer has it");
    omegacat_free(sw);
    check(omegacat_templates_get(db, rowid) == NULL, "templates: get after delete is NULL");
}

int main(int argc, char **argv) {
    probe_basics();
    if (argc > 2) {
        probe_templates(argv[2]);
    } else {
        printf("SKIP  templates: no template database path given\n");
    }
    if (argc > 1) {
        probe_live(argv[1], argc > 2 ? argv[2] : NULL);
    } else {
        printf("SKIP  live capture: no fakedevice path given\n");
    }
    printf("\n%d failure(s)\n", failures);
    return failures;
}
