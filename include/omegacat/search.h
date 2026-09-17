/* include/omegacat/search.h
 *
 * Literal search across the current version of every capture in a store.
 * "Which devices reference this prefix", "which port is this MAC on", "is the
 * old NTP server gone everywhere" -- answered from disk, with no device
 * contacted.
 *
 *     long long q = omegacat_search_open(store, "{\"query\":\"ntp server\"}");
 *     ...watch omegacat_search_notify_handle(q); on each wake, drain it and
 *        read omegacat_search_progress(q) until "finished"...
 *     char *res = omegacat_search_result(q);
 *     omegacat_search_close(q);
 *
 * The request:
 *   {"query": "...",            REQUIRED, matched literally, not as a regex
 *    "case_sensitive": false,
 *    "types": ["running-config"],   empty searches every type
 *    "limit": 5000}                 hits held; default 5000
 *
 * Only the newest stored file of each (device, type) is searched, not the
 * history: a year of nightly captures of an unchanged device is one file, and
 * searching every version would return the same line hundreds of times.
 *
 * Matching is literal, so an address must be typed the way the device prints
 * it: 0011.2233.4455 does not match 00:11:22:33:44:55.
 *
 * DELIVERY. Simpler than a run: progress while it goes, then the whole result
 * once. Hits are sorted by device, type, line, so the same query over an
 * unchanged store renders identically twice.
 *
 *   progress {"done","total","finished","state","error"}
 *            done / total count artifacts; state is running, done,
 *            cancelled or failed
 *   result   {"state","error","hits":[hit],"capped","limit","devices",
 *             "artifacts","bytes","skips":[skip],"warning","elapsed_ms",
 *             "summary"}
 *   hit      {"device","type","file","line","text","truncated","indent"}
 *   skip     {"device","type","file","reason","error"}
 *
 * capped means the limit was reached and hits is the beginning of the answer,
 * not all of it; say so. skips are artifacts that were not searched (too large,
 * not text, unreadable) and must be shown: a search that silently skipped a
 * device reports the same empty result as one that looked and found nothing.
 * line is 1-based; text is trimmed of leading whitespace, indent is how much
 * was trimmed, and truncated says text is shorter than the line. (device,
 * type, file) is what omegacat_store_read takes to show the hit in context.
 *
 * THREADING. Every call is safe from any thread and returns at once. The store
 * handle must stay open until the search finishes.
 *
 * ERRORS. -1 or NULL, with the reason in omegacat_last_error().
 */

#ifndef OMEGACAT_SEARCH_H
#define OMEGACAT_SEARCH_H

#include <omegacat/omegacat.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Starts a search. -1 on a bad store handle, bad JSON, or an empty query. */
long long omegacat_search_open(long long store, const char *request_json);

/* The notifier's readable end. Owned by the search; closed by close. */
long long omegacat_search_notify_handle(long long search);

char *omegacat_search_progress(long long search);

/* The result, once progress says finished. NULL (with a reason) before. */
char *omegacat_search_result(long long search);

/* 0, or -1 on a bad handle. A cancelled search finishes with state
 * "cancelled" and whatever it had found. */
int omegacat_search_cancel(long long search);
int omegacat_search_close(long long search);

#ifdef __cplusplus
} /* extern "C" */
#endif

#endif /* OMEGACAT_SEARCH_H */
