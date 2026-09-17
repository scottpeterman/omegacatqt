// app/store.h
//
// The store surface (include/omegacat/store.h) and search (search.h) for the
// views. Store calls are synchronous local reads; a search is a handle with
// its own notifier, as a run is.
#ifndef OMEGACAT_APP_STORE_H
#define OMEGACAT_APP_STORE_H

#include <QDateTime>
#include <QJsonArray>
#include <QObject>
#include <QString>
#include <QStringList>
#include <QVector>

class QSocketNotifier;

namespace omegacat {

struct StoreDevice {
    QString canonical;
    QStringList aliases;
    QString platform;
    QDateTime firstSeen;
    QDateTime lastSeen;
};

struct StoreType {
    QString type;
    int attempts = 0;
    int stored = 0;
    QDateTime last;
    qint64 bytes = 0;
    QString sha256;
    QString file;
};

struct StoreAttempt {
    QDateTime at;
    QString command;
    QString sha256;
    qint64 bytes = 0;
    QString file;
    bool unchanged = false;
};

// A line diff of two stored versions (omegacat_store_diff).
struct StoreDiffLine {
    QChar op;  // '=', '-', '+'
    int a = 0, b = 0;
    QString text;
    bool ignored = false;  // matched an ignore rule: shown dimmed, not counted
};
struct StoreDiffHunk {
    int aStart = 0, aLen = 0, bStart = 0, bLen = 0;
    QVector<StoreDiffLine> lines;
};
struct StoreDiff {
    QString from, to;
    bool identical = false;
    int added = 0, removed = 0, ignored = 0;
    QString platform, rulesPath, rulesWarning;
    QVector<StoreDiffHunk> hunks;
};

// A .parsed.json. valid is false when the file has none.
struct StoreParsed {
    bool valid = false;
    QString status;  // "parsed" or "no-match"
    QString templateName;
    double score = 0;
    QString hint;
    QStringList header;
    QJsonArray records;  // objects; a value is a string or an array of strings
    QString rawFile;
    QString rawSha256;
};

class Store {
public:
    Store() = default;
    ~Store();
    Store(const Store &) = delete;
    Store &operator=(const Store &) = delete;

    bool open(const QString &root, QString *err);
    void close();
    bool isOpen() const { return m_handle > 0; }
    long long handle() const { return m_handle; }
    QString root() const { return m_root; }

    bool devices(QVector<StoreDevice> *out, QStringList *unreadable, QString *err) const;
    bool types(const QString &canonical, QVector<StoreType> *out, QString *err) const;
    bool history(const QString &canonical, const QString &type, QVector<StoreAttempt> *out, QString *err) const;
    bool read(const QString &canonical, const QString &type, const QString &file, QByteArray *out, QString *err) const;
    bool parsed(const QString &canonical, const QString &type, const QString &file, StoreParsed *out, QString *err) const;
    bool diff(const QString &canonical, const QString &type, const QString &olderFile, const QString &newerFile,
              StoreDiff *out, QString *err) const;
    static bool diffable(const QString &type);
    // The diff ignore rules file; a non-empty path replaces it (the probe's
    // way of keeping out of ~/.omegacat).
    static QString diffIgnorePath(const QString &set = {});

private:
    long long m_handle = -1;
    QString m_root;
};

struct SearchHit {
    QString device;
    QString type;
    QString file;
    int line = 0;
    QString text;
    bool truncated = false;
};

struct SearchResult {
    QString state;
    QString error;
    QVector<SearchHit> hits;
    bool capped = false;
    int limit = 0;
    int devices = 0;
    int artifacts = 0;
    QStringList skips;
    QString warning;
    QString summary;
};

class SearchBridge : public QObject {
    Q_OBJECT
public:
    explicit SearchBridge(QObject *parent = nullptr);
    ~SearchBridge() override;

    bool start(const Store &store, const QString &query, bool caseSensitive,
               const QStringList &types, QString *err);
    void cancel();
    bool isRunning() const { return m_handle > 0 && !m_done; }

signals:
    void progress(int done, int total);
    void finished(const omegacat::SearchResult &result);

private:
    void pull();
    void close();

    long long m_handle = -1;
    QSocketNotifier *m_notifier = nullptr;
    bool m_done = false;
};

}  // namespace omegacat

#endif
