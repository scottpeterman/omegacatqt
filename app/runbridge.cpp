// app/runbridge.cpp
#include "runbridge.h"

#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QSocketNotifier>

#include <omegacat/omegacat.h>

#ifdef _WIN32
#include <winsock2.h>
#else
#include <unistd.h>
#endif

namespace omegacat {

QByteArray takeString(char *raw) {
    QByteArray b(raw ? raw : "");
    omegacat_free(raw);
    return b;
}

QString libraryLastError() { return QString::fromUtf8(takeString(omegacat_last_error())); }

void drainNotifier(long long handle) {
    if (handle < 0) return;
    char scratch[256];
#ifdef _WIN32
    while (::recv(static_cast<SOCKET>(handle), scratch, sizeof(scratch), 0) > 0) {
    }
#else
    while (::read(static_cast<int>(handle), scratch, sizeof(scratch)) > 0) {
    }
#endif
}

RunBridge::RunBridge(QObject *parent) : QObject(parent) {
    qRegisterMetaType<omegacat::RunProgress>();
    qRegisterMetaType<omegacat::RunRow>();
    qRegisterMetaType<omegacat::RunDecision>();
    m_tick.setInterval(500);
    connect(&m_tick, &QTimer::timeout, this, &RunBridge::pull);
}

RunBridge::~RunBridge() { close(); }

QString RunBridge::libraryVersion() { return QString::fromUtf8(takeString(omegacat_version())); }

QByteArray RunBridge::defaults() { return takeString(omegacat_capture_defaults()); }

QVector<CaptureType> RunBridge::types() { return parseTypes(takeString(omegacat_types())); }

QVector<QPair<QString, QString>> RunBridge::validate(const QByteArray &request) {
    QVector<QPair<QString, QString>> out;
    char *raw = omegacat_capture_validate(request.constData());
    if (!raw) {
        out.append({QStringLiteral("request"), libraryLastError()});
        return out;
    }
    for (const QJsonValue &v : QJsonDocument::fromJson(takeString(raw)).array()) {
        const QJsonObject o = v.toObject();
        out.append({o.value(QLatin1String("field")).toString(), o.value(QLatin1String("message")).toString()});
    }
    return out;
}

bool RunBridge::openDemo(int stepMs, QString *err) {
    close();
    const long long h = omegacat_demo_open(stepMs);
    if (h <= 0) {
        if (err) *err = libraryLastError();
        return false;
    }
    return adopt(h, err);
}

bool RunBridge::openCapture(const QByteArray &request, long long vault, QString *err) {
    close();
    const long long h = omegacat_capture_open(vault, request.constData());
    if (h <= 0) {
        if (err) *err = libraryLastError();
        return false;
    }
    return adopt(h, err);
}

RunResult RunBridge::result() const {
    RunResult r;
    if (m_handle > 0) parseResult(takeString(omegacat_run_result(m_handle)), &r);
    return r;
}

bool RunBridge::adopt(long long h, QString *err) {
    const long long fd = omegacat_notify_handle(h);
    if (fd < 0) {
        if (err) *err = libraryLastError();
        omegacat_close(h);
        return false;
    }
    m_handle = h;
    m_rowSeq = 0;
    m_decisionSeq = 0;
    m_finished = false;
    m_progress = RunProgress{};
    emit started();
    m_notifier = new QSocketNotifier(static_cast<qintptr>(fd), QSocketNotifier::Read, this);
    connect(m_notifier, &QSocketNotifier::activated, this, &RunBridge::pull);
    m_tick.start();
    // A run can finish before the event loop sees its first wake; pull once
    // regardless.
    QMetaObject::invokeMethod(this, &RunBridge::pull, Qt::QueuedConnection);
    return true;
}

void RunBridge::cancel() {
    if (m_handle > 0) omegacat_cancel(m_handle);
}

void RunBridge::close() {
    m_tick.stop();
    if (m_notifier) {
        m_notifier->setEnabled(false);
        delete m_notifier;
        m_notifier = nullptr;
    }
    if (m_handle > 0) omegacat_close(m_handle);
    m_handle = -1;
}

void RunBridge::pull() {
    const long long h = m_handle;
    if (h <= 0 || m_finished) return;
    drainNotifier(omegacat_notify_handle(h));

    RunProgress p;
    if (!parseProgress(takeString(omegacat_progress(h)), &p)) return;
    const auto stillOurs = [this, h] { return m_handle == h; };

    QVector<RunRow> rows;
    quint64 next = m_rowSeq;
    if (parseRows(takeString(omegacat_rows_since(h, m_rowSeq)), &next, &rows)) {
        m_rowSeq = next;
        if (!rows.isEmpty()) emit rowsChanged(rows);
        if (!stillOurs()) return;
    }

    QVector<RunDecision> decisions;
    next = m_decisionSeq;
    if (parseDecisions(takeString(omegacat_decisions_since(h, m_decisionSeq)), &next, &decisions)) {
        m_decisionSeq = next;
        if (!decisions.isEmpty()) emit decisionsAdded(decisions);
        if (!stillOurs()) return;
    }

    m_progress = p;
    emit progressChanged(p);
    if (!stillOurs()) return;

    if (p.finished) {
        m_finished = true;
        m_tick.stop();
        if (m_notifier) m_notifier->setEnabled(false);
        emit finished(p);
    }
}

}  // namespace omegacat
