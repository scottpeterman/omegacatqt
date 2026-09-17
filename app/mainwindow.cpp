// app/mainwindow.cpp
#include "mainwindow.h"

#include "aboutdialog.h"
#include "appsettings.h"
#include "settingsdialog.h"

#include "inventorymanagerdialog.h"

#include <QActionGroup>
#include <QApplication>
#include <QButtonGroup>
#include <QDir>
#include <QFrame>
#include <QHBoxLayout>
#include <QJsonDocument>
#include <QJsonObject>
#include <QLabel>
#include <QMenuBar>
#include <QMessageBox>
#include <QPushButton>
#include <QSettings>
#include <QStackedWidget>
#include <QStatusBar>
#include <QVBoxLayout>

#include "filedialogs.h"
#include "panel.h"
#include "runbridge.h"
#include "runview.h"
#include "searchview.h"
#include "storeview.h"
#include "templatelabview.h"
#include "theme.h"
#include "vault.h"
#include "credentialmanagerdialog.h"
#include "vaultunlockdialog.h"

namespace omegacat {

QString defaultVaultPath() { return QDir::home().filePath(QStringLiteral(".omegacat/vault.json")); }
QString defaultStorePath() { return QDir::home().filePath(QStringLiteral("captures")); }

MainWindow::MainWindow(QWidget *parent) : QMainWindow(parent) {
    initResources();
    setWindowTitle(QStringLiteral("OmegaCat"));
    setMinimumSize(1100, 680);
    resize(1400, 860);

    m_bridge = new RunBridge(this);
    m_vault = std::make_unique<Vault>(QSettings().value(QStringLiteral("vault"), defaultVaultPath()).toString());

    auto *central = new QWidget(this);
    central->setObjectName(QStringLiteral("central"));
    auto *col = new QVBoxLayout(central);
    col->setContentsMargins(0, 0, 0, 0);
    col->setSpacing(0);
    col->addWidget(buildHeader());

    m_stack = new QStackedWidget(central);
    m_run = new RunView(m_stack);
    m_store = new StoreView(m_stack);
    m_search = new SearchView(m_stack);
    m_lab = new TemplateLabView(m_stack);
    m_stack->addWidget(m_run);
    m_stack->addWidget(m_store);
    m_stack->addWidget(m_search);
    m_stack->addWidget(m_lab);
    col->addWidget(m_stack, 1);
    setCentralWidget(central);

    m_status = new QLabel(this);
    statusBar()->addWidget(m_status, 1);
    statusBar()->addPermanentWidget(new QLabel(RunBridge::libraryVersion(), this));

    buildMenus();

    connect(m_run, &RunView::captureRequested, this, [this](const QByteArray &req) { startCapture(req); });
    connect(m_run, &RunView::demoRequested, this, [this] { openDemo(40); });
    connect(m_run, &RunView::cancelRequested, m_bridge, &RunBridge::cancel);
    connect(m_run, &RunView::unlockRequested, this, &MainWindow::unlockOrLock);
    connect(m_run, &RunView::importMapRequested, this, &MainWindow::chooseMapToImport);
    connect(m_run, &RunView::removeFolderRequested, this, &MainWindow::confirmRemoveFolder);
    connect(m_run, &RunView::manageInventoryRequested, this, [this](const QString &key) { manageInventory(key); });
    connect(m_run, &RunView::sourceChanged, this, [](RunView::Source s) {
        QSettings().setValue(QStringLiteral("run/source"),
                             s == RunView::Source::Inventory ? QStringLiteral("inventory") : QStringLiteral("list"));
    });
    connect(m_run, &RunView::openInStore, this, [this](const QString &dev, const QString &type, const QString &file) {
        showView(StoreTab);
        if (!m_store->openFile(dev, type, file))
            m_status->setText(tr("%1 / %2 is not in this store (a demo row is not a real file)").arg(dev, type));
    });
    connect(m_search, &SearchView::openInStore, this,
            [this](const QString &dev, const QString &type, const QString &file, int line) {
                showView(StoreTab);
                m_store->openFile(dev, type, file, line);
            });

    connect(m_store, &StoreView::openInLab, this, [this](const QString &dev, const QString &type, const QString &file) {
        showView(Templates);
        QString err;
        if (!m_lab->loadFromStore(dev, type, file, &err)) m_status->setText(err);
    });
    connect(m_lab, &TemplateLabView::status, m_status, &QLabel::setText);

    connect(m_bridge, &RunBridge::rowsChanged, m_run, &RunView::addRows);
    connect(m_bridge, &RunBridge::decisionsAdded, m_run, &RunView::addDecisions);
    connect(m_bridge, &RunBridge::progressChanged, m_run, &RunView::setProgress);
    connect(m_bridge, &RunBridge::finished, this, [this] {
        const RunResult r = m_bridge->result();
        m_run->runFinished(r);
        m_status->setText(tr("%1 %2").arg(r.kind, r.state));
        if (r.kind == QLatin1String("capture")) m_store->refresh();
    });

    const QSettings settings;
    setStorePath(appsettings::launchStore());
    m_run->applyCaptureDefaults(appsettings::captureDefaults());
    setInventoryPath(inventory::defaultPath());
    if (settings.value(QStringLiteral("run/source")).toString() == QLatin1String("inventory"))
        m_run->setSource(RunView::Source::Inventory);
    refreshVault();
    // ThemeManager::setTheme applies the sheet on every change; this is the
    // first application, for a theme set before the window existed.
    qApp->setStyleSheet(omegacat::styleSheet(ThemeManager::instance().tokens()));
    showView(Run);
}

MainWindow::~MainWindow() {
    // The run reads the vault handle; close it before the vault goes.
    m_bridge->close();
}

QWidget *MainWindow::buildHeader() {
    auto *header = new QFrame(this);
    header->setProperty("role", QStringLiteral("header"));
    auto *row = new QHBoxLayout(header);
    row->setContentsMargins(16, 0, 16, 0);
    row->setSpacing(4);
    auto *mark = makeCaption(QStringLiteral("OMEGACAT"), "wordmark", 12, header);
    mark->setObjectName(QStringLiteral("wordmark"));
    // The wordmark is the About box, the way a logo is on a web page.
    AboutDialog::makeTrigger(mark);
    row->addWidget(mark);
    row->addSpacing(24);

    m_tabs = new QButtonGroup(header);
    const char *names[] = {"Run", "Store", "Search", "Templates"};
    for (int i = 0; i < 4; ++i) {
        auto *b = new QPushButton(tr(names[i]), header);
        b->setProperty("role", QStringLiteral("viewTab"));
        b->setCheckable(true);
        b->setCursor(Qt::PointingHandCursor);
        m_tabs->addButton(b, i);
        row->addWidget(b);
    }
    connect(m_tabs, &QButtonGroup::idClicked, this, [this](int id) { showView(View(id)); });

    row->addStretch(1);
    m_storeLabel = new QLabel(header);
    m_storeLabel->setProperty("role", QStringLiteral("subtle"));
    row->addWidget(m_storeLabel);
    auto *change = new QPushButton(tr("Change\u2026"), header);
    connect(change, &QPushButton::clicked, this, &MainWindow::chooseStore);
    row->addWidget(change);
    return header;
}

void MainWindow::buildMenus() {
    QMenu *file = menuBar()->addMenu(tr("&File"));
    file->addAction(tr("Open store\u2026"), this, &MainWindow::chooseStore);
    file->addAction(tr("Import omegamaps map.json\u2026"), this, &MainWindow::chooseMapToImport);
    file->addAction(tr("Manage inventory\u2026"), this, [this] { manageInventory(); });
    file->addSeparator();
    // Built by hand for the same reason as Quit below.
    QAction *prefs = file->addAction(tr("Settings\u2026"));
    // Not QKeySequence::Preferences: that is Cmd+, on macOS and unbound on
    // most Linux desktops and Windows. Qt::CTRL is Cmd on macOS, so this is
    // the platform's usual key everywhere.
    prefs->setShortcut(QKeySequence(Qt::CTRL | Qt::Key_Comma));
    prefs->setMenuRole(QAction::PreferencesRole);  // macOS: OmegaCat > Settings, where it belongs
    connect(prefs, &QAction::triggered, this, [this] { openSettings(); });
    file->addSeparator();
    // Built by hand: addAction(text, receiver, slot, shortcut) is deprecated
    // from Qt 6.4, and the replacement argument order does not exist in 6.2.
    QAction *quit = file->addAction(tr("Quit"));
    quit->setShortcut(QKeySequence::Quit);
    connect(quit, &QAction::triggered, qApp, &QApplication::quit);

    QMenu *vault = menuBar()->addMenu(tr("&Vault"));
    QAction *manage = vault->addAction(tr("Manage credentials\u2026"));
    manage->setShortcut(QKeySequence(Qt::CTRL | Qt::SHIFT | Qt::Key_K));
    connect(manage, &QAction::triggered, this, &MainWindow::manageCredentials);
    vault->addSeparator();
    vault->addAction(tr("Unlock or lock\u2026"), this, &MainWindow::unlockOrLock);

    QMenu *view = menuBar()->addMenu(tr("&View"));
    auto *themes = new QActionGroup(this);
    for (ThemeId id : {ThemeId::Light, ThemeId::Dark, ThemeId::Cyber}) {
        QAction *a = view->addAction(tokensFor(id).name);
        a->setCheckable(true);
        a->setChecked(ThemeManager::instance().id() == id);
        themes->addAction(a);
        connect(a, &QAction::triggered, this, [id] {
            ThemeManager::instance().setTheme(id);
            appsettings::setTheme(themeKey(id));
        });
        // The tick follows the theme however it changed: Settings previews
        // and reverts themes while it is open.
        connect(&ThemeManager::instance(), &ThemeManager::changed, a,
                [a, id] { a->setChecked(ThemeManager::instance().id() == id); });
    }

    QMenu *help = menuBar()->addMenu(tr("&Help"));
    QAction *about = help->addAction(tr("About OmegaCat"), this, &MainWindow::about);
    about->setMenuRole(QAction::AboutRole);  // macOS: the application menu, where About belongs
    help->addAction(tr("Attributions"), this, [this] { AboutDialog::showAttributionsFor(this); });
    QAction *aboutQt = help->addAction(tr("About Qt"), qApp, &QApplication::aboutQt);
    aboutQt->setMenuRole(QAction::AboutQtRole);
}

void MainWindow::showView(View v) {
    // The Lab reads the template database the first time it is shown, so a
    // session that never opens it never creates ~/.omegacat/tfsm_templates.db.
    if (v == Templates && !m_lab->refreshed()) m_lab->refresh();
    m_stack->setCurrentIndex(int(v));
    if (QAbstractButton *b = m_tabs->button(int(v))) b->setChecked(true);
}

MainWindow::View MainWindow::currentView() const { return View(m_stack->currentIndex()); }

bool MainWindow::setStorePath(const QString &path) {
    QString err;
    if (!m_storeHandle.open(path, &err)) {
        m_status->setText(err);
        return false;
    }
    m_storeLabel->setText(QDir::toNativeSeparators(path));
    m_run->setStorePath(path);
    m_store->setStore(&m_storeHandle);
    m_search->setStore(&m_storeHandle);
    m_lab->setStore(&m_storeHandle);
    appsettings::setLastStore(path);
    return true;
}

void MainWindow::setVaultPath(const QString &path) {
    m_bridge->close();
    m_vault = std::make_unique<Vault>(path);
    refreshVault();
}

bool MainWindow::tryQuietUnlock() {
    if (!m_vault->exists() || !m_vault->isLocked()) return false;
    const bool ok = m_vault->unlockQuiet() == VaultError::Ok;
    refreshVault();
    return ok;
}

void MainWindow::refreshVault() {
    if (!m_vault->exists()) {
        m_run->setVaultState(tr("No vault yet"), QStringLiteral("warning"), tr("Create\u2026"));
    } else if (m_vault->isLocked()) {
        m_run->setVaultState(tr("Vault locked"), QStringLiteral("warning"), tr("Unlock\u2026"));
    } else {
        // The count is the useful half: an unlocked vault with nothing a
        // capture can offer fails every device, and it is cheaper to see
        // that here than in the results table.
        QVector<CredentialMeta> metas;
        int ssh = 0;
        if (m_vault->list(&metas) == VaultError::Ok)
            for (const CredentialMeta &m : metas)
                if (!m.isSnmp && !m.disabled) ++ssh;
        if (ssh == 0)
            m_run->setVaultState(tr("Vault unlocked, no usable credentials"), QStringLiteral("warning"), tr("Lock"));
        else
            m_run->setVaultState(ssh == 1 ? tr("Vault unlocked \u00B7 1 credential")
                                          : tr("Vault unlocked \u00B7 %1 credentials").arg(ssh),
                                 QStringLiteral("success"), tr("Lock"));
    }
}

void MainWindow::unlockOrLock() {
    if (m_vault->exists() && !m_vault->isLocked()) {
        m_vault->lock();
    } else {
        // Omega's unlock dialog: create when there is no vault, unlock when
        // there is, and the keyring row either way.
        VaultUnlockDialog dlg(m_vault.get(), this);
        dlg.exec();
    }
    refreshVault();
}

void MainWindow::manageCredentials() {
    // The manager unlocks for itself when it needs to; a capture running
    // against this vault keeps its handle whatever is edited here.
    CredentialManagerDialog dlg(m_vault.get(), this);
    dlg.exec();
    refreshVault();
}

void MainWindow::chooseStore() {
    const QString dir = pickDirectory(this, tr("Open store"), m_storeHandle.root());
    if (!dir.isEmpty()) setStorePath(dir);
}

bool MainWindow::openDemo(int stepMs) {
    QString err;
    m_run->runStarted(tr("Demo"));
    if (!m_bridge->openDemo(stepMs, &err)) {
        m_status->setText(err);
        return false;
    }
    showView(Run);
    return true;
}

bool MainWindow::startCapture(const QByteArray &request) {
    if (m_vault->isLocked()) {
        unlockOrLock();
        if (m_vault->isLocked()) {
            m_status->setText(tr("A capture needs the vault unlocked."));
            return false;
        }
    }
    // The form's store field is editable, so a capture can name a store the
    // Store and Search views are not open on. Follow it before the run starts:
    // otherwise the captures land in one store and the views keep showing
    // another, and a capture that worked looks lost. setStorePath creates the
    // directory, so a store typed for the first time opens here too.
    const QString requested =
        QJsonDocument::fromJson(request).object().value(QLatin1String("store_path")).toString().trimmed();
    if (!requested.isEmpty() && QDir::cleanPath(requested) != QDir::cleanPath(m_storeHandle.root())) {
        if (!setStorePath(requested)) return false;  // setStorePath put the reason in the status bar
    }
    QString err;
    m_run->runStarted(tr("Capture"));
    if (!m_bridge->openCapture(request, m_vault->handle(), &err)) {
        RunResult r;
        r.kind = QStringLiteral("capture");
        r.state = QStringLiteral("failed");
        r.error = err;
        m_run->runFinished(r);
        m_status->setText(err);
        return false;
    }
    showView(Run);
    return true;
}

void MainWindow::setInventoryPath(const QString &path) {
    m_inventoryPath = path;
    reloadInventory();
}

bool MainWindow::reloadInventory(const QHash<QString, QString> &keyMap) {
    QVector<InventoryFolder> folders;
    QString err;
    if (!inventory::load(m_inventoryPath, &folders, &err)) {
        // A damaged file is shown as empty with the reason, not half-loaded.
        m_run->setInventory(m_inventoryPath, {});
        m_status->setText(err);
        return false;
    }
    m_run->setInventory(m_inventoryPath, folders, keyMap);
    return true;
}

void MainWindow::manageInventory(const QString &focusKey) {
    InventoryManagerDialog dialog(m_inventoryPath, m_vault.get(), focusKey, this);
    dialog.exec();
    if (dialog.changed()) {
        reloadInventory(dialog.keyChanges());
        m_status->setText(tr("Inventory saved"));
    }
}

bool MainWindow::importMap(const QString &mapPath, InventoryImport *result, QString *err) {
    if (!inventory::importMap(m_inventoryPath, mapPath, QString(), result, err)) {
        m_status->setText(*err);
        return false;
    }
    reloadInventory();
    m_run->setSource(RunView::Source::Inventory);
    showView(Run);
    m_status->setText(result->message.section(QLatin1Char('\n'), 0, 0));
    return true;
}

bool MainWindow::removeInventoryFolder(const QString &folder, QString *err) {
    if (!inventory::removeFolder(m_inventoryPath, folder, err)) {
        m_status->setText(*err);
        return false;
    }
    reloadInventory();
    m_status->setText(tr("Removed \u201C%1\u201D from the inventory").arg(folder));
    return true;
}

void MainWindow::chooseMapToImport() {
    const QString path = pickOpenFile(this, tr("Import omegamaps map"), QDir::homePath(),
                                      tr("Topology map (*.json);;All files (*)"));
    if (path.isEmpty()) return;
    InventoryImport result;
    QString err;
    if (!importMap(path, &result, &err)) {
        QMessageBox::warning(this, tr("Import map"), err);
        return;
    }
    QMessageBox::information(this, tr("Import map"), result.message);
}

void MainWindow::confirmRemoveFolder(const QString &folder) {
    const auto answer = QMessageBox::question(
        this, tr("Remove folder"),
        tr("Remove \u201C%1\u201D and every device in it from the inventory?\n\n"
           "Captures already in the store are not touched.").arg(folder));
    if (answer != QMessageBox::Yes) return;
    QString err;
    if (!removeInventoryFolder(folder, &err)) QMessageBox::warning(this, tr("Remove folder"), err);
}

void MainWindow::about() { AboutDialog::showFor(this); }

bool MainWindow::openSettings(const std::function<void(SettingsDialog *)> &drive) {
    SettingsDialog dlg(this);
    if (drive) {
        // The probe's way in: it fills the fields and decides, with no event
        // loop of the dialog's own to wait on.
        dlg.show();
        drive(&dlg);
        if (dlg.result() != QDialog::Accepted) return false;
    } else if (dlg.exec() != QDialog::Accepted) {
        return false;
    }
    // Saved. The Run form takes the new defaults now; the store at launch is
    // for next time, and the theme is already showing.
    m_run->applyCaptureDefaults(appsettings::captureDefaults());
    m_status->setText(tr("Settings saved"));
    return true;
}

}  // namespace omegacat
