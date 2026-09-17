// app/searchview.h
//
// The Search view: a literal query across the current capture of every
// (device, type) in the store, the hits, and the hit in its file.
#ifndef OMEGACAT_APP_SEARCHVIEW_H
#define OMEGACAT_APP_SEARCHVIEW_H

#include <QWidget>

#include "store.h"

class QCheckBox;
class QComboBox;
class QLabel;
class QLineEdit;
class QPlainTextEdit;
class QPushButton;
class QTableWidget;

namespace omegacat {

class SearchView : public QWidget {
    Q_OBJECT
public:
    explicit SearchView(QWidget *parent = nullptr);

    void setStore(const Store *store);
    void runQuery(const QString &query);

    QTableWidget *hits() const { return m_hits; }
    QLabel *summary() const { return m_summary; }
    QPlainTextEdit *content() const { return m_content; }
    bool isRunning() const { return m_bridge->isRunning(); }
    QPushButton *exportButton() const { return m_export; }
    QPushButton *copyButton() const { return m_copy; }

    // Writes every hit of the last search to path as CSV: device, type,
    // file, line, text, and whether the text was cut short.
    bool exportHits(const QString &path, QString *err) const;

signals:
    void searchFinished();
    void openInStore(const QString &canonical, const QString &type, const QString &file, int line);

private:
    void start();
    void showHit();
    void finished(const SearchResult &r);

    const Store *m_store = nullptr;
    SearchBridge *m_bridge = nullptr;
    SearchResult m_result;
    int m_line = 0;

    QLineEdit *m_query = nullptr;
    QCheckBox *m_case = nullptr;
    QComboBox *m_types = nullptr;
    QPushButton *m_go = nullptr;
    QLabel *m_status = nullptr;
    QLabel *m_skips = nullptr;
    QLabel *m_summary = nullptr;
    QTableWidget *m_hits = nullptr;
    QLabel *m_hitTitle = nullptr;
    QPlainTextEdit *m_content = nullptr;
    QPushButton *m_open = nullptr;
    QPushButton *m_export = nullptr;
    QPushButton *m_copy = nullptr;
    QString m_lastQuery;
};

}  // namespace omegacat

#endif
