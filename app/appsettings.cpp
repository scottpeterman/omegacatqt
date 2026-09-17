// app/appsettings.cpp
#include "appsettings.h"

#include <QDir>
#include <QJsonDocument>
#include <QJsonObject>
#include <QSettings>

#include "runbridge.h"

namespace omegacat {

namespace appsettings {

namespace {
const QString kTheme = QStringLiteral("theme");
const QString kLastStore = QStringLiteral("store");  // the key MainWindow has always written
const QString kDefaultStore = QStringLiteral("launch/store");
const QString kTypes = QStringLiteral("capture/types");
const QString kHostKeys = QStringLiteral("capture/host_keys");
const QString kLegacy = QStringLiteral("capture/legacy");
const QString kParse = QStringLiteral("capture/parse");
const QString kCredTags = QStringLiteral("capture/credential_tags");
}  // namespace

QString fileName() { return QSettings().fileName(); }

QString theme() { return QSettings().value(kTheme, QStringLiteral("light")).toString(); }
void setTheme(const QString &key) { QSettings().setValue(kTheme, key); }

QString defaultStore() { return QSettings().value(kDefaultStore).toString(); }
void setDefaultStore(const QString &path) {
    QSettings s;
    if (path.trimmed().isEmpty())
        s.remove(kDefaultStore);
    else
        s.setValue(kDefaultStore, QDir::cleanPath(path.trimmed()));
}

QString lastStore() { return QSettings().value(kLastStore).toString(); }
void setLastStore(const QString &path) { QSettings().setValue(kLastStore, path); }

QString launchStore() {
    const QString d = defaultStore();
    if (!d.isEmpty()) return d;
    const QString last = lastStore();
    return last.isEmpty() ? QDir::home().filePath(QStringLiteral("captures")) : last;
}

CaptureDefaults builtinCaptureDefaults() {
    CaptureDefaults d;
    for (const CaptureType &ct : RunBridge::types())
        if (ct.isDefault) d.types << ct.type;
    const QJsonObject lib = QJsonDocument::fromJson(RunBridge::defaults()).object();
    d.hostKeys = lib.value(QLatin1String("host_keys")).toString(QStringLiteral("strict"));
    d.legacy = false;
    d.parse = true;
    return d;
}

CaptureDefaults captureDefaults() {
    const QSettings s;
    CaptureDefaults d = builtinCaptureDefaults();
    if (s.contains(kTypes)) {
        // Only types the library still has: a saved list naming a type that
        // was removed or renamed keeps the rest rather than failing the form.
        QStringList known;
        for (const CaptureType &ct : RunBridge::types()) known << ct.type;
        QStringList saved;
        for (const QString &t : s.value(kTypes).toStringList())
            if (known.contains(t)) saved << t;
        if (!saved.isEmpty()) d.types = saved;
    }
    const QString hk = s.value(kHostKeys).toString();
    if (hk == QLatin1String("strict") || hk == QLatin1String("tofu")) d.hostKeys = hk;
    d.legacy = s.value(kLegacy, d.legacy).toBool();
    d.parse = s.value(kParse, d.parse).toBool();
    d.credentialTags = s.value(kCredTags, d.credentialTags).toString();
    return d;
}

void setCaptureDefaults(const CaptureDefaults &d) {
    QSettings s;
    s.setValue(kTypes, d.types);
    s.setValue(kHostKeys, d.hostKeys);
    s.setValue(kLegacy, d.legacy);
    s.setValue(kParse, d.parse);
    s.setValue(kCredTags, d.credentialTags.trimmed());
}

}  // namespace appsettings

}  // namespace omegacat
