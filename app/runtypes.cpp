// app/runtypes.cpp
#include "runtypes.h"

#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>

namespace omegacat {

namespace {

QJsonObject object(const QByteArray &json, bool *ok) {
    QJsonParseError err;
    const QJsonDocument doc = QJsonDocument::fromJson(json, &err);
    *ok = err.error == QJsonParseError::NoError && doc.isObject();
    return doc.object();
}

QString str(const QJsonObject &o, const char *k) { return o.value(QLatin1String(k)).toString(); }
qint64 num(const QJsonObject &o, const char *k) { return qint64(o.value(QLatin1String(k)).toDouble()); }

QStringList strings(const QJsonValue &v) {
    QStringList out;
    for (const QJsonValue &e : v.toArray()) out.append(e.toString());
    return out;
}

RunRow row(const QJsonObject &o) {
    RunRow r;
    r.seq = quint64(num(o, "seq"));
    r.identity = str(o, "identity");
    r.name = str(o, "name");
    r.display = str(o, "display");
    r.type = str(o, "type");
    r.platform = str(o, "platform");
    r.state = str(o, "state");
    r.command = str(o, "command");
    r.bytes = num(o, "bytes");
    r.sha256 = str(o, "sha256");
    r.path = str(o, "path");
    r.file = str(o, "file");
    r.detail = str(o, "detail");
    r.durationMs = num(o, "duration_ms");
    r.parseStatus = str(o, "parse_status");
    r.templateName = str(o, "template");
    r.parseScore = o.value(QLatin1String("parse_score")).toDouble();
    r.parseRecords = int(num(o, "parse_records"));
    return r;
}

}  // namespace

bool parseProgress(const QByteArray &json, RunProgress *out) {
    bool ok = false;
    const QJsonObject o = object(json, &ok);
    if (!ok) return false;
    RunProgress p;
    p.seq = quint64(num(o, "seq"));
    p.elapsedMs = num(o, "elapsed_ms");
    p.finished = o.value(QLatin1String("finished")).toBool();
    p.total = int(num(o, "total"));
    p.settled = int(num(o, "settled"));
    const QJsonObject c = o.value(QLatin1String("counts")).toObject();
    p.counts.devices = int(num(c, "devices"));
    p.counts.devicesFailed = int(num(c, "devices_failed"));
    p.counts.stored = int(num(c, "stored"));
    p.counts.unchanged = int(num(c, "unchanged"));
    p.counts.notApplicable = int(num(c, "not_applicable"));
    p.counts.failed = int(num(c, "failed"));
    p.counts.running = int(num(c, "running"));
    p.counts.bytesStored = num(c, "bytes_stored");
    p.counts.newHostKeys = int(num(c, "new_host_keys"));
    p.counts.credRejections = int(num(c, "cred_rejections"));
    for (const QJsonValue &v : o.value(QLatin1String("running")).toArray()) p.running.append(row(v.toObject()));
    *out = p;
    return true;
}

bool parseRows(const QByteArray &json, quint64 *nextSeq, QVector<RunRow> *out) {
    bool ok = false;
    const QJsonObject o = object(json, &ok);
    if (!ok) return false;
    *nextSeq = quint64(num(o, "seq"));
    for (const QJsonValue &v : o.value(QLatin1String("rows")).toArray()) out->append(row(v.toObject()));
    return true;
}

bool parseDecisions(const QByteArray &json, quint64 *nextSeq, QVector<RunDecision> *out) {
    bool ok = false;
    const QJsonObject o = object(json, &ok);
    if (!ok) return false;
    *nextSeq = quint64(num(o, "seq"));
    for (const QJsonValue &v : o.value(QLatin1String("decisions")).toArray()) {
        const QJsonObject d = v.toObject();
        RunDecision rd;
        rd.seq = quint64(num(d, "seq"));
        rd.atMs = num(d, "at_ms");
        rd.kind = str(d, "kind");
        rd.identity = str(d, "identity");
        rd.type = str(d, "type");
        rd.name = str(d, "name");
        rd.detail = str(d, "detail");
        rd.text = str(d, "text");
        out->append(rd);
    }
    return true;
}

bool parseResult(const QByteArray &json, RunResult *out) {
    bool ok = false;
    const QJsonObject o = object(json, &ok);
    if (!ok) return false;
    out->kind = str(o, "kind");
    out->state = str(o, "state");
    out->error = str(o, "error");
    out->storePath = str(o, "store_path");
    out->logPath = str(o, "log_path");
    out->devices = int(num(o, "devices"));
    out->types = strings(o.value(QLatin1String("types")));
    out->skipped = strings(o.value(QLatin1String("skipped")));
    return true;
}

QVector<CaptureType> parseTypes(const QByteArray &json) {
    QVector<CaptureType> out;
    for (const QJsonValue &v : QJsonDocument::fromJson(json).array()) {
        const QJsonObject o = v.toObject();
        CaptureType t;
        t.type = str(o, "type");
        t.description = str(o, "description");
        t.keep = int(num(o, "keep"));
        t.isDefault = o.value(QLatin1String("default")).toBool();
        t.platforms = strings(o.value(QLatin1String("platforms")));
        out.append(t);
    }
    return out;
}

QString humanBytes(qint64 n) {
    if (n < 1024) return QStringLiteral("%1 B").arg(n);
    if (n < 1024 * 1024) return QStringLiteral("%1 KB").arg(double(n) / 1024, 0, 'f', 1);
    return QStringLiteral("%1 MB").arg(double(n) / (1024 * 1024), 0, 'f', 1);
}

QString humanDuration(qint64 ms) {
    if (ms < 1000) return QStringLiteral("%1 ms").arg(ms);
    if (ms < 60000) return QStringLiteral("%1 s").arg(double(ms) / 1000, 0, 'f', 1);
    return QStringLiteral("%1m %2s").arg(ms / 60000).arg((ms / 1000) % 60);
}

QString stateTone(const QString &state) {
    if (state == QLatin1String("stored")) return QStringLiteral("success");
    if (state == QLatin1String("unchanged")) return QStringLiteral("secondary");
    if (state == QLatin1String("not applicable")) return QStringLiteral("warning");
    if (state == QLatin1String("failed")) return QStringLiteral("danger");
    return QStringLiteral("accent");
}

}  // namespace omegacat
