/* include/omegacat/inventory.h
 *
 * OmegaCat's own device inventory: a session file, by default
 * ~/.omegacat/inventory.yaml, filled by importing omegamaps' map.json.
 *
 *     char *inv = omegacat_inventory_load(NULL);          // default path
 *     char *res = omegacat_inventory_import_map(NULL, "/crawls/lab/map.json", NULL);
 *     ... omegacat_free each ...
 *
 * A capture selects from it with the request's "session_file" (this path) and
 * either "match" (glob patterns tried against each session's name and host) or
 * "session_keys" (each session's "key", exactly). A view that lets a person
 * tick devices sends keys: a pattern never sees the port, so ticking one of
 * several devices port-forwarded behind one address would take all of them.
 *
 * A map import copies the crawled devices (not leaves) into one folder, SSH,
 * addressed by the IP the crawl found, named by the map's key, the crawl's
 * platform kept as device_type. Re-importing recognises a device by its
 * address anywhere or its name in that folder, adds only the rest, and changes
 * nothing on a recognised device but device_type. The map file is never read
 * again.
 *
 * Every path argument may be NULL or "" for the default inventory.
 *
 * THREADING. Local file reads and an atomic rewrite; safe from any thread.
 *
 * ERRORS. -1 or NULL, with the reason in omegacat_last_error().
 */

#ifndef OMEGACAT_INVENTORY_H
#define OMEGACAT_INVENTORY_H

#include <omegacat/omegacat.h>

#ifdef __cplusplus
extern "C" {
#endif

/* ~/.omegacat/inventory.yaml, or "inventory.yaml" with no home directory.
 * Free it. */
char *omegacat_inventory_default_path(void);

/* {"path", "folders": [{"name", "sessions": [{"key","name","host","port",
 *   "transport","platform","device_type","legacy","credential"}]}]}
 * in file order. "platform" is the authority a person set (capture uses it
 * instead of detecting); "device_type" is an import's guess (capture only
 * compares it). A file that does not exist is {"folders": []}. Free it. */
char *omegacat_inventory_load(const char *path);

/* Imports map_path into folder (NULL or "": the map file's name, or its
 * directory's when the file is map.json). A file that is not a map is refused.
 *   {"folder","created","added","skipped","refreshed","renamed":[...],
 *    "rejected":[...],"message"}
 * message is a sentence for the person. Free it. */
char *omegacat_inventory_import_map(const char *path, const char *map_path, const char *folder);

/* Applies a JSON array of edits in order and saves once; if any edit fails,
 * nothing is written and the error names the edit by position.
 *
 *   {"op":"save", "folder", "key", "session":{"name","host","port",
 *                 "platform","legacy","credential"}}
 *        key "" adds a device to folder; otherwise edits the session with that
 *        key. Only those six fields are written; device_type, username and the
 *        rest are kept.
 *   {"op":"delete", "keys":[...]}
 *   {"op":"move", "keys":[...], "to"}                 to an existing folder
 *   {"op":"patch", "keys":[...], "platform"?, "legacy"?, "credential"?}
 *        absent members are left unchanged
 *   {"op":"add_folder", "name"}
 *   {"op":"rename_folder", "folder", "to"}
 *   {"op":"remove_folder", "folder"}
 *
 * Refused: an address another session already has (keys stay unique), a
 * platform not in omegacat_inventory_platforms(), a port outside 0-65535.
 *
 * Returns {"keys":{"<original key>":"<new key>" or "" when deleted},
 *          "added":[keys]} -- every key the edits changed, for a view that
 * holds ticks. Free it. */
char *omegacat_inventory_apply(const char *path, const char *edits_json);

/* The platform names a device can be set to, as a JSON array. Free it. */
char *omegacat_inventory_platforms(void);

/* Removes a folder and its sessions. 0, or -1. */
int omegacat_inventory_remove_folder(const char *path, const char *folder);

#ifdef __cplusplus
}
#endif

#endif
