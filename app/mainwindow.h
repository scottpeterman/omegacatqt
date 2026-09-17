// app/mainwindow.h
//
// The window: a header with the three views, and the things they share -- the
// vault, the store, and the run.
#ifndef OMEGACAT_APP_MAINWINDOW_H
#define OMEGACAT_APP_MAINWINDOW_H

#include <QMainWindow>
#include <functional>
#include <memory>

#include "inventory.h"
#include "store.h"

class QButtonGroup;
class QLabel;
class QStackedWidget;

namespace omegacat {

class RunBridge;
class RunView;
class SearchView;
class SettingsDialog;
class StoreView;
class TemplateLabView;
class Vault;

class MainWindow : public QMainWindow {
    Q_OBJECT
public:
    enum View { Run = 0, StoreTab = 1, Search = 2, Templates = 3 };

    explicit MainWindow(QWidget *parent = nullptr);
    ~MainWindow() override;

    void showView(View v);
    View currentView() const;
    bool setStorePath(const QString &path);
    void setVaultPath(const QString &path);
    bool tryQuietUnlock();

    // The inventory the Run view selects from. The default is
    // ~/.omegacat/inventory.yaml.
    void setInventoryPath(const QString &path);
    QString inventoryPath() const { return m_inventoryPath; }
    // keyMap carries Run view ticks across edits that changed keys.
    bool reloadInventory(const QHash<QString, QString> &keyMap = {});
    // Opens the inventory manager (on focusKey's device, if given) and, when
    // it changed anything, reloads the Run view with ticks carried across.
    void manageInventory(const QString &focusKey = {});
    // Imports a map.json into the inventory, reloads the Run view and switches
    // it to the inventory. No dialogs: the menu entry adds the file picker and
    // the summary around this.
    bool importMap(const QString &mapPath, InventoryImport *result, QString *err);
    bool removeInventoryFolder(const QString &folder, QString *err);

    bool openDemo(int stepMs);
    // File > Settings. drive, when given, is called with the dialog shown
    // instead of running its event loop; it accepts or rejects it.
    bool openSettings(const std::function<void(SettingsDialog *)> &drive = {});
    bool startCapture(const QByteArray &request);

    Vault *vault() const { return m_vault.get(); }
    RunBridge *bridge() const { return m_bridge; }
    RunView *runView() const { return m_run; }
    StoreView *storeView() const { return m_store; }
    // The store the Store and Search views are open on.
    QString storePath() const { return m_storeHandle.root(); }
    SearchView *searchView() const { return m_search; }
    TemplateLabView *templateLab() const { return m_lab; }

private:
    void buildMenus();
    QWidget *buildHeader();
    void refreshVault();
    void unlockOrLock();
    void manageCredentials();
    void chooseStore();
    void chooseMapToImport();
    void confirmRemoveFolder(const QString &folder);
    void about();

    std::unique_ptr<Vault> m_vault;
    Store m_storeHandle;
    QString m_inventoryPath;
    RunBridge *m_bridge = nullptr;

    QButtonGroup *m_tabs = nullptr;
    QLabel *m_storeLabel = nullptr;
    QStackedWidget *m_stack = nullptr;
    RunView *m_run = nullptr;
    StoreView *m_store = nullptr;
    SearchView *m_search = nullptr;
    TemplateLabView *m_lab = nullptr;
    QLabel *m_status = nullptr;
};

QString defaultVaultPath();
QString defaultStorePath();

}  // namespace omegacat

#endif
