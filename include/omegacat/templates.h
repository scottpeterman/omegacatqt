/* include/omegacat/templates.h
 *
 * The Template Lab: test a TextFSM template against output, sweep the template
 * database the way a parse does, and add, edit and delete templates.
 *
 *     char *r = omegacat_templates_test(
 *         "{\"raw_output\":\"...\",\"textfsm_content\":\"...\",\"command\":\"show ip arp\"}");
 *     char *s = omegacat_templates_sweep(
 *         "{\"raw_output\":\"...\",\"platform\":\"arista_eos\",\"command\":\"show ip arp\"}");
 *     ... omegacat_free each ...
 *
 * The semantics are netlapse's (parser/engine.py, web/api/admin.py), because the
 * same database is edited in both: a sweep cleans the output, filters on the
 * platform plus the command ("arista_eos" + "show ip arp" ->
 * arista_eos_show_ip_arp), accepts a parse scoring 15 or more, and when that
 * fails retries on the vendor alone ("arista").
 *
 * DATABASE. Every db argument, and "templates_path" in a request, may be NULL
 * or "" for the database captures use by default, ~/.omegacat/tfsm_templates.db
 * (copied from the shipped one on first use). Rows are addressed by "rowid":
 * the id column is not a key, and some rows have none. A save gives a row
 * without an id the next one, so netlapse can address it too.
 *
 * ONE ENGINE. A sweep uses the same in-memory engine a capture on that
 * database uses, and a save or delete reloads it: the next capture parses
 * with the edit, with no restart.
 *
 * THREADING. Safe from any thread. test is fast. The first sweep on a database
 * compiles every template (seconds), and save and delete re-read them when an
 * engine is open -- call those off the GUI thread.
 *
 * ERRORS. NULL or -1, with the reason in omegacat_last_error(). A save
 * refusal (empty name, a name another row has) is a sentence for the person.
 */

#ifndef OMEGACAT_TEMPLATES_H
#define OMEGACAT_TEMPLATES_H

#include <omegacat/omegacat.h>

#ifdef __cplusplus
extern "C" {
#endif

/* The default template database's path, created from the shipped copy if it
 * does not exist. Free it. */
char *omegacat_templates_default_path(void);

/* [{"platform","count"}], the first two '_' parts of each distinct template
 * name, sorted. Free it. */
char *omegacat_templates_platforms(const char *db);

/* [{"rowid","id","cli_command","source"}] by name, for names starting with
 * platform and containing query (SQLite LIKE; either may be NULL or "").
 * "id" is null for a row without one. Free it. */
char *omegacat_templates_list(const char *db, const char *platform, const char *query);

/* {"rowid","id","cli_command","source","textfsm_content","cli_content",
 *  "textfsm_hash","created"}. NULL for a rowid with no row. Free it. */
char *omegacat_templates_get(const char *db, long long rowid);

/* Runs one template, not the database, against output.
 * Request: {"raw_output","textfsm_content","command","clean"} -- clean (default
 * true) strips the session preamble first; command is used only to recognise
 * version commands in the score.
 * Result: {"compiled","success","error","error_type":"compile"|"runtime",
 *   "rule_line","input_line","header","records","record_count","field_count",
 *   "score","breakdown":{"records","fields","population","consistency","total"}}
 * A template that fails is a result, not an error. Free it. */
char *omegacat_templates_test(const char *request_json);

/* Request: {"templates_path","raw_output","platform","command"}.
 * Result: {"primary":P, "fallback":P (only when the primary failed and the
 *   vendor retry succeeded), "candidates":[{"rowid","cli_command",
 *   "compile_error"}] (the primary filter's, by name)}
 * P: {"success","template","score","record_count","header","records","error",
 *   "filter","tried"}. Free it. */
char *omegacat_templates_sweep(const char *request_json);

/* Request: {"templates_path","rowid" (0 or absent: add),"cli_command",
 *   "textfsm_content"}. Changes the name and content only.
 * Result: {"rowid","id","cli_command","reload_error"} -- reload_error, when
 * present, means the save is on disk but captures keep the previous templates
 * until the engine reloads. Free it. */
char *omegacat_templates_save(const char *request_json);

/* 0 deleted; 1 deleted but the engine did not reload (reason in
 * omegacat_last_error); -1 not deleted. */
int omegacat_templates_delete(const char *db, long long rowid);

#ifdef __cplusplus
}
#endif

#endif
