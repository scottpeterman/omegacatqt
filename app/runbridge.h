// app/runbridge.h
//
// A run handle as Qt signals. The notifier's readable end is watched with a
// QSocketNotifier; on each wake the bridge drains it and pulls, in the order
// omegacat.h requires: progress first, then rows and decisions since the last
// sequence number. Views never touch the C surface for a run.
//
// A timer also pulls every half second while a run is unfinished: a running
// pair's duration moves without any event, and nothing else would redraw it.
#ifndef OMEGACAT_APP_RUNBRIDGE_H
#define OMEGACAT_APP_RUNBRIDGE_H

#include <QObject>
#include <QTimer>

#include "runtypes.h"

class QSocketNotifier;

namespace omegacat {

class RunBridge : public QObject {
    Q_OBJECT
public:
    explicit RunBridge(QObject *parent = nullptr);
    ~RunBridge() override;

    bool openDemo(int stepMs, QString *err);
    bool openCapture(const QByteArray &request, long long vault, QString *err);

    // [{field, message}] for a request, without running it.
    static QVector<QPair<QString, QString>> validate(const QByteArray &request);
    static QByteArray defaults();
    static QVector<CaptureType> types();
    static QString libraryVersion();

    RunResult result() const;
    void cancel();
    void close();

    bool isOpen() const { return m_handle > 0; }
    bool isFinished() const { return m_finished; }
    const RunProgress &progress() const { return m_progress; }

signals:
    void started();
    void progressChanged(const omegacat::RunProgress &p);
    void rowsChanged(const QVector<omegacat::RunRow> &rows);
    void decisionsAdded(const QVector<omegacat::RunDecision> &decisions);
    void finished(const omegacat::RunProgress &p);

private:
    bool adopt(long long h, QString *err);
    void pull();

    long long m_handle = -1;
    QSocketNotifier *m_notifier = nullptr;
    QTimer m_tick;
    quint64 m_rowSeq = 0;
    quint64 m_decisionSeq = 0;
    bool m_finished = false;
    RunProgress m_progress;
};

// Takes ownership of a string the library returned.
QByteArray takeString(char *raw);
QString libraryLastError();
void drainNotifier(long long handle);

}  // namespace omegacat

#endif
