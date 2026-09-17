// app/runview.h
//
// The Run view: the capture form, the results table, and progress with the
// decisions a run made. It shows what a RunBridge tells it and owns no run.
#ifndef OMEGACAT_APP_RUNVIEW_H
#define OMEGACAT_APP_RUNVIEW_H

#include <QAbstractTableModel>
#include <QHash>
#include <QJsonObject>
#include <QWidget>

#include "appsettings.h"
#include "inventory.h"
#include "runtypes.h"

class QCheckBox;
class QComboBox;
class QLabel;
class QLineEdit;
class QListWidget;
class QPlainTextEdit;
class QProgressBar;
class QPushButton;
class QStackedWidget;
class QTableView;
class QTreeWidget;
class QTreeWidgetItem;

namespace omegacat {

class StatBox;

class ResultsModel : public QAbstractTableModel {
    Q_OBJECT
public:
    enum Column { Device, Type, State, Platform, Bytes, Time, Parsed, Detail, ColumnCount };

    explicit ResultsModel(QObject *parent = nullptr);
    int rowCount(const QModelIndex &parent = QModelIndex()) const override;
    int columnCount(const QModelIndex &parent = QModelIndex()) const override;
    QVariant data(const QModelIndex &index, int role) const override;
    QVariant headerData(int section, Qt::Orientation o, int role) const override;

    void clear();
    void merge(const QVector<RunRow> &rows);
    const RunRow &rowAt(int i) const { return m_rows.at(i); }

private:
    QVector<RunRow> m_rows;
    QHash<QString, int> m_index;
};

class RunView : public QWidget {
    Q_OBJECT
public:
    explicit RunView(QWidget *parent = nullptr);

    // The request the form describes, as JSON for omegacat_capture_open.
    QByteArray request() const;

    // Where the devices come from: the typed list, or ticked inventory
    // entries (sent as session_file + session_keys).
    enum class Source { List, Inventory };
    Source source() const;
    void setSource(Source s);

    // Replaces the inventory tree. Ticks survive a reload for every session
    // still in it, so importing a second map does not clear a selection.
    // keyMap carries ticks across edits that changed keys (original -> new,
    // "" deleted).
    void setInventory(const QString &path, const QVector<InventoryFolder> &folders,
                      const QHash<QString, QString> &keyMap = {});
    // The session keys ticked in the inventory, in tree order, each once.
    QStringList checkedKeys() const;
    // Ticks or unticks the session with this key. False if there is none.
    bool setSessionChecked(const QString &key, bool checked);
    void setStorePath(const QString &path);
    // Puts the form's capture options to these: types, host keys, legacy,
    // parsing, credential tags. Devices and the store are left alone.
    void applyCaptureDefaults(const CaptureDefaults &d);
    QCheckBox *legacyBox() const { return m_legacy; }
    QCheckBox *parseBox() const { return m_parse; }
    // action is the button's text: Create, Unlock or Lock.
    void setVaultState(const QString &text, const QString &tone, const QString &action);

    // Fed by the window from its RunBridge.
    void runStarted(const QString &label);
    void addRows(const QVector<RunRow> &rows);
    void addDecisions(const QVector<RunDecision> &decisions);
    void setProgress(const RunProgress &p);
    void runFinished(const RunResult &result);

    // Fields merged into request() over what the form describes. Empty in
    // the application; app_probe uses it to keep known_hosts and the
    // template database out of the real ~/.omegacat while every other field
    // still comes from the widgets.
    void setRequestExtras(const QJsonObject &extras) { m_requestExtras = extras; }

    ResultsModel *model() const { return m_model; }
    QListWidget *decisions() const { return m_decisions; }

    // The form, for app_probe to fill and click the way a user does.
    QPlainTextEdit *devicesEdit() const { return m_devices; }
    QListWidget *typesList() const { return m_types; }
    QLineEdit *storeEdit() const { return m_store; }
    QComboBox *hostKeysBox() const { return m_hostKeys; }
    QLineEdit *credTagsEdit() const { return m_credTags; }
    QPushButton *startButton() const { return m_start; }
    QTreeWidget *inventoryTree() const { return m_inventory; }
    QLineEdit *inventoryFilter() const { return m_inventoryFilter; }
    QPushButton *inventoryNoneButton() const { return m_inventoryNone; }
    int statValue(const QString &name) const;

signals:
    void captureRequested(const QByteArray &request);
    void demoRequested();
    void cancelRequested();
    void unlockRequested();
    void importMapRequested();
    void removeFolderRequested(const QString &folder);
    void manageInventoryRequested(const QString &key);
    void sourceChanged(Source source);
    void openInStore(const QString &canonical, const QString &type, const QString &file);

private:
    QWidget *buildForm();
    QWidget *buildResults();
    QWidget *buildProgress();
    void selectionChanged();
    void startClicked();
    void setRunning(bool running);
    QWidget *buildDevicesField(QWidget *panel);
    void applyInventoryFilter();
    void inventoryItemChanged(QTreeWidgetItem *item, int column);
    void refreshFolderStates();
    void updateInventoryCount();

    // form
    QJsonObject m_requestExtras;
    QPlainTextEdit *m_devices = nullptr;
    QPushButton *m_listBtn = nullptr;
    QPushButton *m_inventoryBtn = nullptr;
    QStackedWidget *m_sourceStack = nullptr;
    QLabel *m_sourceHint = nullptr;
    QString m_inventoryPath;
    QTreeWidget *m_inventory = nullptr;
    QLineEdit *m_inventoryFilter = nullptr;
    QPushButton *m_inventoryNone = nullptr;
    bool m_syncingChecks = false;  // set while the view itself changes check states

    QListWidget *m_types = nullptr;
    QLineEdit *m_store = nullptr;
    QComboBox *m_hostKeys = nullptr;
    QCheckBox *m_legacy = nullptr;
    QCheckBox *m_parse = nullptr;
    QLineEdit *m_credTags = nullptr;
    QLabel *m_vaultState = nullptr;
    QPushButton *m_unlock = nullptr;
    QLabel *m_formError = nullptr;
    QPushButton *m_start = nullptr;
    QPushButton *m_cancel = nullptr;
    QPushButton *m_demo = nullptr;

    // results
    ResultsModel *m_model = nullptr;
    QTableView *m_table = nullptr;
    QLabel *m_runLabel = nullptr;
    QLabel *m_selection = nullptr;
    QPushButton *m_open = nullptr;

    // progress
    QProgressBar *m_bar = nullptr;
    QLabel *m_barText = nullptr;
    QHash<QString, StatBox *> m_stats;
    QHash<QString, int> m_statValues;
    QListWidget *m_decisions = nullptr;
};

}  // namespace omegacat

#endif
