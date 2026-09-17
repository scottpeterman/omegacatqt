// app/store.cpp
#include "store.h"

#include <QJsonDocument>
#include <QJsonObject>
#include <QSocketNotifier>

#include <omegacat/search.h>
#include <omegacat/store.h>

#include "runbridge.h"

namespace omegacat {

namespace {

QDateTime fromMs(const QJsonValue &v) {
    const qint64 ms = qint64(v.toDouble());
    return ms > 0 ? QDateTime::fromMSecsSinceEpoch(ms) : QDateTime();
}

QStringList strings(const QJsonValue &v) {
    QStringList out;
    for (const QJsonValue &e : v.toArray()) out.append(e.toString());
    return out;
}

bool failed(char *raw, QByteArray *out, QString *err) {
    if (!raw) {
        if (err) *err = libraryLastError();
        return true;
    }
    *out = takeString(raw);
    return false;
}

}  // namespace

Store::~Store() { close(); }

bool Store::open(const QString &root, QString *err) {
    close();
    const long long h = omegacat_store_open(root.toUtf8().constData());
    if (h <= 0) {
        if (err) *err = libraryLastError();
        return false;
    }
    m_handle = h;
    m_root = root;
    return true;
}

void Store::close() {
    if (m_handle > 0) omegacat_store_close(m_handle);
    m_handle = -1;
    m_root.clear();
}

bool Store::devices(QVector<StoreDevice> *out, QStringList *unreadable, QString *err) const {
    QByteArray json;
    if (failed(omegacat_store_devices(m_handle), &json, err)) return false;
    const QJsonObject o = QJsonDocument::fromJson(json).object();
    for (const QJsonValue &v : o.value(QLatin1String("devices")).toArray()) {
        const QJsonObject d = v.toObject();
        out->append({d.value(QLatin1String("canonical")).toString(), strings(d.value(QLatin1String("aliases"))),
                     d.value(QLatin1String("platform")).toString(), fromMs(d.value(QLatin1String("first_seen_ms"))),
                     fromMs(d.value(QLatin1String("last_seen_ms")))});
    }
    if (unreadable) *unreadable = strings(o.value(QLatin1String("unreadable")));
    return true;
}

bool Store::types(const QString &canonical, QVector<StoreType> *out, QString *err) const {
    QByteArray json;
    if (failed(omegacat_store_types(m_handle, canonical.toUtf8().constData()), &json, err)) return false;
    for (const QJsonValue &v : QJsonDocument::fromJson(json).array()) {
        const QJsonObject t = v.toObject();
        StoreType st;
        st.type = t.value(QLatin1String("type")).toString();
        st.attempts = t.value(QLatin1String("attempts")).toInt();
        st.stored = t.value(QLatin1String("stored")).toInt();
        st.last = fromMs(t.value(QLatin1String("last_ms")));
        st.bytes = qint64(t.value(QLatin1String("bytes")).toDouble());
        st.sha256 = t.value(QLatin1String("sha256")).toString();
        st.file = t.value(QLatin1String("file")).toString();
        out->append(st);
    }
    return true;
}

bool Store::history(const QString &canonical, const QString &type, QVector<StoreAttempt> *out, QString *err) const {
    QByteArray json;
    if (failed(omegacat_store_history(m_handle, canonical.toUtf8().constData(), type.toUtf8().constData()), &json, err))
        return false;
    for (const QJsonValue &v : QJsonDocument::fromJson(json).array()) {
        const QJsonObject h = v.toObject();
        out->append({fromMs(h.value(QLatin1String("at_ms"))), h.value(QLatin1String("command")).toString(),
                     h.value(QLatin1String("sha256")).toString(), qint64(h.value(QLatin1String("bytes")).toDouble()),
                     h.value(QLatin1String("file")).toString(), h.value(QLatin1String("unchanged")).toBool()});
    }
    return true;
}

bool Store::read(const QString &canonical, const QString &type, const QString &file, QByteArray *out, QString *err) const {
    long long n = 0;
    char *raw = omegacat_store_read(m_handle, canonical.toUtf8().constData(), type.toUtf8().constData(),
                                    file.toUtf8().constData(), &n);
    if (!raw) {
        if (err) *err = libraryLastError();
        return false;
    }
    *out = QByteArray(raw, int(n));  // the length is the answer, not strlen
    omegacat_free(raw);
    return true;
}

bool Store::parsed(const QString &canonical, const QString &type, const QString &file, StoreParsed *out, QString *err) const {
    QByteArray json;
    if (failed(omegacat_store_parsed(m_handle, canonical.toUtf8().constData(), type.toUtf8().constData(),
                                     file.toUtf8().constData()),
               &json, err))
        return false;
    *out = StoreParsed{};
    const QJsonDocument doc = QJsonDocument::fromJson(json);
    if (!doc.isObject()) return true;  // "null": no parse for this file
    const QJsonObject o = doc.object();
    out->valid = true;
    out->status = o.value(QLatin1String("status")).toString();
    out->templateName = o.value(QLatin1String("template")).toString();
    out->score = o.value(QLatin1String("score")).toDouble();
    out->hint = o.value(QLatin1String("hint")).toString();
    out->header = strings(o.value(QLatin1String("header")));
    out->records = o.value(QLatin1String("records")).toArray();
    out->rawFile = o.value(QLatin1String("raw_file")).toString();
    out->rawSha256 = o.value(QLatin1String("raw_sha256")).toString();
    return true;
}

// ---------------------------------------------------------------------------

SearchBridge::SearchBridge(QObject *parent) : QObject(parent) {}
SearchBridge::~SearchBridge() { close(); }

bool SearchBridge::start(const Store &store, const QString &query, bool caseSensitive,
                         const QStringList &types, QString *err) {
    close();
    QJsonObject req;
    req.insert(QStringLiteral("query"), query);
    req.insert(QStringLiteral("case_sensitive"), caseSensitive);
    req.insert(QStringLiteral("types"), QJsonArray::fromStringList(types));
    const long long h = omegacat_search_open(store.handle(), QJsonDocument(req).toJson(QJsonDocument::Compact).constData());
    if (h <= 0) {
        if (err) *err = libraryLastError();
        return false;
    }
    m_handle = h;
    m_done = false;
    const long long fd = omegacat_search_notify_handle(h);
    m_notifier = new QSocketNotifier(static_cast<qintptr>(fd), QSocketNotifier::Read, this);
    connect(m_notifier, &QSocketNotifier::activated, this, &SearchBridge::pull);
    QMetaObject::invokeMethod(this, &SearchBridge::pull, Qt::QueuedConnection);
    return true;
}

void SearchBridge::cancel() {
    if (m_handle > 0) omegacat_search_cancel(m_handle);
}

void SearchBridge::close() {
    if (m_notifier) {
        m_notifier->setEnabled(false);
        delete m_notifier;
        m_notifier = nullptr;
    }
    if (m_handle > 0) omegacat_search_close(m_handle);
    m_handle = -1;
}

void SearchBridge::pull() {
    if (m_handle <= 0 || m_done) return;
    drainNotifier(omegacat_search_notify_handle(m_handle));
    const QJsonObject p = QJsonDocument::fromJson(takeString(omegacat_search_progress(m_handle))).object();
    emit progress(p.value(QLatin1String("done")).toInt(), p.value(QLatin1String("total")).toInt());
    if (!p.value(QLatin1String("finished")).toBool()) return;

    m_done = true;
    if (m_notifier) m_notifier->setEnabled(false);
    const QJsonObject o = QJsonDocument::fromJson(takeString(omegacat_search_result(m_handle))).object();
    SearchResult r;
    r.state = o.value(QLatin1String("state")).toString();
    r.error = o.value(QLatin1String("error")).toString();
    r.capped = o.value(QLatin1String("capped")).toBool();
    r.limit = o.value(QLatin1String("limit")).toInt();
    r.devices = o.value(QLatin1String("devices")).toInt();
    r.artifacts = o.value(QLatin1String("artifacts")).toInt();
    r.warning = o.value(QLatin1String("warning")).toString();
    r.summary = o.value(QLatin1String("summary")).toString();
    for (const QJsonValue &v : o.value(QLatin1String("hits")).toArray()) {
        const QJsonObject h = v.toObject();
        r.hits.append({h.value(QLatin1String("device")).toString(), h.value(QLatin1String("type")).toString(),
                       h.value(QLatin1String("file")).toString(), h.value(QLatin1String("line")).toInt(),
                       h.value(QLatin1String("text")).toString(), h.value(QLatin1String("truncated")).toBool()});
    }
    for (const QJsonValue &v : o.value(QLatin1String("skips")).toArray()) {
        const QJsonObject s = v.toObject();
        QString where = s.value(QLatin1String("device")).toString();
        if (!s.value(QLatin1String("type")).toString().isEmpty())
            where += QStringLiteral(" / ") + s.value(QLatin1String("type")).toString();
        r.skips.append(where + QStringLiteral(": ") + s.value(QLatin1String("reason")).toString());
    }
    emit finished(r);
}

bool Store::diff(const QString &canonical, const QString &type, const QString &olderFile, const QString &newerFile,
                 StoreDiff *out, QString *err) const {
    QByteArray json;
    if (failed(omegacat_store_diff(m_handle, canonical.toUtf8().constData(), type.toUtf8().constData(),
                                   olderFile.toUtf8().constData(), newerFile.toUtf8().constData(), 3),
               &json, err))
        return false;
    const QJsonObject o = QJsonDocument::fromJson(json).object();
    *out = StoreDiff{};
    out->from = o.value(QLatin1String("from")).toString();
    out->to = o.value(QLatin1String("to")).toString();
    out->identical = o.value(QLatin1String("identical")).toBool();
    out->added = o.value(QLatin1String("added")).toInt();
    out->removed = o.value(QLatin1String("removed")).toInt();
    out->ignored = o.value(QLatin1String("ignored")).toInt();
    out->platform = o.value(QLatin1String("platform")).toString();
    out->rulesPath = o.value(QLatin1String("rules_path")).toString();
    out->rulesWarning = o.value(QLatin1String("rules_warning")).toString();
    for (const QJsonValue &hv : o.value(QLatin1String("hunks")).toArray()) {
        const QJsonObject ho = hv.toObject();
        StoreDiffHunk hunk;
        hunk.aStart = ho.value(QLatin1String("a_start")).toInt();
        hunk.aLen = ho.value(QLatin1String("a_len")).toInt();
        hunk.bStart = ho.value(QLatin1String("b_start")).toInt();
        hunk.bLen = ho.value(QLatin1String("b_len")).toInt();
        for (const QJsonValue &lv : ho.value(QLatin1String("lines")).toArray()) {
            const QJsonObject lo = lv.toObject();
            const QString op = lo.value(QLatin1String("op")).toString();
            hunk.lines.append({op.isEmpty() ? QLatin1Char('=') : op.at(0), lo.value(QLatin1String("a")).toInt(),
                               lo.value(QLatin1String("b")).toInt(), lo.value(QLatin1String("text")).toString(),
                               lo.value(QLatin1String("ignored")).toBool()});
        }
        out->hunks.append(hunk);
    }
    return true;
}

bool Store::diffable(const QString &type) { return omegacat_store_diffable(type.toUtf8().constData()) == 1; }

QString Store::diffIgnorePath(const QString &set) {
    const QByteArray p = set.toUtf8();
    return QString::fromUtf8(takeString(omegacat_diff_ignore_path(set.isEmpty() ? nullptr : p.constData())));
}

}  // namespace omegacat

