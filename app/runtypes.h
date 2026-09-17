// app/runtypes.h
//
// The run surface's JSON (include/omegacat/omegacat.h) as C++ values. Parsing
// lives here and nowhere else, so a field added on the Go side is one edit.
#ifndef OMEGACAT_APP_RUNTYPES_H
#define OMEGACAT_APP_RUNTYPES_H

#include <QMetaType>
#include <QString>
#include <QStringList>
#include <QVector>

namespace omegacat {

// One (device, capture type) pair.
struct RunRow {
    quint64 seq = 0;
    QString identity;
    QString name;
    QString display;
    QString type;
    QString platform;
    QString state;  // running, stored, unchanged, "not applicable", failed
    QString command;
    qint64 bytes = 0;
    QString sha256;
    QString path;
    QString file;
    QString detail;
    qint64 durationMs = 0;

    QString parseStatus;  // "", "parsed", "no-match"
    QString templateName;
    double parseScore = 0;
    int parseRecords = 0;

    QString key() const { return identity + QLatin1Char('\n') + type; }
    bool openable() const { return !file.isEmpty() && !name.isEmpty(); }
};

struct RunCounts {
    int devices = 0;
    int devicesFailed = 0;
    int stored = 0;
    int unchanged = 0;
    int notApplicable = 0;
    int failed = 0;
    int running = 0;
    qint64 bytesStored = 0;
    int newHostKeys = 0;
    int credRejections = 0;
};

struct RunProgress {
    quint64 seq = 0;
    qint64 elapsedMs = 0;
    bool finished = false;
    int total = 0;
    int settled = 0;
    RunCounts counts;
    QVector<RunRow> running;
};

struct RunDecision {
    quint64 seq = 0;
    qint64 atMs = 0;
    QString kind;
    QString identity;
    QString type;
    QString name;
    QString detail;
    QString text;
};

struct RunResult {
    QString kind;   // "capture" or "demo"
    QString state;  // running, done, cancelled, failed
    QString error;
    QString storePath;
    QString logPath;
    int devices = 0;
    QStringList types;
    QStringList skipped;
};

// A capture type the library offers.
struct CaptureType {
    QString type;
    QString description;
    int keep = 0;
    bool isDefault = false;
    QStringList platforms;
};

bool parseProgress(const QByteArray &json, RunProgress *out);
bool parseRows(const QByteArray &json, quint64 *nextSeq, QVector<RunRow> *out);
bool parseDecisions(const QByteArray &json, quint64 *nextSeq, QVector<RunDecision> *out);
bool parseResult(const QByteArray &json, RunResult *out);
QVector<CaptureType> parseTypes(const QByteArray &json);

// "1.2 KB", "340 ms": shared by every view that shows sizes and durations.
QString humanBytes(qint64 n);
QString humanDuration(qint64 ms);

// The tone a state is drawn in: success, secondary, warning, danger, accent.
QString stateTone(const QString &state);

}  // namespace omegacat

Q_DECLARE_METATYPE(omegacat::RunRow)
Q_DECLARE_METATYPE(omegacat::RunProgress)
Q_DECLARE_METATYPE(omegacat::RunDecision)

#endif
