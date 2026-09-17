// app/templatebridge.h
//
// The Template Lab surface (include/omegacat/templates.h) for the view.
//
// test, platforms, list and get are quick and called on the GUI thread. sweep,
// save and delete can take seconds -- the first sweep on a database compiles
// every template, and a save re-reads them into the engine captures share --
// so they run on a worker thread and answer with a signal. Only one of those
// runs at a time; busy() says whether one is.
#ifndef OMEGACAT_APP_TEMPLATEBRIDGE_H
#define OMEGACAT_APP_TEMPLATEBRIDGE_H

#include <QJsonArray>
#include <QObject>
#include <QString>
#include <QStringList>
#include <QVector>

#include <functional>

namespace omegacat {

struct LabPlatform {
    QString platform;
    int count = 0;
};

struct LabItem {
    qint64 rowid = 0;
    qint64 id = -1;  // -1: the row has no id
    QString name;
    QString source;
};

struct LabTemplate {
    LabItem item;
    QString content;
    QString sample;
};

struct LabBreakdown {
    double records = 0, fields = 0, population = 0, consistency = 0, total = 0;
};

// omegacat_templates_test's result.
struct LabTest {
    bool compiled = false;
    bool success = false;
    QString error;
    QString errorType;  // "compile" or "runtime"
    int ruleLine = 0;
    QString inputLine;
    QStringList header;
    QJsonArray records;
    int recordCount = 0;
    int fieldCount = 0;
    double score = 0;
    LabBreakdown breakdown;
};

struct LabParse {
    bool success = false;
    QString templateName;
    double score = 0;
    int recordCount = 0;
    QStringList header;
    QJsonArray records;
    QString error;
    QString filter;
    int tried = 0;
};

struct LabCandidate {
    qint64 rowid = 0;
    QString name;
    QString compileError;
};

struct LabSweep {
    bool ok = false;  // false: the call itself failed, error says why
    QString error;
    LabParse primary;
    bool hasFallback = false;
    LabParse fallback;
    QVector<LabCandidate> candidates;
};

struct LabSaved {
    bool ok = false;
    QString error;
    qint64 rowid = 0;
    qint64 id = 0;
    QString name;
    QString reloadError;
};

class TemplateBridge : public QObject {
    Q_OBJECT
public:
    explicit TemplateBridge(QObject *parent = nullptr);
    ~TemplateBridge() override;

    // "" for the default database (created from the shipped copy on first
    // use). Resolved to a real path by path().
    void setDatabasePath(const QString &path) { m_path = path; }
    QString path() const;

    static LabTest test(const QString &raw, const QString &content, const QString &command, bool clean);
    bool platforms(QVector<LabPlatform> *out, QString *err) const;
    bool list(const QString &platform, const QString &query, QVector<LabItem> *out, QString *err) const;
    bool get(qint64 rowid, LabTemplate *out, QString *err) const;

    // Asynchronous; false when another one is running.
    bool sweep(const QString &raw, const QString &platform, const QString &command);
    bool save(qint64 rowid, const QString &name, const QString &content);
    bool remove(qint64 rowid);
    bool busy() const { return m_busy; }

signals:
    void sweepFinished(const omegacat::LabSweep &result);
    void saveFinished(const omegacat::LabSaved &result);
    void removeFinished(qint64 rowid, bool ok, const QString &error);

private:
    void runAsync(std::function<void()> work);

    QString m_path;
    mutable QString m_resolved;
    bool m_busy = false;
};

}  // namespace omegacat

#endif
