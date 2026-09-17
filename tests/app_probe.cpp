// tests/app_probe.cpp
//
// Drives the application's own window with no display and checks that what
// the views show agrees with what they were fed:
//
//   Run     the demo, played through the window: the results table holds
//           every row the run has, none left running; the counters are the
//           run's counters; the decisions list has every decision
//   Store   a live capture of the fake devices, through the window: both
//           devices listed, an ARP table opens with its parsed records
//   Search  a query across that store finds the devices, and a hit opens
//           its file at the line
//   Diff    a second running-config version written beside the first: Diff
//           is offered for it and not for ARP, shows +1/-0 with the added line,
//           and a two-row history selection switches to the same diff
//   Form    a capture started the way a user starts one: devices typed into
//           the box, types ticked, a new store typed into the field, Start
//           clicked; the Store view follows the store the capture wrote to
//   Manager the inventory manager: one device edited (a host change carries
//           the Run view's tick to the new key), several edited together, a
//           clashing address refused with nothing written, a device added, and
//           a platform set on a device captured as that platform with the
//           device's disagreement in the decisions list
//   Inventory  an omegamaps map.json imported through the window, re-imported
//           without duplicates; one of two devices port-forwarded behind one
//           address ticked and captured, and only that one
//
// Saves a grab of each view per theme. Runs offscreen:
//
//   QT_QPA_PLATFORM=offscreen app_probe <grab-prefix> [fakedevice templates.db]
//
// Without the fake device and template database the Store and Search checks
// are skipped. Exit status is the number of failed checks.

#include <QApplication>
#include <QClipboard>
#include <QMenuBar>
#include <QMouseEvent>
#include <QRegularExpression>
#include <QTextBrowser>
#include <QCheckBox>
#include <QComboBox>
#include <QDir>
#include <QElapsedTimer>
#include <QFile>
#include <QFileInfo>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QLabel>
#include <QLineEdit>
#include <QListWidget>
#include <QPlainTextEdit>
#include <QProcess>
#include <QPushButton>
#include <QRadioButton>
#include <QSet>
#include <QSettings>
#include <QStyleFactory>
#include <QTableWidget>
#include <QTemporaryDir>
#include <QTreeWidget>

#include <cstdio>

#include "aboutdialog.h"
#include "appsettings.h"
#include "exporting.h"
#include "settingsdialog.h"
#include "credentialeditordialog.h"
#include "credentialmanagerdialog.h"
#include "inventorymanagerdialog.h"
#include "mainwindow.h"
#include "runbridge.h"
#include "runview.h"
#include "searchview.h"
#include "storeview.h"
#include "templatelabview.h"
#include "theme.h"
#include "vault.h"
#include "vaultunlockdialog.h"

using namespace omegacat;

namespace {

int failures = 0;

void check(bool ok, const QString &what) {
    std::printf("%s  %s\n", ok ? "ok  " : "FAIL", qPrintable(what));
    std::fflush(stdout);
    if (!ok) ++failures;
}

// Pumps events until pred holds or the time runs out.
bool waitFor(const std::function<bool()> &pred, int ms) {
    QElapsedTimer t;
    t.start();
    while (!pred()) {
        if (t.elapsed() > ms) return false;
        QApplication::processEvents(QEventLoop::AllEvents, 50);
    }
    return true;
}

// Reads RFC 4180 back, to check what the exports wrote rather than trusting
// the writer's own idea of it.
QVector<QStringList> readCsv(const QByteArray &data) {
    QVector<QStringList> out;
    QStringList row;
    QByteArray field;
    bool quoted = false, inQuotes = false;
    for (int i = 0; i < data.size(); ++i) {
        const char c = data.at(i);
        if (inQuotes) {
            if (c == '"' && i + 1 < data.size() && data.at(i + 1) == '"') {
                field.append('"');
                ++i;
            } else if (c == '"') {
                inQuotes = false;
            } else {
                field.append(c);
            }
        } else if (c == '"' && field.isEmpty() && !quoted) {
            inQuotes = quoted = true;
        } else if (c == ',') {
            row << QString::fromUtf8(field);
            field.clear();
            quoted = false;
        } else if (c == '\r' && i + 1 < data.size() && data.at(i + 1) == '\n') {
            row << QString::fromUtf8(field);
            out.append(row);
            row.clear();
            field.clear();
            quoted = false;
            ++i;
        } else {
            field.append(c);
        }
    }
    if (!field.isEmpty() || !row.isEmpty()) {
        row << QString::fromUtf8(field);
        out.append(row);
    }
    return out;
}

void grabAll(MainWindow &w, const QString &prefix, MainWindow::View view, const QString &name) {
    for (ThemeId id : {ThemeId::Light, ThemeId::Dark, ThemeId::Cyber}) {
        ThemeManager::instance().setTheme(id);
        w.showView(view);
        QApplication::processEvents();
        const QString path = QStringLiteral("%1-%2-%3.png").arg(prefix, name, themeKey(id));
        check(w.grab().save(path), QStringLiteral("grab %1").arg(path));
    }
    ThemeManager::instance().setTheme(ThemeId::Light);
}

}  // namespace

int main(int argc, char **argv) {
    QApplication app(argc, argv);
    QApplication::setOrganizationName(QStringLiteral("omegacat-probe"));
    QApplication::setStyle(QStyleFactory::create(QStringLiteral("Fusion")));
    if (argc < 2) {
        std::fprintf(stderr, "usage: %s <grab-prefix> [fakedevice templates.db]\n", argv[0]);
        return 2;
    }
    const QString prefix = QString::fromLocal8Bit(argv[1]);
    const bool live = argc >= 4;

    QTemporaryDir tmp;
    ThemeManager::instance().setTheme(ThemeId::Light);
    // The window restores the last device source; a previous probe run that
    // ended on the inventory must not start this one there.
    QSettings().remove(QStringLiteral("run/source"));
    // Settings a previous probe run saved, or left behind by failing midway,
    // would change the form every check below assumes.
    QSettings().remove(QStringLiteral("capture"));
    QSettings().remove(QStringLiteral("launch"));
    // The diff ignore rules the probe's diffs use, never ~/.omegacat's.
    const QString rulesPath = tmp.filePath(QStringLiteral("diff-ignore.yaml"));
    {
        QFile rules(rulesPath);
        if (rules.open(QIODevice::WriteOnly))
            rules.write("platforms:\n  cisco_ios:\n    - '^! probe generated at '\n");
    }
    Store::diffIgnorePath(rulesPath);
    MainWindow w;
    w.setInventoryPath(tmp.filePath(QStringLiteral("home/.omegacat/inventory.yaml")));
    // A directory that does not exist yet, as ~/.omegacat is on a machine
    // that has never run ocvault: creating the vault has to make it.
    w.setVaultPath(tmp.filePath(QStringLiteral("home/.omegacat/vault.json")));
    w.setStorePath(tmp.filePath(QStringLiteral("store")));
    w.resize(1400, 860);
    w.show();

    // ---- Run: the demo -------------------------------------------------
    int decisionsSeen = 0;
    QObject::connect(w.bridge(), &RunBridge::decisionsAdded,
                     [&](const QVector<RunDecision> &d) { decisionsSeen += int(d.size()); });
    check(w.openDemo(0), QStringLiteral("demo opens"));
    check(waitFor([&] { return w.bridge()->isFinished(); }, 10000), QStringLiteral("demo finishes"));
    QApplication::processEvents();

    const RunProgress p = w.bridge()->progress();
    ResultsModel *model = w.runView()->model();
    check(model->rowCount() == p.total && p.total > 0,
          QStringLiteral("results table has every row (%1 of %2)").arg(model->rowCount()).arg(p.total));
    bool anyRunning = false;
    QSet<QString> states;
    for (int i = 0; i < model->rowCount(); ++i) {
        states.insert(model->rowAt(i).state);
        if (model->rowAt(i).state == QLatin1String("running")) anyRunning = true;
    }
    check(!anyRunning, QStringLiteral("no row is left running"));
    check(states.contains(QStringLiteral("stored")) && states.contains(QStringLiteral("unchanged")) &&
              states.contains(QStringLiteral("not applicable")) && states.contains(QStringLiteral("failed")),
          QStringLiteral("every terminal state is in the table"));
    check(w.runView()->statValue(QStringLiteral("stored")) == p.counts.stored &&
              w.runView()->statValue(QStringLiteral("failed")) == p.counts.failed &&
              w.runView()->statValue(QStringLiteral("unchanged")) == p.counts.unchanged,
          QStringLiteral("counters are the run's counters"));
    check(w.runView()->decisions()->count() == decisionsSeen && decisionsSeen > 0,
          QStringLiteral("decisions list has every decision (%1)").arg(decisionsSeen));
    grabAll(w, prefix, MainWindow::Run, QStringLiteral("run"));

    // ---- About and Attributions ----------------------------------------
    {
        auto openAbout = [] () -> AboutDialog * {
            for (QWidget *tl : QApplication::topLevelWidgets())
                if (auto *d = qobject_cast<AboutDialog *>(tl); d && d->isVisible()) return d;
            return nullptr;
        };
        auto *mark = w.findChild<QLabel *>(QStringLiteral("wordmark"));
        check(mark != nullptr, QStringLiteral("about: the wordmark is findable"));
        auto clickMark = [&] {
            QMouseEvent release(QEvent::MouseButtonRelease, QPointF(4, 4), mark->mapToGlobal(QPointF(4, 4)),
                                Qt::LeftButton, Qt::NoButton, Qt::NoModifier);
            QApplication::sendEvent(mark, &release);
            QApplication::processEvents();
        };
        if (mark) clickMark();
        AboutDialog *about = openAbout();
        check(about != nullptr, QStringLiteral("about: a wordmark click opens the About box"));
        if (about) {
            const QPixmap pm = about->splash()->pixmap(Qt::ReturnByValue);
            check(!pm.isNull() && qRound(pm.width() / pm.devicePixelRatio()) == 784,
                  QStringLiteral("about: the splash is shown at 784 wide (%1)").arg(pm.width()));
            check(about->details().contains(QString::fromLatin1(qVersion())),
                  QStringLiteral("about: details carry the running Qt version"));
            clickMark();
            check(openAbout() == about, QStringLiteral("about: a second click raises the same box"));
            check(about->aboutQtButton() != nullptr, QStringLiteral("about: offers About Qt"));
            check(!about->attributionsVisible(), QStringLiteral("about: Attributions starts closed"));

            // Every document is compiled in and non-empty.
            const QStringList docs = AboutDialog::documentPaths();
            QString notices, lgpl;
            for (const QString &path : docs) {
                QFile f(path);
                const bool ok = f.open(QIODevice::ReadOnly) && f.size() > 1000;
                check(ok, QStringLiteral("about: %1 is compiled in").arg(path));
                if (path.endsWith(QLatin1String("THIRD_PARTY_NOTICES.md"))) notices = QString::fromUtf8(f.readAll());
                if (path.endsWith(QLatin1String("LGPL-3.0.txt"))) lgpl = QString::fromUtf8(f.readAll());
            }
            check(lgpl.contains(QLatin1String("GNU LESSER GENERAL PUBLIC LICENSE")),
                  QStringLiteral("about: the LGPL text is the LGPL"));
            for (const char *need : {"### Qt 6", "LGPLv3 section 3", "About Qt", "### SQLite",
                                     "ntc-templates", "Apache-2.0 section 4(d)"})
                check(notices.contains(QLatin1String(need)),
                      QStringLiteral("about: the notices cover \u201C%1\u201D").arg(QLatin1String(need)));

            // The compiled-in notices against go.mod: every required module at
            // the version go.mod pins. A dependency bump without a regeneration
            // fails here instead of shipping a notices file that names the old
            // version -- or not the module at all.
            QFile gomod(QStringLiteral(OMEGACAT_SOURCE_DIR "/go.mod"));
            int required = 0;
            QStringList stale;
            if (gomod.open(QIODevice::ReadOnly)) {
                bool inRequire = false;
                static const QRegularExpression line(QStringLiteral("^\\s*(?:require\\s+)?(\\S+)\\s+(v\\S+)"));
                for (const QString &raw : QString::fromUtf8(gomod.readAll()).split(QLatin1Char('\n'))) {
                    const QString l = raw.trimmed();
                    if (l.startsWith(QLatin1String("require ("))) { inRequire = true; continue; }
                    if (inRequire && l == QLatin1String(")")) { inRequire = false; continue; }
                    if (!inRequire && !l.startsWith(QLatin1String("require "))) continue;
                    const auto m = line.match(l);
                    if (!m.hasMatch()) continue;
                    ++required;
                    const QString want = QStringLiteral("### %1\n\nVersion %2.").arg(m.captured(1), m.captured(2));
                    if (!notices.contains(want)) stale << m.captured(1) + QLatin1Char(' ') + m.captured(2);
                }
            }
            check(required > 0 && stale.isEmpty(),
                  QStringLiteral("about: notices name all %1 go.mod modules at their versions%2")
                      .arg(required)
                      .arg(stale.isEmpty() ? QString() : QStringLiteral(" -- stale: ") + stale.join(QStringLiteral(", "))));

            for (ThemeId id : {ThemeId::Light, ThemeId::Dark, ThemeId::Cyber}) {
                ThemeManager::instance().setTheme(id);
                QApplication::processEvents();
                check(about->grab().save(QStringLiteral("%1-about-%2.png").arg(prefix, themeKey(id))),
                      QStringLiteral("about: grab %1").arg(themeKey(id)));
            }

            const int closedHeight = about->height();
            about->setAttributionsVisible(true);
            QApplication::processEvents();
            check(about->attributionsVisible() && about->height() >= closedHeight + 250,
                  QStringLiteral("about: Attributions opens and the box grows (%1 -> %2)")
                      .arg(closedHeight).arg(about->height()));
            const QString shown = about->browser()->toPlainText();
            check(shown.contains(QLatin1String("Third-party notices")) && shown.contains(QLatin1String("golang.org/x/crypto")),
                  QStringLiteral("about: the notices render in the section"));
            for (ThemeId id : {ThemeId::Light, ThemeId::Dark, ThemeId::Cyber}) {
                ThemeManager::instance().setTheme(id);
                QApplication::processEvents();
                const QImage img = about->browser()->grab().toImage();
                const QColor body = img.pixelColor(img.width() / 2, img.height() - 4);
                const QColor want = tokensFor(id).bgInput;
                check(qAbs(body.red() - want.red()) + qAbs(body.green() - want.green()) + qAbs(body.blue() - want.blue()) < 24,
                      QStringLiteral("about: %1: the notices well is the theme's input colour (%2 vs %3)")
                          .arg(themeKey(id), body.name(), want.name()));
                about->grab().save(QStringLiteral("%1-attributions-%2.png").arg(prefix, themeKey(id)));
            }
            ThemeManager::instance().setTheme(ThemeId::Light);
            about->showDocument(2);
            check(about->browser()->toPlainText().contains(QLatin1String("GNU LESSER GENERAL PUBLIC LICENSE")),
                  QStringLiteral("about: the LGPL text is selectable in the section"));
            about->setAttributionsVisible(false);
            QApplication::processEvents();
            check(!about->attributionsVisible() && about->height() == closedHeight,
                  QStringLiteral("about: closing Attributions shrinks the box back (%1)").arg(about->height()));
            about->close();
            QApplication::processEvents();
        }

        // Help: About with the macOS About role, Attributions opening the
        // section directly, and About Qt with its role.
        QAction *aboutAct = nullptr, *attrAct = nullptr, *qtAct = nullptr;
        for (QAction *m : w.menuBar()->actions()) {
            if (!m->menu()) continue;
            for (QAction *a : m->menu()->actions()) {
                const QString t = a->text();
                if (t == QLatin1String("About OmegaCat")) aboutAct = a;
                if (t == QLatin1String("Attributions")) attrAct = a;
                if (t == QLatin1String("About Qt")) qtAct = a;
            }
        }
        check(aboutAct && aboutAct->menuRole() == QAction::AboutRole, QStringLiteral("help: About OmegaCat, About role"));
        check(qtAct && qtAct->menuRole() == QAction::AboutQtRole, QStringLiteral("help: About Qt, About Qt role"));
        check(attrAct != nullptr, QStringLiteral("help: Attributions"));
        if (attrAct) {
            attrAct->trigger();
            QApplication::processEvents();
            AboutDialog *d = openAbout();
            check(d && d->attributionsVisible(), QStringLiteral("help: Attributions opens the box with the section open"));
            if (d) d->close();
            QApplication::processEvents();
        }
    }

    if (!live) {
        std::printf("SKIP  store and search: no fakedevice and template database given\n");
        std::printf("\n%d failure(s)\n", failures);
        return failures;
    }

    // ---- A live capture, through the window ----------------------------
    QProcess lab;
    lab.start(QString::fromLocal8Bit(argv[2]), {});
    check(lab.waitForStarted(5000) && lab.waitForReadyRead(10000), QStringLiteral("fakedevice starts"));
    const QJsonObject ready = QJsonDocument::fromJson(lab.readLine()).object();
    QStringList addrs;
    for (const QJsonValue &d : ready.value(QLatin1String("devices")).toArray())
        addrs.append(d.toObject().value(QLatin1String("addr")).toString());
    check(addrs.size() == 2, QStringLiteral("two fake devices"));

    Vault *v = w.vault();
    check(v->create(QStringLiteral("lab-master-passphrase")) == VaultError::Ok, QStringLiteral("vault created"));
    CredentialInput cred;
    cred.name = QStringLiteral("lab");
    cred.username = ready.value(QLatin1String("user")).toString();
    cred.auth = AuthMethod::Password;
    cred.password = ready.value(QLatin1String("password")).toString();
    check(v->store(cred) == VaultError::Ok, QStringLiteral("credential stored"));

    // Edit through the credential editor the manager uses, changing only
    // metadata. The manager saves that with updateMetadata; the capture below
    // selects on the new tag, so it authenticates only if the edit kept the
    // stored password -- the loss vault.h warns an editing form can cause.
    {
        QVector<CredentialMeta> metas;
        check(v->list(&metas) == VaultError::Ok && metas.size() == 1, QStringLiteral("vault lists the credential"));
        CredentialEditorDialog editor(metas.first(), &w);
        check(!editor.material(), QStringLiteral("an untouched editor carries no credential material"));
        CredentialMeta edited = editor.metadata();
        edited.tags = QStringList{QStringLiteral("lab"), QStringLiteral("edited")};
        check(v->updateMetadata(edited) == VaultError::Ok, QStringLiteral("metadata edit saved"));
        QVector<CredentialMeta> after;
        v->list(&after);
        check(after.size() == 1 && after.first().hasSecret && after.first().tags.contains(QStringLiteral("edited")),
              QStringLiteral("after the edit: tags changed, secret still present"));
    }

    // The credential manager and the unlock dialog, for the eye.
    {
        CredentialManagerDialog mgr(v, &w);
        mgr.show();
        QApplication::processEvents();
        const auto *table = mgr.findChild<QTableWidget *>();
        check(table && table->rowCount() == 1, QStringLiteral("credential manager lists the credential"));
        const auto button = [&mgr](const QString &prefixText) -> QPushButton * {
            for (QPushButton *b : mgr.findChildren<QPushButton *>())
                if (b->text().startsWith(prefixText)) return b;
            return nullptr;
        };
        for (ThemeId id : {ThemeId::Light, ThemeId::Dark}) {
            ThemeManager::instance().setTheme(id);
            QApplication::processEvents();
            QPushButton *unlockBtn = button(QStringLiteral("Unlock"));
            QPushButton *addBtn = button(QStringLiteral("Add"));
            check(unlockBtn && addBtn && !unlockBtn->isEnabled() && addBtn->isEnabled(),
                  QStringLiteral("%1: an unlocked vault offers Add, not Unlock").arg(themeKey(id)));
            // The body is a scroll area's widget, which fills with the
            // palette's Window colour: it has to be the theme's, not Fusion's.
            const QImage img = mgr.grab().toImage();
            const QColor body = img.pixelColor(8, img.height() / 2);  // body margin, beside the table
            const QColor want = tokensFor(id).bgPrimary;
            check(qAbs(body.lightness() - want.lightness()) < 40,
                  QStringLiteral("%1: dialog body is the theme's surface (%2 vs %3)")
                      .arg(themeKey(id), body.name(), want.name()));
            img.save(QStringLiteral("%1-credentials-%2.png").arg(prefix, themeKey(id)));
        }
        ThemeManager::instance().setTheme(ThemeId::Light);
        mgr.close();
    }
    {
        v->lock();
        VaultUnlockDialog unlockDlg(v, &w);
        unlockDlg.show();
        QApplication::processEvents();
        unlockDlg.grab().save(QStringLiteral("%1-unlock-light.png").arg(prefix));
        unlockDlg.close();
        check(v->unlock(QStringLiteral("lab-master-passphrase")) == VaultError::Ok, QStringLiteral("vault unlocks again"));
    }

    QJsonObject req = QJsonDocument::fromJson(RunBridge::defaults()).object();
    req.insert(QStringLiteral("devices"), QJsonArray::fromStringList(addrs));
    req.insert(QStringLiteral("types"), QJsonArray::fromStringList({QStringLiteral("running-config"), QStringLiteral("arp-table")}));
    req.insert(QStringLiteral("store_path"), tmp.filePath(QStringLiteral("store")));
    req.insert(QStringLiteral("host_keys"), QStringLiteral("tofu"));
    req.insert(QStringLiteral("known_hosts_path"), tmp.filePath(QStringLiteral("known_hosts")));
    req.insert(QStringLiteral("templates_path"), QString::fromLocal8Bit(argv[3]));
    req.insert(QStringLiteral("cred_tags"), QJsonArray::fromStringList({QStringLiteral("edited")}));
    check(w.startCapture(QJsonDocument(req).toJson(QJsonDocument::Compact)), QStringLiteral("capture starts"));
    check(waitFor([&] { return w.bridge()->isFinished(); }, 60000), QStringLiteral("capture finishes"));
    QApplication::processEvents();
    const RunResult result = w.bridge()->result();
    check(result.state == QLatin1String("done"), QStringLiteral("capture done (%1 %2)").arg(result.state, result.error));
    check(w.bridge()->progress().counts.failed == 0 && w.bridge()->progress().counts.credRejections == 0,
          QStringLiteral("capture with the edited credential authenticated everywhere"));

    int parsedRows = 0;
    for (int i = 0; i < model->rowCount(); ++i)
        if (model->rowAt(i).parseStatus == QLatin1String("parsed")) ++parsedRows;
    check(parsedRows == 1, QStringLiteral("the IOS ARP row shows as parsed (%1)").arg(parsedRows));

    // ---- Store ----------------------------------------------------------
    StoreView *store = w.storeView();
    w.showView(MainWindow::StoreTab);
    store->refresh();
    const QTreeWidget *devs = store->devices();
    QStringList names;
    for (int i = 0; i < devs->topLevelItemCount(); ++i) names.append(devs->topLevelItem(i)->text(0));
    check(names.contains(QStringLiteral("lab-r1")) && names.contains(QStringLiteral("lab-spine-1")),
          QStringLiteral("store lists both devices (%1)").arg(names.join(QStringLiteral(", "))));
    check(store->openFile(QStringLiteral("lab-r1"), QStringLiteral("arp-table"), QString()),
          QStringLiteral("lab-r1 arp-table opens"));
    check(store->content()->toPlainText().contains(QStringLiteral("0c1d.5e2f.0101")),
          QStringLiteral("raw ARP table is shown"));
    check(store->parsedButton()->isEnabled(), QStringLiteral("parsed view is offered"));
    store->showParsed(true);
    check(store->parsedTable()->rowCount() == 3 && store->parsedTable()->columnCount() > 3,
          QStringLiteral("parsed table: %1 rows x %2 columns").arg(store->parsedTable()->rowCount())
              .arg(store->parsedTable()->columnCount()));
    grabAll(w, prefix, MainWindow::StoreTab, QStringLiteral("store"));

    // ---- Store: diff ----------------------------------------------------
    // The fake device's config never changes, so the second version is
    // written into the store the way Put would have: a new file and a
    // history line naming it.
    {
        check(!store->diffButton()->isEnabled(), QStringLiteral("diff: not offered for arp-table"));
        check(store->openFile(QStringLiteral("lab-r1"), QStringLiteral("running-config"), QString()),
              QStringLiteral("diff: lab-r1 running-config opens"));
        check(!store->diffButton()->isEnabled(), QStringLiteral("diff: not offered with one stored version"));

        const QString typeDir = tmp.filePath(QStringLiteral("store/devices/lab-r1/running-config"));
        const QString firstFile = store->currentFile();
        QFile first(typeDir + QLatin1Char('/') + firstFile);
        const QByteArray older = first.open(QIODevice::ReadOnly) ? first.readAll() : QByteArray();
        // Stored captures carry no trailing newline; appending straight on
        // would change the last line rather than add one.
        // One real change, and one line the probe's ignore rule covers.
        const QByteArray newer = older + (older.endsWith('\n') ? "" : "\n") +
                                 "ntp server 172.16.0.99\n! probe generated at 12:00\n";
        const QString newFile = QStringLiteral("2099-01-01T00-00-00Z.txt");
        QFile out(typeDir + QLatin1Char('/') + newFile);
        QFile hist(typeDir + QStringLiteral("/history.jsonl"));
        const QByteArray line = QJsonDocument(QJsonObject{{QStringLiteral("at"), QStringLiteral("2099-01-01T00:00:00Z")},
                                                          {QStringLiteral("command"), QStringLiteral("show running-config")},
                                                          {QStringLiteral("sha256"), QStringLiteral("probe")},
                                                          {QStringLiteral("bytes"), int(newer.size())},
                                                          {QStringLiteral("file"), newFile}})
                                    .toJson(QJsonDocument::Compact) + "\n";
        check(!older.isEmpty() && out.open(QIODevice::WriteOnly) && out.write(newer) == newer.size() &&
                  hist.open(QIODevice::Append) && hist.write(line) == line.size(),
              QStringLiteral("diff: second version written"));
        out.close();
        hist.close();

        check(store->openFile(QStringLiteral("lab-r1"), QStringLiteral("running-config"), newFile) &&
                  store->diffButton()->isEnabled(),
              QStringLiteral("diff: offered once there are two versions"));
        QString title;
        for (const QLabel *l : store->findChildren<QLabel *>())
            if (l->isVisible() && l->text().startsWith(QStringLiteral("lab-r1 / running-config"))) title = l->text();
        check(title.contains(newFile), QStringLiteral("store: openFile titles the panel with the file it opened"));
        store->showMode(StoreView::Mode::Diff);
        QApplication::processEvents();
        const QString info = store->diffInfo()->text();
        const QString body = store->diffView()->toPlainText();
        check(info.contains(QStringLiteral("+1")) && info.contains(QStringLiteral("\u22120")) &&
                  info.contains(QStringLiteral("1 ignored")) && info.contains(firstFile) &&
                  body.contains(QStringLiteral("+  ntp server 172.16.0.99")) &&
                  body.contains(QStringLiteral("+  ! probe generated at 12:00")),
              QStringLiteral("diff: +1 -0 with the rule-covered line shown but not counted (%1)").arg(info));
        check(store->diffInfo()->toolTip().contains(rulesPath),
              QStringLiteral("diff: the rules file the probe configured is the one used"));
        check(!store->diffView()->extraSelections().isEmpty(), QStringLiteral("diff: changed lines are marked"));
        grabAll(w, prefix, MainWindow::StoreTab, QStringLiteral("diff"));
        store->refresh();
        QApplication::processEvents();
        check(store->diffView()->toPlainText().isEmpty() && !store->diffButton()->isChecked(),
              QStringLiteral("diff: a refresh clears the diff instead of leaving it under a reset title"));
        check(store->openFile(QStringLiteral("lab-r1"), QStringLiteral("running-config"), newFile),
              QStringLiteral("diff: reopened after refresh"));

        // Two versions selected in History: straight to that diff.
        store->showMode(StoreView::Mode::Raw);
        QTableWidget *ht = store->historyTable();
        ht->clearSelection();
        for (int r = 0; r < 2 && r < ht->rowCount(); ++r)
            ht->selectionModel()->select(ht->model()->index(r, 0), QItemSelectionModel::Select | QItemSelectionModel::Rows);
        QApplication::processEvents();
        check(store->diffButton()->isChecked() && store->diffInfo()->text() == info,
              QStringLiteral("diff: selecting two history rows shows their diff (%1)").arg(store->diffInfo()->text()));
        store->showMode(StoreView::Mode::Raw);
    }

    // ---- Search ---------------------------------------------------------
    SearchView *search = w.searchView();
    w.showView(MainWindow::Search);
    bool searched = false;
    QObject::connect(search, &SearchView::searchFinished, [&] { searched = true; });
    search->runQuery(QStringLiteral("hostname"));
    check(waitFor([&] { return searched; }, 20000), QStringLiteral("search finishes"));
    QSet<QString> hitDevices;
    for (int i = 0; i < search->hits()->rowCount(); ++i) hitDevices.insert(search->hits()->item(i, 0)->text());
    check(hitDevices.contains(QStringLiteral("lab-r1")) && hitDevices.contains(QStringLiteral("lab-spine-1")),
          QStringLiteral("hits in both devices (%1 hits)").arg(search->hits()->rowCount()));
    check(search->content()->toPlainText().contains(QStringLiteral("hostname")),
          QStringLiteral("first hit shown in its file"));
    grabAll(w, prefix, MainWindow::Search, QStringLiteral("search"));

    // ---- Run: a capture from the form ----------------------------------
    // Everything above builds its request in code. This one goes through the
    // widgets and the button, into a store typed into the form's field that
    // the window is not open on. The Store view already lists both devices
    // from the first capture, so "lists both devices" alone would pass with
    // the views left on the old store: the store the window is open on is
    // checked by path, and the device list after the run is not refreshed by
    // hand -- the window has to do it.
    {
        RunView *rv = w.runView();
        w.showView(MainWindow::Run);
        const QString typedStore = tmp.filePath(QStringLiteral("typed/store"));
        check(!QFileInfo::exists(typedStore) && w.storePath() != typedStore,
              QStringLiteral("form: the typed store is new and not the open one"));

        // The only fields not from the widgets: known_hosts and the template
        // database, so the probe never writes to a real ~/.omegacat.
        QJsonObject extras;
        extras.insert(QStringLiteral("known_hosts_path"), tmp.filePath(QStringLiteral("known_hosts")));
        extras.insert(QStringLiteral("templates_path"), QString::fromLocal8Bit(argv[3]));
        rv->setRequestExtras(extras);

        // Pasted the way a list arrives: a comment, a blank line, a trailing one.
        rv->devicesEdit()->setPlainText(
            QStringLiteral("# lab pair\n%1\n\n%2   # spine\n").arg(addrs.value(0), addrs.value(1)));
        QListWidget *types = rv->typesList();
        for (int i = 0; i < types->count(); ++i) {
            const QString t = types->item(i)->text();
            types->item(i)->setCheckState(
                t == QLatin1String("running-config") || t == QLatin1String("arp-table") ? Qt::Checked : Qt::Unchecked);
        }
        rv->storeEdit()->setText(QDir::toNativeSeparators(typedStore));
        // Keys were learned by the first capture: strict has to accept them.
        rv->hostKeysBox()->setCurrentIndex(rv->hostKeysBox()->findData(QStringLiteral("strict")));
        rv->credTagsEdit()->setText(QStringLiteral("edited"));

        const QJsonObject formReq = QJsonDocument::fromJson(rv->request()).object();
        check(formReq.value(QLatin1String("types")).toArray().size() == 2 &&
                  formReq.value(QLatin1String("host_keys")).toString() == QLatin1String("strict") &&
                  formReq.value(QLatin1String("store_path")).toString() == typedStore,
              QStringLiteral("form: request carries the ticked types, strict keys and the typed store"));

        const QString oldStore = w.storePath();
        rv->startButton()->click();
        check(waitFor([&] { return w.bridge()->isFinished(); }, 60000), QStringLiteral("form: capture finishes"));
        QApplication::processEvents();
        const RunResult fr = w.bridge()->result();
        const RunCounts fc = w.bridge()->progress().counts;
        check(fr.state == QLatin1String("done"), QStringLiteral("form: capture done (%1 %2)").arg(fr.state, fr.error));
        // IOS: config + ARP stored. EOS: config stored, `show ip arp` refused.
        check(fc.stored == 3 && fc.notApplicable == 1 && fc.failed == 0 && fc.credRejections == 0,
              QStringLiteral("form: 3 stored, 1 not applicable, none failed (%1/%2/%3, %4 auth rejects)")
                  .arg(fc.stored).arg(fc.notApplicable).arg(fc.failed).arg(fc.credRejections));

        check(QDir::cleanPath(w.storePath()) == QDir::cleanPath(typedStore),
              QStringLiteral("form: the window follows the typed store (open on %1, was %2)")
                  .arg(w.storePath(), oldStore));
        check(QFileInfo::exists(typedStore + QStringLiteral("/devices/lab-r1/arp-table")),
              QStringLiteral("form: captures landed in the typed store"));

        w.showView(MainWindow::StoreTab);
        QApplication::processEvents();
        const QTreeWidget *typedDevs = store->devices();
        QStringList typedNames;
        for (int i = 0; i < typedDevs->topLevelItemCount(); ++i) typedNames.append(typedDevs->topLevelItem(i)->text(0));
        check(typedNames.contains(QStringLiteral("lab-r1")) && typedNames.contains(QStringLiteral("lab-spine-1")),
              QStringLiteral("form: Store view lists the typed store's devices (%1)").arg(typedNames.join(QStringLiteral(", "))));
        rv->setRequestExtras({});
    }

    // ---- Run: the inventory, from an omegamaps map ---------------------
    {
        RunView *rv = w.runView();
        const QString invPath = w.inventoryPath();
        const auto writeFile = [](const QString &path, const QByteArray &body, bool append) {
            QDir().mkpath(QFileInfo(path).absolutePath());
            QFile f(path);
            return f.open(append ? QIODevice::Append : QIODevice::WriteOnly) && f.write(body) == body.size();
        };

        // Three crawled devices and a leaf, shaped like omegamaps' output.
        const QString mapPath = tmp.filePath(QStringLiteral("crawl-lab/map.json"));
        check(writeFile(mapPath, R"({
  "eng-spine-1": {"node_details": {"ip": "172.16.2.5", "platform": "arista_eos"},
    "peers": {"eng-leaf-1": {"ip": "172.16.11.41", "platform": "cisco_ios", "connections": [["Eth3","Gi0/0"]]},
              "lab-server": {"ip": "172.16.99.9", "platform": "linux", "connections": [["Eth9","eth0"]]}}},
  "eng-leaf-1": {"node_details": {"ip": "172.16.11.41", "platform": "cisco_ios"},
    "peers": {"eng-spine-1": {"ip": "172.16.2.5", "platform": "arista_eos", "connections": [["Gi0/0","Eth3"]]}}},
  "eng-leaf-2": {"node_details": {"ip": "172.16.11.42", "platform": "cisco_ios"}, "peers": {}}
})", false),
              QStringLiteral("inventory: map.json written"));

        InventoryImport imp;
        QString err;
        const bool imported = w.importMap(mapPath, &imp, &err);  // not inside check(): argument order is unspecified
        check(imported, QStringLiteral("inventory: map imports (%1)").arg(err));
        check(imp.folder == QLatin1String("crawl-lab") && imp.created && imp.added == 3 && imp.skipped == 0,
              QStringLiteral("inventory: folder named from the crawl directory, 3 added (%1, %2 added, %3 skipped)")
                  .arg(imp.folder).arg(imp.added).arg(imp.skipped));
        check(QFileInfo::exists(invPath), QStringLiteral("inventory: file written"));
        check(rv->source() == RunView::Source::Inventory, QStringLiteral("inventory: the Run view switched to it"));

        QTreeWidget *tree = rv->inventoryTree();
        QStringList names;
        QString spinePlatform;
        if (tree->topLevelItemCount() == 1)
            for (int i = 0; i < tree->topLevelItem(0)->childCount(); ++i) {
                const QTreeWidgetItem *it = tree->topLevelItem(0)->child(i);
                names.append(it->text(0));
                if (it->text(0) == QLatin1String("eng-spine-1") && it->toolTip(0).contains(QStringLiteral("arista_eos")))
                    spinePlatform = QStringLiteral("arista_eos");
            }
        check(tree->topLevelItemCount() == 1 && names.size() == 3 && !names.contains(QStringLiteral("lab-server")),
              QStringLiteral("inventory: one folder, the 3 crawled devices, no leaf (%1)").arg(names.join(QStringLiteral(", "))));
        check(spinePlatform == QLatin1String("arista_eos"), QStringLiteral("inventory: platform in the tooltip (%1)").arg(spinePlatform));

        InventoryImport again;
        const bool reimported = w.importMap(mapPath, &again, &err);
        check(reimported && again.added == 0 && again.skipped == 3 &&
                  tree->topLevelItem(0)->childCount() == 3,
              QStringLiteral("inventory: re-import adds nothing (%1 added, %2 skipped)").arg(again.added).arg(again.skipped));

        // Start with nothing ticked is refused in the form, before any run.
        const quint64 seqBefore = w.bridge()->progress().seq;
        rv->startButton()->click();
        QApplication::processEvents();
        bool refusalShown = false;
        for (const QLabel *l : rv->findChildren<QLabel *>())
            if (l->isVisible() && l->text().contains(QStringLiteral("Tick at least one device"))) refusalShown = true;
        check(refusalShown && w.bridge()->progress().seq == seqBefore,
              QStringLiteral("inventory: Start with nothing ticked is refused in the form"));

        // Both fake devices behind 127.0.0.1, told apart only by port.
        const QString a0 = addrs.value(0), a1 = addrs.value(1);
        const QString port0 = a0.section(QLatin1Char(':'), -1), port1 = a1.section(QLatin1Char(':'), -1);
        check(writeFile(invPath, QStringLiteral("    - folder_name: Forwarded\n"
                                                "      sessions:\n"
                                                "        - name: lab-r1\n"
                                                "          transport: ssh\n"
                                                "          host: 127.0.0.1\n"
                                                "          port: %1\n"
                                                "        - name: lab-spine-1\n"
                                                "          transport: ssh\n"
                                                "          host: 127.0.0.1\n"
                                                "          port: %2\n").arg(port0, port1).toUtf8(), true),
              QStringLiteral("inventory: forwarded folder added to the file"));
        check(w.reloadInventory() && tree->topLevelItemCount() == 2, QStringLiteral("inventory: reload shows 2 folders"));

        // Which fake device answers on which port is the fake's business; tick
        // the second one by its key and expect that name back.
        const QString wantName = ready.value(QLatin1String("devices")).toArray().at(1).toObject()
                                     .value(QLatin1String("name")).toString();
        const QString key = QStringLiteral("ssh:127.0.0.1:%1").arg(port1);
        check(rv->setSessionChecked(key, true), QStringLiteral("inventory: %1 ticked by key").arg(key));
        check(rv->checkedKeys() == QStringList{key}, QStringLiteral("inventory: exactly one key ticked"));

        QJsonObject extras;
        extras.insert(QStringLiteral("known_hosts_path"), tmp.filePath(QStringLiteral("known_hosts")));
        extras.insert(QStringLiteral("templates_path"), QString::fromLocal8Bit(argv[3]));
        rv->setRequestExtras(extras);
        QListWidget *types = rv->typesList();
        for (int i = 0; i < types->count(); ++i)
            types->item(i)->setCheckState(types->item(i)->text() == QLatin1String("running-config") ? Qt::Checked
                                                                                                   : Qt::Unchecked);
        const QJsonObject invReq = QJsonDocument::fromJson(rv->request()).object();
        check(invReq.value(QLatin1String("session_file")).toString() == invPath &&
                  invReq.value(QLatin1String("session_keys")).toArray() == QJsonArray{key} &&
                  !invReq.contains(QLatin1String("match")),
              QStringLiteral("inventory: request carries the inventory and the one key"));

        rv->startButton()->click();
        check(waitFor([&] { return w.bridge()->isFinished(); }, 60000), QStringLiteral("inventory: capture finishes"));
        QApplication::processEvents();
        const RunCounts ic = w.bridge()->progress().counts;
        QSet<QString> devicesSeen;
        for (int i = 0; i < model->rowCount(); ++i) devicesSeen.insert(model->rowAt(i).name);
        check(w.bridge()->result().state == QLatin1String("done") && ic.devices == 1 && ic.failed == 0 &&
                  ic.stored + ic.unchanged == 1,
              QStringLiteral("inventory: one device, one config, nothing failed (%1 devices, %2 failed)")
                  .arg(ic.devices).arg(ic.failed));
        check(devicesSeen == QSet<QString>{wantName},
              QStringLiteral("inventory: only the ticked device was captured (%1)")
                  .arg(QStringList(devicesSeen.values()).join(QStringLiteral(", "))));
        grabAll(w, prefix, MainWindow::Run, QStringLiteral("inventory"));

        // The tree takes the panel's spare height. At 860 px the form has none,
        // so what is checked is where added height goes: a stretch above Start
        // used to take all of it.
        w.showView(MainWindow::Run);
        QApplication::processEvents();
        const int treeShort = tree->height();
        const QSize sizeWas = w.size();
        w.resize(sizeWas.width(), sizeWas.height() + 300);
        QApplication::processEvents();
        const int treeTall = tree->height();
        w.resize(sizeWas);
        QApplication::processEvents();
        check(treeTall - treeShort >= 250,
              QStringLiteral("inventory: 300 px more window gives the tree most of it (%1 -> %2 px)").arg(treeShort).arg(treeTall));

        // ---- a filtered folder ticks only what the filter shows ----
        // Folder boxes are set here the way a click sets them (setCheckState
        // raises itemChanged exactly as the view's click does).
        QTreeWidgetItem *crawl = nullptr;
        for (int i = 0; i < tree->topLevelItemCount(); ++i)
            if (tree->topLevelItem(i)->text(0) == QLatin1String("crawl-lab")) crawl = tree->topLevelItem(i);
        const QString spineKey = QStringLiteral("ssh:172.16.2.5:22");
        const auto hint = [&] {
            for (const QLabel *l : rv->findChildren<QLabel *>())
                if (l->isVisible() && l->text().contains(QStringLiteral("selected"))) return l->text();
            return QString();
        };
        const auto keySet = [&] {
            const QStringList k = rv->checkedKeys();
            return QSet<QString>(k.cbegin(), k.cend());
        };
        check(crawl != nullptr, QStringLiteral("filter: crawl-lab folder present"));
        if (crawl) {
            rv->inventoryFilter()->setText(QStringLiteral("spine"));
            QApplication::processEvents();
            crawl->setCheckState(0, Qt::Checked);
            check(keySet() == QSet<QString>{key, spineKey},
                  QStringLiteral("filter: ticking a filtered folder ticks only its visible device (%1)")
                      .arg(rv->checkedKeys().join(QStringLiteral(", "))));

            rv->inventoryFilter()->clear();
            QApplication::processEvents();
            check(crawl->checkState(0) == Qt::PartiallyChecked,
                  QStringLiteral("filter: unfiltered, the folder shows partly ticked"));

            rv->inventoryFilter()->setText(QStringLiteral("leaf"));
            QApplication::processEvents();
            check(hint().contains(QStringLiteral("2 hidden")),
                  QStringLiteral("filter: the count names ticks the filter hides (%1)").arg(hint()));
            check(crawl->checkState(0) == Qt::Unchecked,
                  QStringLiteral("filter: the folder box reflects its visible devices"));

            crawl->setCheckState(0, Qt::Checked);
            check(keySet().size() == 4, QStringLiteral("filter: ticking again adds the 2 visible leaves (%1 ticked)")
                                            .arg(keySet().size()));
            crawl->setCheckState(0, Qt::Unchecked);
            check(keySet() == QSet<QString>{key, spineKey},
                  QStringLiteral("filter: unticking a filtered folder leaves hidden ticks alone (%1)")
                      .arg(rv->checkedKeys().join(QStringLiteral(", "))));

            rv->inventoryNoneButton()->click();
            QApplication::processEvents();
            check(rv->checkedKeys().isEmpty() && !hint().contains(QStringLiteral("hidden")),
                  QStringLiteral("filter: None unticks everything, hidden included (%1)").arg(hint()));

            rv->inventoryFilter()->clear();
            rv->setSessionChecked(key, true);
            QApplication::processEvents();
        }

        // ---- the inventory manager ----
        {
            const auto fileText = [&] {
                QFile f(invPath);
                return f.open(QIODevice::ReadOnly) ? QString::fromUtf8(f.readAll()) : QString();
            };
            const QString leaf1 = QStringLiteral("ssh:172.16.11.41:22");
            const QString leaf1Moved = QStringLiteral("ssh:172.16.11.141:22");
            const QString leaf2 = QStringLiteral("ssh:172.16.11.42:22");
            rv->setSessionChecked(leaf1, true);

            InventoryManagerDialog dlg(invPath, w.vault(), key, &w);
            dlg.show();
            QApplication::processEvents();
            check(dlg.selectedKeys() == QStringList{key} && !dlg.nameEdit()->text().isEmpty() &&
                      dlg.platformBox()->currentData().toString().isEmpty(),
                  QStringLiteral("manager: opens on the device it was asked for, platform (detect)"));
            check(dlg.credentialBox()->findText(QStringLiteral("lab")) >= 0,
                  QStringLiteral("manager: vault credentials offered by name"));

            // One device: a new address.
            dlg.selectKeys({leaf1});
            dlg.hostEdit()->setText(QStringLiteral("172.16.11.141"));
            dlg.save();
            QApplication::processEvents();
            check(!dlg.notice()->isVisible() && dlg.selectedKeys() == QStringList{leaf1Moved} &&
                      dlg.keyChanges().value(leaf1) == leaf1Moved,
                  QStringLiteral("manager: host edit saved, reselected under its new key (%1)").arg(dlg.notice()->text()));

            // Several devices: legacy and a credential, platform untouched.
            dlg.selectKeys({leaf1Moved, leaf2});
            check(dlg.platformBox()->currentData().toString().startsWith(QLatin1Char('\x01')) &&
                      dlg.legacyBox()->checkState() == Qt::PartiallyChecked && !dlg.hostEdit()->isEnabled(),
                  QStringLiteral("manager: a multi-selection starts unchanged, per-device fields disabled"));
            dlg.legacyBox()->setCheckState(Qt::Checked);
            dlg.credentialBox()->setEditText(QStringLiteral("lab-tacacs"));
            dlg.save();
            QApplication::processEvents();
            const QString afterBulk = fileText();
            check(!dlg.notice()->isVisible() && afterBulk.count(QStringLiteral("credential: lab-tacacs")) == 2 &&
                      afterBulk.count(QStringLiteral("legacy_algorithms: true")) == 2 &&
                      !afterBulk.contains(QStringLiteral("platform: ")),
                  QStringLiteral("manager: bulk edit wrote legacy and credential to both, no platform (%1)").arg(dlg.notice()->text()));

            // A clash is refused inline and writes nothing.
            dlg.selectKeys({leaf2});
            dlg.hostEdit()->setText(QStringLiteral("172.16.2.5"));
            dlg.save();
            QApplication::processEvents();
            check(dlg.notice()->isVisible() && dlg.notice()->text().contains(QStringLiteral("already at")) &&
                      fileText() == afterBulk,
                  QStringLiteral("manager: an occupied address is refused, file untouched (%1)").arg(dlg.notice()->text()));

            // Add a device.
            dlg.beginAdd(QStringLiteral("crawl-lab"));
            dlg.nameEdit()->setText(QStringLiteral("eng-leaf-3"));
            dlg.hostEdit()->setText(QStringLiteral("172.16.11.43"));
            dlg.save();
            QApplication::processEvents();
            check(dlg.selectedKeys() == QStringList{QStringLiteral("ssh:172.16.11.43:22")},
                  QStringLiteral("manager: added device is selected (%1)").arg(dlg.notice()->text()));

            // Set the forwarded spine's platform to one it is not.
            dlg.selectKeys({key});
            dlg.platformBox()->setCurrentIndex(dlg.platformBox()->findData(QStringLiteral("cisco_ios")));
            dlg.save();
            QApplication::processEvents();
            check(fileText().contains(QStringLiteral("platform: cisco_ios")), QStringLiteral("manager: platform saved"));
            dlg.grab().save(prefix + QStringLiteral("-manager-light.png"));
            dlg.close();

            w.reloadInventory(dlg.keyChanges());
            QApplication::processEvents();
            const QStringList ticks = rv->checkedKeys();
            check(ticks.contains(leaf1Moved) && !ticks.contains(leaf1) && ticks.contains(key),
                  QStringLiteral("manager: the Run view's tick followed the host edit (%1)").arg(ticks.join(QStringLiteral(", "))));

            // Capture only the spine, as the platform it was set to.
            rv->setSessionChecked(leaf1Moved, false);
            const qint64 decisionsBefore = rv->decisions()->count();
            rv->startButton()->click();
            check(waitFor([&] { return w.bridge()->isFinished(); }, 60000), QStringLiteral("manager: capture finishes"));
            QApplication::processEvents();
            QString spinePlat;
            for (int i = 0; i < model->rowCount(); ++i) spinePlat = model->rowAt(i).platform;
            bool said = false;
            for (int i = 0; i < rv->decisions()->count(); ++i)
                said = said || rv->decisions()->item(i)->text().contains(QStringLiteral("platform set to cisco_ios; the device reports arista_eos"));
            check(w.bridge()->progress().counts.failed == 0 && spinePlat == QLatin1String("cisco_ios") && said,
                  QStringLiteral("manager: captured as the set platform, disagreement in decisions (platform %1, %2 decisions before)")
                      .arg(spinePlat).arg(decisionsBefore));
        }

        const bool removed = w.removeInventoryFolder(QStringLiteral("crawl-lab"), &err);
        check(removed && tree->topLevelItemCount() == 1,
              QStringLiteral("inventory: folder removed (%1)").arg(err));
        check(rv->checkedKeys() == QStringList{key}, QStringLiteral("inventory: the tick survived the reload"));

        rv->setRequestExtras({});
        rv->setSource(RunView::Source::List);
    }


    // ---- Template Lab ---------------------------------------------------
    // On a copy of the shipped template database, never ~/.omegacat's.
    if (live) {
        TemplateLabView *tlab = w.templateLab();
        const QString labDb = tmp.filePath(QStringLiteral("lab-templates.db"));
        check(QFile::copy(QString::fromLocal8Bit(argv[3]), labDb) &&
                  QFile(labDb).setPermissions(QFileDevice::ReadOwner | QFileDevice::WriteOwner),
              QStringLiteral("lab: template database copied"));
        tlab->setDatabasePath(labDb);
        w.showView(MainWindow::Templates);
        QApplication::processEvents();
        check(tlab->refreshed() && tlab->platformBox()->findData(QStringLiteral("juniper_junos")) > 0 &&
                  tlab->platformBox()->itemText(tlab->platformBox()->findData(QStringLiteral("juniper_junos"))) ==
                      QLatin1String("juniper_junos (16)"),
              QStringLiteral("lab: platforms listed with netlapse's counts"));

        const QString session = QStringLiteral(
            "lab-leaf-1#show ip arp\n"
            "Address         Age (sec)  Hardware Addr   Interface\n"
            "172.16.10.1       0:00:12  001c.7300.0001  Vlan10, Ethernet1\n"
            "172.16.10.20      0:01:40  001c.7300.0020  Vlan10, Ethernet7\n"
            "172.16.20.1       0:00:03  001c.7300.0101  Vlan20, Ethernet2\n"
            "172.16.20.44      0:02:55  001c.7300.0144  Vlan20, Ethernet9\n"
            "lab-leaf-1#");
        const QString tpl = QStringLiteral(
            "Value ADDRESS (\\d+\\.\\d+\\.\\d+\\.\\d+)\n"
            "Value AGE (\\S+)\n"
            "Value MAC (\\S+)\n"
            "Value INTERFACE (.+?)\n"
            "\n"
            "Start\n"
            "  ^Address\\s+Age\n"
            "  ^${ADDRESS}\\s+${AGE}\\s+${MAC}\\s+${INTERFACE}\\s*$$ -> Record\n");

        // Test: a good template on cleaned output.
        tlab->rawEdit()->setPlainText(session);
        tlab->templateEdit()->setPlainText(tpl);
        tlab->nameEdit()->setText(QStringLiteral("arista_eos_show_lab_arp"));
        tlab->test();
        check(tlab->matchBox()->property("state").toString() == QLatin1String("ok") &&
                  tlab->parsedTable()->rowCount() == 4 && tlab->parsedTable()->columnCount() == 4 &&
                  tlab->scorePill()->text().startsWith(QLatin1String("score ")),
              QStringLiteral("lab: test parses 4 records (%1, %2)")
                  .arg(tlab->matchCaption()->text(), tlab->scorePill()->text()));
        check(tlab->barValue(0) > 0 && tlab->barValue(1) > 0 && tlab->barValue(2) > 0.99 && tlab->barValue(3) > 0.99,
              QStringLiteral("lab: score bars drawn from the breakdown (%1 %2 %3 %4)")
                  .arg(tlab->barValue(0)).arg(tlab->barValue(1)).arg(tlab->barValue(2)).arg(tlab->barValue(3)));
        for (ThemeId id : {ThemeId::Light, ThemeId::Dark, ThemeId::Cyber}) {
            ThemeManager::instance().setTheme(id);
            QApplication::processEvents();
            w.grab().save(QStringLiteral("%1-lab-test-%2.png").arg(prefix, themeKey(id)));
        }
        ThemeManager::instance().setTheme(ThemeId::Light);

        // A strict template on the uncleaned text: a State Error, its rule
        // line marked in the template and its input line in the output.
        tlab->templateEdit()->setPlainText(tpl + QStringLiteral("  ^.* -> Error\n"));
        tlab->cleanBox()->setChecked(false);
        tlab->test();
        check(tlab->matchBox()->property("state").toString() == QLatin1String("fail") &&
                  tlab->matchCaption()->text() == QLatin1String("STATE ERROR") &&
                  tlab->matchMeta()->text().contains(QLatin1String("<b>9</b>")),
              QStringLiteral("lab: a strict template fails on the echo line (%1)").arg(tlab->matchMeta()->text()));
        const auto marks = tlab->templateEdit()->extraSelections();
        const auto rawMarks = tlab->rawEdit()->extraSelections();
        check(!marks.isEmpty() && marks.first().cursor.blockNumber() == 8 && !rawMarks.isEmpty() &&
                  rawMarks.first().cursor.blockNumber() == 0,
              QStringLiteral("lab: the rule line and the input line are marked"));
        w.grab().save(prefix + QStringLiteral("-lab-error-light.png"));
        tlab->templateEdit()->setPlainText(tpl);
        check(tlab->templateEdit()->extraSelections().isEmpty(), QStringLiteral("lab: editing the template drops the mark"));
        tlab->cleanBox()->setChecked(true);

        tlab->templateEdit()->setPlainText(QStringLiteral("Value A (\\S+\n\nStart\n  ^${A} -> Record\n"));
        tlab->test();
        check(tlab->matchCaption()->text() == QLatin1String("COMPILE ERROR"), QStringLiteral("lab: a compile error is shown as one"));
        tlab->templateEdit()->setPlainText(tpl);

        // Sweep: the primary filter, then a command nothing is named for.
        tlab->setPlatform(QStringLiteral("arista_eos"));
        tlab->commandEdit()->setText(QStringLiteral("show ip arp"));
        tlab->sweep();
        check(waitFor([&] { return !tlab->bridge()->busy(); }, 60000), QStringLiteral("lab: sweep finishes"));
        QApplication::processEvents();
        check(tlab->matchBox()->property("state").toString() == QLatin1String("ok") &&
                  tlab->matchCaption()->text().contains(QLatin1String("BEST MATCH")) &&
                  tlab->parsedTable()->rowCount() == 4 && tlab->candidates()->count() > 0,
              QStringLiteral("lab: sweep finds the database's best (%1, %2 candidates)")
                  .arg(tlab->matchCaption()->text()).arg(tlab->candidates()->count()));
        bool winnerMarked = false;
        for (int i = 0; i < tlab->candidates()->count(); ++i)
            winnerMarked = winnerMarked || tlab->candidates()->item(i)->text().contains(QLatin1String("winner"));
        check(winnerMarked, QStringLiteral("lab: the winner is marked among the candidates"));
        for (ThemeId id : {ThemeId::Light, ThemeId::Dark, ThemeId::Cyber}) {
            ThemeManager::instance().setTheme(id);
            QApplication::processEvents();
            w.grab().save(QStringLiteral("%1-lab-sweep-%2.png").arg(prefix, themeKey(id)));
        }
        ThemeManager::instance().setTheme(ThemeId::Light);

        tlab->commandEdit()->setText(QStringLiteral("show lab arp"));
        tlab->sweep();
        check(waitFor([&] { return !tlab->bridge()->busy(); }, 60000), QStringLiteral("lab: second sweep finishes"));
        QApplication::processEvents();
        check(tlab->matchBox()->property("state").toString() == QLatin1String("fallback") &&
                  tlab->matchMeta()->text().contains(QLatin1String("filter <code>arista</code>")),
              QStringLiteral("lab: nothing named for the command falls back to the vendor (%1)").arg(tlab->matchCaption()->text()));

        // Save: a new row, then the same sweep finds it without a restart --
        // the capture engine reloaded.
        tlab->save();
        check(waitFor([&] { return !tlab->bridge()->busy(); }, 60000), QStringLiteral("lab: save finishes"));
        QApplication::processEvents();
        const qint64 savedRow = tlab->editingRowid();
        check(savedRow > 0 && tlab->editState()->text().contains(QLatin1String("id")),
              QStringLiteral("lab: saved (%1)").arg(tlab->editState()->text()));
        tlab->sweep();
        check(waitFor([&] { return !tlab->bridge()->busy(); }, 60000), QStringLiteral("lab: sweep after save finishes"));
        QApplication::processEvents();
        check(tlab->matchBox()->property("state").toString() == QLatin1String("ok") &&
                  tlab->matchMeta()->text().contains(QLatin1String("your template wins")),
              QStringLiteral("lab: the saved template wins its own command (%1)").arg(tlab->matchMeta()->text()));

        // Saving again edits the same row rather than adding another.
        tlab->templateEdit()->setPlainText(tpl + QStringLiteral("\n# edited in the probe\n"));
        tlab->save();
        check(waitFor([&] { return !tlab->bridge()->busy(); }, 60000), QStringLiteral("lab: second save finishes"));
        QApplication::processEvents();
        check(tlab->editingRowid() == savedRow, QStringLiteral("lab: a second save updates the same row"));

        // Browser: find it, open another template, delete ours.
        tlab->showPage(TemplateLabView::Page::Browser);
        QApplication::processEvents();
        int ours = -1, lldp = -1;
        QTableWidget *bt = tlab->browserTable();
        for (int r = 0; r < bt->rowCount(); ++r) {
            if (bt->item(r, 2)->text() == QLatin1String("arista_eos_show_lab_arp")) ours = r;
            if (bt->item(r, 2)->text() == QLatin1String("juniper_junos_show_lldp_neighbors")) lldp = r;
        }
        check(ours >= 0 && lldp >= 0 && bt->rowCount() > 1000, QStringLiteral("lab: browser lists every template (%1)").arg(bt->rowCount()));
        w.grab().save(prefix + QStringLiteral("-lab-browser-light.png"));
        if (lldp >= 0) {
            QString err;
            const qint64 rowid = bt->item(lldp, 0)->data(Qt::UserRole).toLongLong();
            check(tlab->loadTemplate(rowid, &err) && tlab->nameEdit()->text() == QLatin1String("juniper_junos_show_lldp_neighbors") &&
                      tlab->templateEdit()->toPlainText().contains(QLatin1String("Value")) &&
                      tlab->platformBox()->currentData().toString() == QLatin1String("juniper_junos") &&
                      tlab->sampleButton()->isEnabled(),
                  QStringLiteral("lab: a template opens into the editor with its platform and sample (%1)").arg(err));
        }
        tlab->removeTemplate(savedRow);
        check(waitFor([&] { return !tlab->bridge()->busy(); }, 60000), QStringLiteral("lab: delete finishes"));
        QApplication::processEvents();
        QVector<LabItem> left;
        QString lerr;
        tlab->bridge()->list(QString(), QStringLiteral("show_lab_arp"), &left, &lerr);
        check(left.isEmpty(), QStringLiteral("lab: the deleted template is gone"));

        // From the store: the Store view's button fills output, platform and command.
        w.showView(MainWindow::StoreTab);
        StoreView *sv = w.storeView();
        check(sv->openFile(QStringLiteral("lab-r1"), QStringLiteral("arp-table"), QString()) && sv->labButton()->isEnabled(),
              QStringLiteral("lab: the Store view offers the Template Lab for a stored capture"));
        sv->labButton()->click();
        QApplication::processEvents();
        check(w.currentView() == MainWindow::Templates && tlab->rawEdit()->toPlainText() == sv->content()->toPlainText() &&
                  tlab->platformBox()->currentData().toString() == QLatin1String("cisco_ios") &&
                  tlab->commandEdit()->text() == QLatin1String("show ip arp"),
              QStringLiteral("lab: opened from the store with platform %1 and command \u201C%2\u201D")
                  .arg(tlab->platformBox()->currentData().toString(), tlab->commandEdit()->text()));
        tlab->sweep();
        check(waitFor([&] { return !tlab->bridge()->busy(); }, 60000), QStringLiteral("lab: store sweep finishes"));
        QApplication::processEvents();
        check(tlab->matchBox()->property("state").toString() == QLatin1String("ok") && tlab->parsedTable()->rowCount() > 0,
              QStringLiteral("lab: the stored ARP table sweeps to a parse (%1)").arg(tlab->matchDetail()->text()));
        w.showView(MainWindow::Run);
    }


    // ---- Settings ---------------------------------------------------------
    {
        QAction *prefsAct = nullptr;
        for (QAction *m : w.menuBar()->actions()) {
            if (!m->menu()) continue;
            for (QAction *a : m->menu()->actions())
                if (a->text() == QStringLiteral("Settings\u2026")) prefsAct = a;
        }
        check(prefsAct && prefsAct->menuRole() == QAction::PreferencesRole && !prefsAct->shortcut().isEmpty(),
              QStringLiteral("settings: File > Settings, Preferences role, with a shortcut"));
        const CaptureDefaults builtin = appsettings::builtinCaptureDefaults();
        check(!builtin.types.isEmpty() && builtin.hostKeys == QLatin1String("strict") &&
                  appsettings::captureDefaults() == builtin,
              QStringLiteral("settings: nothing saved means the library's defaults (%1)").arg(builtin.types.join(QLatin1Char(','))));

        // Cancel: the theme previewed while open goes back.
        ThemeManager::instance().setTheme(ThemeId::Light);
        bool previewed = false;
        const bool cancelled = !w.openSettings([&](SettingsDialog *d) {
            d->themeBox()->setCurrentIndex(d->themeBox()->findData(QStringLiteral("dark")));
            QApplication::processEvents();
            previewed = ThemeManager::instance().id() == ThemeId::Dark;
            d->tabs()->setCurrentIndex(0);
            QApplication::processEvents();
            d->grab().save(prefix + QStringLiteral("-settings-general-dark.png"));
            d->tabs()->setCurrentIndex(1);
            QApplication::processEvents();
            d->grab().save(prefix + QStringLiteral("-settings-capture-dark.png"));
            // The dialog body is the theme's, not Fusion's grey: the failure the
            // credential dialogs once had.
            const QImage img = d->grab().toImage();
            const QColor body = img.pixelColor(img.width() - 6, img.height() / 2);
            const QColor dlgBg = tokensFor(ThemeId::Dark).bgSecondary, pageBg = tokensFor(ThemeId::Dark).bgPrimary;
            const auto near = [](const QColor &a, const QColor &b) {
                return qAbs(a.red() - b.red()) + qAbs(a.green() - b.green()) + qAbs(a.blue() - b.blue()) < 24;
            };
            check(near(body, dlgBg) || near(body, pageBg),
                  QStringLiteral("settings: dark dialog body is the theme's (%1)").arg(body.name()));
            d->reject();
        });
        QAction *lightAct = nullptr;
        for (QAction *m : w.menuBar()->actions())
            if (m->menu())
                for (QAction *a : m->menu()->actions())
                    if (a->text() == tokensFor(ThemeId::Light).name) lightAct = a;
        check(cancelled && previewed && ThemeManager::instance().id() == ThemeId::Light && lightAct && lightAct->isChecked(),
              QStringLiteral("settings: the theme previews, and Cancel puts it and the View menu back"));

        // No capture types is refused, on the Capture tab, with a reason.
        w.openSettings([&](SettingsDialog *d) {
            d->tabs()->setCurrentIndex(0);
            for (int i = 0; i < d->typesList()->count(); ++i) d->typesList()->item(i)->setCheckState(Qt::Unchecked);
            const bool saved = d->save();
            check(!saved && d->tabs()->currentIndex() == 1 && d->errorLabel()->isVisible() &&
                      appsettings::captureDefaults() == builtin,
                  QStringLiteral("settings: no capture types is refused and nothing is written"));
            d->reject();
        });

        // Save: the Run form takes the values now, and they persist.
        const QString launchStorePath = tmp.filePath(QStringLiteral("launch-store"));
        const bool saved = w.openSettings([&](SettingsDialog *d) {
            for (int i = 0; i < d->typesList()->count(); ++i) {
                const QString t = d->typesList()->item(i)->text();
                d->typesList()->item(i)->setCheckState(
                    t == QLatin1String("running-config") || t == QLatin1String("inventory") ? Qt::Checked : Qt::Unchecked);
            }
            d->hostKeysBox()->setCurrentIndex(d->hostKeysBox()->findData(QStringLiteral("tofu")));
            d->legacyBox()->setChecked(true);
            d->parseBox()->setChecked(false);
            d->credentialTagsEdit()->setText(QStringLiteral("  lab  "));
            d->fixedStoreButton()->setChecked(true);
            d->storeEdit()->setText(launchStorePath);
            if (d->save()) d->accept(); else d->reject();
        });
        QStringList formTypes;
        for (int i = 0; i < w.runView()->typesList()->count(); ++i)
            if (w.runView()->typesList()->item(i)->checkState() == Qt::Checked)
                formTypes << w.runView()->typesList()->item(i)->text();
        check(saved && formTypes == QStringList{QStringLiteral("running-config"), QStringLiteral("inventory")} &&
                  w.runView()->hostKeysBox()->currentData().toString() == QLatin1String("tofu") &&
                  w.runView()->legacyBox()->isChecked() && !w.runView()->parseBox()->isChecked() &&
                  w.runView()->credTagsEdit()->text() == QLatin1String("lab"),
              QStringLiteral("settings: saving puts the Run form to the new defaults (%1)").arg(formTypes.join(QLatin1Char(','))));
        const CaptureDefaults stored = appsettings::captureDefaults();
        check(stored.types == formTypes && stored.hostKeys == QLatin1String("tofu") && stored.legacy && !stored.parse &&
                  stored.credentialTags == QLatin1String("lab") &&
                  appsettings::launchStore() == QDir::cleanPath(launchStorePath),
              QStringLiteral("settings: saved values read back (%1, launch %2)").arg(appsettings::fileName(), appsettings::launchStore()));

        // A new window starts from them: the next launch.
        {
            MainWindow w2;
            QStringList t2;
            for (int i = 0; i < w2.runView()->typesList()->count(); ++i)
                if (w2.runView()->typesList()->item(i)->checkState() == Qt::Checked)
                    t2 << w2.runView()->typesList()->item(i)->text();
            check(t2 == formTypes && w2.runView()->hostKeysBox()->currentData().toString() == QLatin1String("tofu") &&
                      QDir::cleanPath(w2.storePath()) == QDir::cleanPath(launchStorePath),
                  QStringLiteral("settings: a new window opens the launch store with the saved defaults (%1)").arg(w2.storePath()));
            // Its setStorePath recorded the launch store as the last one; the
            // default still wins at the next launch.
            check(appsettings::launchStore() == QDir::cleanPath(launchStorePath), QStringLiteral("settings: the default store outranks the last one"));
        }

        // The dialog opens on what was saved; Restore defaults fills the
        // library's and writes nothing.
        w.openSettings([&](SettingsDialog *d) {
            check(d->captureDefaults() == stored && d->fixedStoreButton()->isChecked(),
                  QStringLiteral("settings: the dialog opens on the saved values"));
            d->restoreDefaults();
            check(d->captureDefaults() == builtin && d->lastStoreButton()->isChecked() &&
                      appsettings::captureDefaults() == stored,
                  QStringLiteral("settings: Restore defaults fills the fields and saves nothing"));
            d->reject();
        });

        QSettings().remove(QStringLiteral("capture"));
        QSettings().remove(QStringLiteral("launch"));
        w.runView()->applyCaptureDefaults(appsettings::captureDefaults());
        ThemeManager::instance().setTheme(ThemeId::Light);
    }


    // ---- Copy and CSV export ---------------------------------------------
    {
        // The writer on the awkward cells, read back by an independent reader.
        ExportTable tricky;
        tricky.header = {QStringLiteral("NAME"), QStringLiteral("DESCRIPTION"), QStringLiteral("VLANS")};
        QJsonArray recs;
        recs.append(QJsonObject{{QStringLiteral("NAME"), QStringLiteral("et-0/0/1")},
                                {QStringLiteral("DESCRIPTION"), QStringLiteral("uplink, \"core\" side")},
                                {QStringLiteral("VLANS"), QJsonArray{QStringLiteral("10"), QStringLiteral("20")}}});
        recs.append(QJsonObject{{QStringLiteral("NAME"), QStringLiteral("-")},
                                {QStringLiteral("DESCRIPTION"), QString()}});
        const QVector<QStringList> back = readCsv(toCsv(parsedTable(tricky.header, recs, QStringLiteral("\n"))));
        check(back.size() == 3 && back.at(0) == tricky.header &&
                  back.at(1) == QStringList{QStringLiteral("et-0/0/1"), QStringLiteral("uplink, \"core\" side"),
                                            QStringLiteral("10\n20")} &&
                  back.at(2) == QStringList{QStringLiteral("-"), QString(), QString()},
              QStringLiteral("export: commas, quotes, list values and a leading '-' survive CSV unchanged"));
        const QString tsv = toTsv(parsedTable(tricky.header, recs, QStringLiteral(", ")));
        check(tsv.startsWith(QLatin1String("NAME\tDESCRIPTION\tVLANS\n")) && tsv.contains(QLatin1String("\t10, 20\n")),
              QStringLiteral("export: the clipboard form is tab-separated columns"));
        check(exportFileName({QStringLiteral("lab-r1"), QStringLiteral("arp-table"), QStringLiteral("2026-09-16T13:51:12Z")},
                             QStringLiteral("csv")) == QLatin1String("lab-r1_arp-table_2026-09-16T13_51_12Z.csv"),
              QStringLiteral("export: file names lose the characters a filesystem refuses"));
    }
    if (live) {
        StoreView *sv = w.storeView();
        w.showView(MainWindow::StoreTab);
        check(sv->openFile(QStringLiteral("lab-r1"), QStringLiteral("arp-table"), QString()),
              QStringLiteral("export: lab-r1 arp-table opens"));
        sv->showMode(StoreView::Mode::Raw);
        QApplication::processEvents();
        check(sv->copyButton()->isEnabled() && !sv->exportButton()->isEnabled(),
              QStringLiteral("export: Raw offers Copy and not Export"));
        QGuiApplication::clipboard()->clear();
        sv->copyButton()->click();
        check(QGuiApplication::clipboard()->text() == sv->content()->toPlainText() && !sv->content()->toPlainText().isEmpty() &&
                  sv->copyButton()->text().startsWith(QLatin1String("Copied")),
              QStringLiteral("export: Copy puts the stored capture on the clipboard, and says so"));

        sv->showMode(StoreView::Mode::Parsed);
        QApplication::processEvents();
        check(sv->exportButton()->isEnabled() && sv->copyButton()->isEnabled(), QStringLiteral("export: Parsed offers Copy and Export"));
        sv->copyButton()->click();
        const QStringList tsvLines = QGuiApplication::clipboard()->text().split(QLatin1Char('\n'), Qt::SkipEmptyParts);
        QStringList header;
        for (int c = 0; c < sv->parsedTable()->columnCount(); ++c) header << sv->parsedTable()->horizontalHeaderItem(c)->text();
        check(!tsvLines.isEmpty() && tsvLines.first() == header.join(QLatin1Char('\t')) &&
                  tsvLines.size() == sv->parsedTable()->rowCount() + 1,
              QStringLiteral("export: Copy in Parsed is the table as columns (%1 lines)").arg(tsvLines.size()));
        const QString csvPath = tmp.filePath(QStringLiteral("lab-r1-arp.csv"));
        QString err;
        check(sv->exportParsed(csvPath, &err), QStringLiteral("export: parsed records written (%1)").arg(err));
        QFile csvFile(csvPath);
        const QVector<QStringList> rows = csvFile.open(QIODevice::ReadOnly) ? readCsv(csvFile.readAll()) : QVector<QStringList>();
        bool cellsMatch = rows.size() == sv->parsedTable()->rowCount() + 1 && !rows.isEmpty() && rows.first() == header;
        for (int r = 1; cellsMatch && r < rows.size(); ++r)
            for (int c = 0; cellsMatch && c < header.size(); ++c)
                cellsMatch = rows.at(r).value(c) == sv->parsedTable()->item(r - 1, c)->text();
        check(cellsMatch, QStringLiteral("export: the CSV is the table, cell for cell (%1 rows)").arg(rows.size()));
        check(!sv->exportParsed(tmp.filePath(QStringLiteral("no-such-dir/x.csv")), &err) && !err.isEmpty(),
              QStringLiteral("export: a path that cannot be written is an error, not a silent nothing"));

        // Search: every hit to CSV, and the capture a hit is in to the clipboard.
        w.showView(MainWindow::Search);
        SearchView *search = w.searchView();
        // An earlier section searched already; a new search starts from nothing.
        search->runQuery(QStringLiteral("hostname"));
        check(!search->exportButton()->isEnabled() && !search->copyButton()->isEnabled(),
              QStringLiteral("export: a search in progress offers neither"));
        check(waitFor([&] { return !search->isRunning() && search->hits()->rowCount() > 0; }, 20000),
              QStringLiteral("export: search finishes with hits"));
        QApplication::processEvents();
        check(search->exportButton()->isEnabled() && search->copyButton()->isEnabled(),
              QStringLiteral("export: hits offer Export, the selected hit offers Copy"));
        search->copyButton()->click();
        check(QGuiApplication::clipboard()->text() == search->content()->toPlainText() &&
                  QGuiApplication::clipboard()->text().contains(QLatin1String("hostname")),
              QStringLiteral("export: search Copy is the whole capture the hit is in"));
        const QString hitsPath = tmp.filePath(QStringLiteral("hits.csv"));
        err.clear();
        check(search->exportHits(hitsPath, &err), QStringLiteral("export: hits written (%1)").arg(err));
        QFile hitsFile(hitsPath);
        const QVector<QStringList> hitRows = hitsFile.open(QIODevice::ReadOnly) ? readCsv(hitsFile.readAll()) : QVector<QStringList>();
        check(hitRows.size() == search->hits()->rowCount() + 1 &&
                  hitRows.first() == QStringList{QStringLiteral("device"), QStringLiteral("type"), QStringLiteral("file"),
                                                 QStringLiteral("line"), QStringLiteral("text"), QStringLiteral("truncated")} &&
                  hitRows.at(1).value(0) == search->hits()->item(0, 0)->text() &&
                  hitRows.at(1).value(3) == search->hits()->item(0, 2)->text() &&
                  hitRows.at(1).value(4) == search->hits()->item(0, 3)->text(),
              QStringLiteral("export: hits CSV has every hit with its file and line (%1 rows)").arg(hitRows.size()));
        w.showView(MainWindow::Run);
    }

    lab.kill();
    lab.waitForFinished(3000);
    std::printf("\n%d failure(s)\n", failures);
    return failures;
}
