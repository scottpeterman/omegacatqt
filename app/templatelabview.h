// app/templatelabview.h
//
// The Template Lab, after netlapse's (web/static/js/views/templates.js): write
// or fix a TextFSM template against real output, see exactly why it fails or
// what it scores, check which template the database would pick, and save.
//
//   Author & Test     raw output and template side by side with the result.
//                     Test runs the editor's template alone and saves nothing;
//                     Test vs Database sweeps the database as a parse does.
//   Template Browser  filter, search, open into Author, delete.
//
// Output comes from a paste or from the store ("From store", or the Store
// view's Template Lab button), which also fills the platform and the command
// the capture ran. The database is ~/.omegacat/tfsm_templates.db unless set
// otherwise; a save reaches the engine captures use, so the next capture
// parses with it.
#ifndef OMEGACAT_APP_TEMPLATELABVIEW_H
#define OMEGACAT_APP_TEMPLATELABVIEW_H

#include <QWidget>

#include "templatebridge.h"

class QCheckBox;
class QComboBox;
class QFrame;
class QLabel;
class QLineEdit;
class QListWidget;
class QPlainTextEdit;
class QPushButton;
class QStackedWidget;
class QTableWidget;

namespace omegacat {

class LabBar;
class Store;

class TemplateLabView : public QWidget {
    Q_OBJECT
public:
    explicit TemplateLabView(QWidget *parent = nullptr);

    // "" is the default database. Takes effect at the next refresh().
    void setDatabasePath(const QString &path);
    void setStore(const Store *store) { m_store = store; }

    // Reads the platform list and, when it is showing, the browser. The main
    // window calls it the first time the view is shown, so the default
    // database is not created until someone opens the Lab.
    void refresh();
    bool refreshed() const { return m_refreshed; }

    enum class Page { Author, Browser };
    void showPage(Page page);

    // Puts a stored capture into the raw output, with its device's platform
    // and the command that produced it.
    bool loadFromStore(const QString &canonical, const QString &type, const QString &file, QString *err);
    // Opens a template from the database into the editor.
    bool loadTemplate(qint64 rowid, QString *err);
    void clearEditor();

    void test();
    void sweep();
    void save();
    void removeTemplate(qint64 rowid);

    // For the probe.
    TemplateBridge *bridge() const { return m_bridge; }
    QLineEdit *nameEdit() const { return m_name; }
    QComboBox *platformBox() const { return m_platform; }
    QLineEdit *commandEdit() const { return m_command; }
    QCheckBox *cleanBox() const { return m_clean; }
    QPlainTextEdit *rawEdit() const { return m_raw; }
    QPlainTextEdit *templateEdit() const { return m_template; }
    QFrame *matchBox() const { return m_match; }
    QLabel *matchCaption() const { return m_matchCaption; }
    QLabel *matchMeta() const { return m_matchMeta; }
    QLabel *matchDetail() const { return m_matchDetail; }
    QLabel *scorePill() const { return m_pill; }
    QLabel *editState() const { return m_editState; }
    QTableWidget *parsedTable() const { return m_parsed; }
    QListWidget *candidates() const { return m_candidates; }
    QTableWidget *browserTable() const { return m_browser; }
    QPushButton *sampleButton() const { return m_sample; }
    double barValue(int i) const;
    qint64 editingRowid() const { return m_editing; }
    void setPlatform(const QString &platform);

signals:
    void status(const QString &text);

private:
    QWidget *buildAuthor();
    QWidget *buildBrowser();
    void reloadBrowser();
    void showPlaceholder(const QString &text);
    void showMatch(const QString &state, const QString &caption, const QString &meta, const QString &detail);
    void showBars(bool on);
    void setParsed(const QStringList &header, const QJsonArray &records);
    void showCandidates(const QVector<LabCandidate> &cands, const QString &winner);
    void sweepFinished(const LabSweep &s);
    void saveFinished(const LabSaved &r);
    void markRawLine(const QString &text);
    void setEditing(qint64 rowid, qint64 id);
    void chooseFromStore();

    TemplateBridge *m_bridge = nullptr;
    const Store *m_store = nullptr;
    bool m_refreshed = false;
    qint64 m_editing = 0;
    int m_lastCompileOk = -1;  // -1 untested, 0 failed, 1 compiled
    QString m_sampleText;
    int m_ruleLine = 0;
    QString m_inputLine;

    QPushButton *m_authorTab = nullptr;
    QPushButton *m_browserTab = nullptr;
    QStackedWidget *m_pages = nullptr;

    QLineEdit *m_name = nullptr;
    QComboBox *m_platform = nullptr;
    QLineEdit *m_command = nullptr;
    QCheckBox *m_clean = nullptr;
    QPushButton *m_fromStore = nullptr;
    QPlainTextEdit *m_raw = nullptr;
    QPushButton *m_sample = nullptr;
    QPlainTextEdit *m_template = nullptr;
    QLabel *m_editState = nullptr;
    QPushButton *m_test = nullptr;
    QPushButton *m_sweep = nullptr;
    QPushButton *m_save = nullptr;
    QPushButton *m_clear = nullptr;

    QLabel *m_pill = nullptr;
    QLabel *m_placeholder = nullptr;
    QFrame *m_match = nullptr;
    QLabel *m_matchCaption = nullptr;
    QLabel *m_matchMeta = nullptr;
    QLabel *m_matchDetail = nullptr;
    QWidget *m_bars = nullptr;
    LabBar *m_barWidgets[4] = {};
    QLabel *m_barValues[4] = {};
    QPushButton *m_candidatesToggle = nullptr;
    QListWidget *m_candidates = nullptr;
    QLabel *m_recordCount = nullptr;
    QTableWidget *m_parsed = nullptr;

    QComboBox *m_browserPlatform = nullptr;
    QLineEdit *m_browserSearch = nullptr;
    QLabel *m_browserCount = nullptr;
    QTableWidget *m_browser = nullptr;
};

}  // namespace omegacat

#endif
