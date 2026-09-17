// app/templatebridge.cpp
#include "templatebridge.h"

#include <QCoreApplication>
#include <QJsonDocument>
#include <QJsonObject>
#include <QPointer>
#include <thread>

#include <omegacat/templates.h>

#include "runbridge.h"

namespace omegacat {

namespace {

QStringList strings(const QJsonValue &v) {
    QStringList out;
    for (const QJsonValue &e : v.toArray()) out.append(e.toString());
    return out;
}

LabParse parseFrom(const QJsonObject &o) {
    LabParse p;
    p.success = o.value(QLatin1String("success")).toBool();
    p.templateName = o.value(QLatin1String("template")).toString();
    p.score = o.value(QLatin1String("score")).toDouble();
    p.recordCount = o.value(QLatin1String("record_count")).toInt();
    p.header = strings(o.value(QLatin1String("header")));
    p.records = o.value(QLatin1String("records")).toArray();
    p.error = o.value(QLatin1String("error")).toString();
    p.filter = o.value(QLatin1String("filter")).toString();
    p.tried = o.value(QLatin1String("tried")).toInt();
    return p;
}

LabItem itemFrom(const QJsonObject &o) {
    LabItem it;
    it.rowid = qint64(o.value(QLatin1String("rowid")).toDouble());
    const QJsonValue id = o.value(QLatin1String("id"));
    it.id = id.isDouble() ? qint64(id.toDouble()) : -1;
    it.name = o.value(QLatin1String("cli_command")).toString();
    it.source = o.value(QLatin1String("source")).toString();
    return it;
}

QByteArray utf8(const QString &s) { return s.toUtf8(); }

}  // namespace

TemplateBridge::TemplateBridge(QObject *parent) : QObject(parent) {
    qRegisterMetaType<omegacat::LabSweep>("omegacat::LabSweep");
    qRegisterMetaType<omegacat::LabSaved>("omegacat::LabSaved");
}

TemplateBridge::~TemplateBridge() = default;

QString TemplateBridge::path() const {
    if (!m_path.isEmpty()) return m_path;
    // The default is resolved once: resolving copies the shipped database into
    // ~/.omegacat on first use, which is not worth repeating per call.
    if (m_resolved.isEmpty()) {
        if (char *raw = omegacat_templates_default_path()) m_resolved = QString::fromUtf8(takeString(raw));
    }
    return m_resolved;
}

LabTest TemplateBridge::test(const QString &raw, const QString &content, const QString &command, bool clean) {
    QJsonObject req{{QStringLiteral("raw_output"), raw},
                    {QStringLiteral("textfsm_content"), content},
                    {QStringLiteral("command"), command},
                    {QStringLiteral("clean"), clean}};
    LabTest t;
    char *out = omegacat_templates_test(QJsonDocument(req).toJson(QJsonDocument::Compact).constData());
    if (!out) {
        t.error = libraryLastError();
        t.errorType = QStringLiteral("compile");
        return t;
    }
    const QJsonObject o = QJsonDocument::fromJson(takeString(out)).object();
    t.compiled = o.value(QLatin1String("compiled")).toBool();
    t.success = o.value(QLatin1String("success")).toBool();
    t.error = o.value(QLatin1String("error")).toString();
    t.errorType = o.value(QLatin1String("error_type")).toString();
    t.ruleLine = o.value(QLatin1String("rule_line")).toInt();
    t.inputLine = o.value(QLatin1String("input_line")).toString();
    t.header = strings(o.value(QLatin1String("header")));
    t.records = o.value(QLatin1String("records")).toArray();
    t.recordCount = o.value(QLatin1String("record_count")).toInt();
    t.fieldCount = o.value(QLatin1String("field_count")).toInt();
    t.score = o.value(QLatin1String("score")).toDouble();
    const QJsonObject b = o.value(QLatin1String("breakdown")).toObject();
    t.breakdown = {b.value(QLatin1String("records")).toDouble(), b.value(QLatin1String("fields")).toDouble(),
                   b.value(QLatin1String("population")).toDouble(),
                   b.value(QLatin1String("consistency")).toDouble(), b.value(QLatin1String("total")).toDouble()};
    return t;
}

bool TemplateBridge::platforms(QVector<LabPlatform> *out, QString *err) const {
    out->clear();
    char *raw = omegacat_templates_platforms(utf8(path()).constData());
    if (!raw) {
        if (err) *err = libraryLastError();
        return false;
    }
    for (const QJsonValue &v : QJsonDocument::fromJson(takeString(raw)).array()) {
        const QJsonObject o = v.toObject();
        out->append({o.value(QLatin1String("platform")).toString(), o.value(QLatin1String("count")).toInt()});
    }
    return true;
}

bool TemplateBridge::list(const QString &platform, const QString &query, QVector<LabItem> *out,
                          QString *err) const {
    out->clear();
    char *raw = omegacat_templates_list(utf8(path()).constData(), utf8(platform).constData(),
                                        utf8(query).constData());
    if (!raw) {
        if (err) *err = libraryLastError();
        return false;
    }
    for (const QJsonValue &v : QJsonDocument::fromJson(takeString(raw)).array()) out->append(itemFrom(v.toObject()));
    return true;
}

bool TemplateBridge::get(qint64 rowid, LabTemplate *out, QString *err) const {
    char *raw = omegacat_templates_get(utf8(path()).constData(), rowid);
    if (!raw) {
        if (err) *err = libraryLastError();
        return false;
    }
    const QJsonObject o = QJsonDocument::fromJson(takeString(raw)).object();
    out->item = itemFrom(o);
    out->content = o.value(QLatin1String("textfsm_content")).toString();
    out->sample = o.value(QLatin1String("cli_content")).toString();
    return true;
}

void TemplateBridge::runAsync(std::function<void()> work) {
    m_busy = true;
    std::thread(std::move(work)).detach();
}

bool TemplateBridge::sweep(const QString &raw, const QString &platform, const QString &command) {
    if (m_busy) return false;
    const QByteArray req = QJsonDocument(QJsonObject{{QStringLiteral("templates_path"), path()},
                                                     {QStringLiteral("raw_output"), raw},
                                                     {QStringLiteral("platform"), platform},
                                                     {QStringLiteral("command"), command}})
                               .toJson(QJsonDocument::Compact);
    QPointer<TemplateBridge> self(this);
    runAsync([self, req] {
        LabSweep s;
        if (char *out = omegacat_templates_sweep(req.constData())) {
            const QJsonObject o = QJsonDocument::fromJson(takeString(out)).object();
            s.ok = true;
            s.primary = parseFrom(o.value(QLatin1String("primary")).toObject());
            if (o.contains(QLatin1String("fallback"))) {
                s.hasFallback = true;
                s.fallback = parseFrom(o.value(QLatin1String("fallback")).toObject());
            }
            for (const QJsonValue &v : o.value(QLatin1String("candidates")).toArray()) {
                const QJsonObject c = v.toObject();
                s.candidates.append({qint64(c.value(QLatin1String("rowid")).toDouble()),
                                     c.value(QLatin1String("cli_command")).toString(),
                                     c.value(QLatin1String("compile_error")).toString()});
            }
        } else {
            s.error = libraryLastError();
        }
        // Back to the GUI thread through the application object, which
        // outlives the bridge; the pointer check covers a bridge deleted while
        // this ran.
        QMetaObject::invokeMethod(qApp, [self, s] {
            if (!self) return;
            self->m_busy = false;
            emit self->sweepFinished(s);
        }, Qt::QueuedConnection);
    });
    return true;
}

bool TemplateBridge::save(qint64 rowid, const QString &name, const QString &content) {
    if (m_busy) return false;
    QJsonObject o{{QStringLiteral("templates_path"), path()},
                  {QStringLiteral("cli_command"), name},
                  {QStringLiteral("textfsm_content"), content}};
    if (rowid > 0) o.insert(QStringLiteral("rowid"), double(rowid));
    const QByteArray req = QJsonDocument(o).toJson(QJsonDocument::Compact);
    QPointer<TemplateBridge> self(this);
    runAsync([self, req, name] {
        LabSaved r;
        r.name = name;
        if (char *out = omegacat_templates_save(req.constData())) {
            const QJsonObject o = QJsonDocument::fromJson(takeString(out)).object();
            r.ok = true;
            r.rowid = qint64(o.value(QLatin1String("rowid")).toDouble());
            r.id = qint64(o.value(QLatin1String("id")).toDouble());
            r.reloadError = o.value(QLatin1String("reload_error")).toString();
        } else {
            r.error = libraryLastError();
        }
        QMetaObject::invokeMethod(qApp, [self, r] {
            if (!self) return;
            self->m_busy = false;
            emit self->saveFinished(r);
        }, Qt::QueuedConnection);
    });
    return true;
}

bool TemplateBridge::remove(qint64 rowid) {
    if (m_busy) return false;
    const QByteArray db = utf8(path());
    QPointer<TemplateBridge> self(this);
    runAsync([self, db, rowid] {
        const int rc = omegacat_templates_delete(db.constData(), rowid);
        const QString err = rc == 0 ? QString() : libraryLastError();
        QMetaObject::invokeMethod(qApp, [self, rowid, rc, err] {
            if (!self) return;
            self->m_busy = false;
            emit self->removeFinished(rowid, rc >= 0, err);
        }, Qt::QueuedConnection);
    });
    return true;
}

}  // namespace omegacat
