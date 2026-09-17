// app/inventory.h
//
// The device inventory (omegacat/inventory.h) as Qt values: load it, import
// an omegamaps map.json into it, remove a folder. Stateless -- every call names
// the file -- because the inventory is a small file rewritten atomically, not a
// handle anything holds open.
#pragma once

#include <QHash>
#include <QJsonArray>
#include <QString>
#include <QStringList>
#include <QVector>

namespace omegacat {

struct InventorySession {
    QString key;  // transport:host:port -- what a capture's session_keys selects
    QString name;
    QString host;
    int port = 0;
    QString transport;
    QString platform;    // set by a person: capture uses it
    QString deviceType;  // an import's guess: capture only compares it
    bool legacy = false;
    QString credential;
};

struct InventoryFolder {
    QString name;
    QVector<InventorySession> sessions;
};

struct InventoryImport {
    QString folder;
    bool created = false;
    int added = 0;
    int skipped = 0;
    int refreshed = 0;
    QStringList renamed;
    QStringList rejected;
    QString message;
};

struct InventoryApply {
    QHash<QString, QString> keys;  // original key -> new key, "" when deleted
    QStringList added;
};

namespace inventory {

// ~/.omegacat/inventory.yaml.
QString defaultPath();

// A missing file is an empty inventory, not an error.
bool load(const QString &path, QVector<InventoryFolder> *out, QString *err);

// folder empty: the map's file name, or its directory's for map.json.
bool importMap(const QString &path, const QString &mapPath, const QString &folder, InventoryImport *out,
               QString *err);

bool removeFolder(const QString &path, const QString &folder, QString *err);

// Applies edits (see omegacat_inventory_apply) in one write, or none.
bool apply(const QString &path, const QJsonArray &edits, InventoryApply *out, QString *err);

// Platform names a device can be set to.
QStringList platforms();

// Composes a later key map onto an earlier one, so original keys keep mapping
// to where they ended up across several applies.
void composeKeys(QHash<QString, QString> *into, const QHash<QString, QString> &later);

}  // namespace inventory
}  // namespace omegacat
