// app/storeview.h
//
// The Store view: what a store holds, with no run. Devices on the left; the
// selected capture in the middle, as the raw file or, for a parsed type, the
// parsed table; types and attempt history on the right.
#ifndef OMEGACAT_APP_STOREVIEW_H
#define OMEGACAT_APP_STOREVIEW_H

#include <QWidget>

#include "store.h"

class QLabel;
class QPlainTextEdit;
class QPushButton;
class QStackedWidget;
class QTableWidget;
class QTreeWidget;

namespace omegacat {

class StoreView : public QWidget {
    Q_OBJECT
public:
    explicit StoreView(QWidget *parent = nullptr);

    void setStore(const Store *store);
    void refresh();

    // Selects a device, a type and a file, the way "Open in store" and a
    // search hit do. Returns false when the store does not have them.
    bool openFile(const QString &canonical, const QString &type, const QString &file, int line = 0);
    void showParsed(bool parsed);

    // Raw, the parse, or a diff. Diff shows the selected history version
    // against the stored version before it, or two selected versions against
    // each other, for the types Store::diffable accepts.
    enum class Mode { Raw, Parsed, Diff };
    void showMode(Mode mode);

    QTreeWidget *devices() const { return m_devices; }
    QTableWidget *parsedTable() const { return m_parsed; }
    QPlainTextEdit *content() const { return m_content; }
    QPushButton *parsedButton() const { return m_parsedBtn; }
    QPushButton *diffButton() const { return m_diffBtn; }
    QPlainTextEdit *diffView() const { return m_diffView; }
    QLabel *diffInfo() const { return m_diffInfo; }
    QTableWidget *historyTable() const { return m_history; }
    QString currentFile() const { return m_file; }
    QPushButton *labButton() const { return m_labBtn; }
    QPushButton *copyButton() const { return m_copyBtn; }
    QPushButton *exportButton() const { return m_exportBtn; }

    // What Copy puts on the clipboard in the current mode: the capture as
    // stored (Raw), the records as tab-separated columns (Parsed), or the
    // diff as shown (Diff).
    QString copyText() const;
    // Writes the shown parse's records to path as CSV. Export CSV asks for
    // the path and calls this.
    bool exportParsed(const QString &path, QString *err) const;

signals:
    // The shown capture, to open in the Template Lab.
    void openInLab(const QString &canonical, const QString &type, const QString &file);

private:
    QWidget *buildDevices();
    QWidget *buildContent();
    QWidget *buildDetails();
    void deviceSelected();
    void typeSelected();
    void historySelected();
    void load(const QString &file, int line);
    void updateDiffAvailability();
    void renderDiff();
    void updateOutputButtons();

    const Store *m_store = nullptr;
    QString m_device;
    QString m_type;
    QString m_file;
    QString m_fileSha;
    int m_line = 0;

    QTreeWidget *m_devices = nullptr;
    QLabel *m_unreadable = nullptr;
    QLabel *m_contentTitle = nullptr;
    QPushButton *m_rawBtn = nullptr;
    QPushButton *m_parsedBtn = nullptr;
    QPushButton *m_diffBtn = nullptr;
    QPushButton *m_labBtn = nullptr;
    QPushButton *m_copyBtn = nullptr;
    QPushButton *m_exportBtn = nullptr;
    StoreParsed m_shownParse;  // valid only while its records are the ones in the table
    QPlainTextEdit *m_diffView = nullptr;
    QLabel *m_diffInfo = nullptr;
    QString m_diffOlder, m_diffNewer;  // set by a two-row history selection
    QStackedWidget *m_stack = nullptr;
    QPlainTextEdit *m_content = nullptr;
    QTableWidget *m_parsed = nullptr;
    QLabel *m_parseInfo = nullptr;
    QTableWidget *m_types = nullptr;
    QTableWidget *m_history = nullptr;
};

}  // namespace omegacat

#endif
