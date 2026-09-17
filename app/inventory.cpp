// app/inventory.cpp
#include "inventory.h"

#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>

#include <omegacat/inventory.h>

#include "runbridge.h"

namespace omegacat {
namespace inventory {

namespace {

QStringList strings(const QJsonValue &v) {
    QStringList out;
    for (const QJsonValue &e : v.toArray()) out.append(e.toString());
    return out;
}

}  // namespace

QString defaultPath() {
    return QString::fromUtf8(takeString(omegacat_inventory_default_path()));
}

bool load(const QString &path, QVector<InventoryFolder> *out, QString *err) {
    const QByteArray p = path.toUtf8();
    char *raw = omegacat_inventory_load(p.constData());
    if (!raw) {
        if (err) *err = libraryLastError();
        return false;
    }
    const QJsonObject o = QJsonDocument::fromJson(takeString(raw)).object();
    out->clear();
    for (const QJsonValue &fv : o.value(QLatin1String("folders")).toArray()) {
        const QJsonObject f = fv.toObject();
        InventoryFolder folder;
        folder.name = f.value(QLatin1String("name")).toString();
        for (const QJsonValue &sv : f.value(QLatin1String("sessions")).toArray()) {
            const QJsonObject s = sv.toObject();
            folder.sessions.append({s.value(QLatin1String("key")).toString(),
                                    s.value(QLatin1String("name")).toString(),
                                    s.value(QLatin1String("host")).toString(),
                                    s.value(QLatin1String("port")).toInt(),
                                    s.value(QLatin1String("transport")).toString(),
                                    s.value(QLatin1String("platform")).toString(),
                                    s.value(QLatin1String("device_type")).toString(),
                                    s.value(QLatin1String("legacy")).toBool(),
                                    s.value(QLatin1String("credential")).toString()});
        }
        out->append(folder);
    }
    return true;
}

bool importMap(const QString &path, const QString &mapPath, const QString &folder, InventoryImport *out,
               QString *err) {
    const QByteArray p = path.toUtf8(), m = mapPath.toUtf8(), f = folder.toUtf8();
    char *raw = omegacat_inventory_import_map(p.constData(), m.constData(), f.constData());
    if (!raw) {
        if (err) *err = libraryLastError();
        return false;
    }
    const QJsonObject o = QJsonDocument::fromJson(takeString(raw)).object();
    out->folder = o.value(QLatin1String("folder")).toString();
    out->created = o.value(QLatin1String("created")).toBool();
    out->added = o.value(QLatin1String("added")).toInt();
    out->skipped = o.value(QLatin1String("skipped")).toInt();
    out->refreshed = o.value(QLatin1String("refreshed")).toInt();
    out->renamed = strings(o.value(QLatin1String("renamed")));
    out->rejected = strings(o.value(QLatin1String("rejected")));
    out->message = o.value(QLatin1String("message")).toString();
    return true;
}

bool removeFolder(const QString &path, const QString &folder, QString *err) {
    const QByteArray p = path.toUtf8(), f = folder.toUtf8();
    if (omegacat_inventory_remove_folder(p.constData(), f.constData()) != 0) {
        if (err) *err = libraryLastError();
        return false;
    }
    return true;
}

bool apply(const QString &path, const QJsonArray &edits, InventoryApply *out, QString *err) {
    const QByteArray p = path.toUtf8(), e = QJsonDocument(edits).toJson(QJsonDocument::Compact);
    char *raw = omegacat_inventory_apply(p.constData(), e.constData());
    if (!raw) {
        if (err) *err = libraryLastError();
        return false;
    }
    const QJsonObject o = QJsonDocument::fromJson(takeString(raw)).object();
    out->keys.clear();
    const QJsonObject keys = o.value(QLatin1String("keys")).toObject();
    for (auto it = keys.constBegin(); it != keys.constEnd(); ++it) out->keys.insert(it.key(), it.value().toString());
    out->added = strings(o.value(QLatin1String("added")));
    return true;
}

QStringList platforms() {
    char *raw = omegacat_inventory_platforms();
    QStringList out;
    for (const QJsonValue &v : QJsonDocument::fromJson(takeString(raw)).array()) out.append(v.toString());
    return out;
}

void composeKeys(QHash<QString, QString> *into, const QHash<QString, QString> &later) {
    QHash<QString, QString> pending = later;
    for (auto it = into->begin(); it != into->end(); ++it) {
        if (!it.value().isEmpty() && pending.contains(it.value())) it.value() = pending.take(it.value());
    }
    for (auto it = pending.constBegin(); it != pending.constEnd(); ++it) into->insert(it.key(), it.value());
}

}  // namespace inventory
}  // namespace omegacat
