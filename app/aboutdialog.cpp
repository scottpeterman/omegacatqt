// app/aboutdialog.cpp

#include "aboutdialog.h"

#include <QApplication>
#include <QClipboard>
#include <QComboBox>
#include <QDialogButtonBox>
#include <QEvent>
#include <QFile>
#include <QFontDatabase>
#include <QGuiApplication>
#include <QHBoxLayout>
#include <QLabel>
#include <QMouseEvent>
#include <QPointer>
#include <QPushButton>
#include <QScreen>
#include <QSysInfo>
#include <QTextBrowser>
#include <QVBoxLayout>

#include <iterator>

#include "runbridge.h"
#include "theme.h"

namespace omegacat {

namespace {

// The splash's width on screen, in logical pixels: half the stored image, so a
// 2x display shows every pixel of it and a 1x display a smooth reduction.
constexpr int kSplashWidth = 784;
// The Attributions section's reading area. Fixed, so opening it grows the
// dialog by a known amount instead of by the length of 600 lines of notices.
constexpr int kBrowserHeight = 300;

QPointer<AboutDialog> g_open;

class ClickToAbout : public QObject {
public:
    using QObject::QObject;

protected:
    bool eventFilter(QObject *watched, QEvent *e) override {
        if (e->type() == QEvent::MouseButtonRelease &&
            static_cast<QMouseEvent *>(e)->button() == Qt::LeftButton) {
            AboutDialog::showFor(static_cast<QWidget *>(watched)->window());
            return true;
        }
        return QObject::eventFilter(watched, e);
    }
};

struct Document {
    const char *label;
    const char *path;
    bool markdown;
};

// Resource paths from resources.qrc. The notices come first: they are what a
// reader opening Attributions is looking for, and they name the other three.
const Document kDocuments[] = {
    {"Third-party notices", ":/omegacat/licenses/THIRD_PARTY_NOTICES.md", true},
    {"GNU GPL v3.0 (OmegaCat)", ":/omegacat/licenses/GPL-3.0.txt", false},
    {"GNU LGPL v3.0 (Qt)", ":/omegacat/licenses/LGPL-3.0.txt", false},
    {"Apache License 2.0", ":/omegacat/licenses/Apache-2.0.txt", false},
};

QString readResource(const QString &path) {
    QFile f(path);
    if (!f.open(QIODevice::ReadOnly)) return {};
    return QString::fromUtf8(f.readAll());
}

}  // namespace

AboutDialog::AboutDialog(QWidget *parent) : QDialog(parent) {
    setWindowTitle(tr("About OmegaCat"));
    setAttribute(Qt::WA_DeleteOnClose);

    auto *col = new QVBoxLayout(this);
    col->setContentsMargins(0, 0, 0, 16);
    col->setSpacing(10);
    // Height follows the content, so the dialog grows when Attributions opens
    // and shrinks back when it closes; the width is the splash's.
    col->setSizeConstraint(QLayout::SetFixedSize);

    // Scaled once, smoothly, to the pixels the screen will show; a pixmap left
    // for QLabel to shrink is drawn without smoothing.
    initResources();
    const QPixmap src(QStringLiteral(":/omegacat/splash.png"));
    const qreal dpr = parent ? parent->devicePixelRatioF()
                             : (QGuiApplication::primaryScreen() ? QGuiApplication::primaryScreen()->devicePixelRatio()
                                                                 : 1.0);
    m_splash = new QLabel(this);
    if (!src.isNull()) {
        QPixmap pm = src.scaledToWidth(qRound(kSplashWidth * dpr), Qt::SmoothTransformation);
        pm.setDevicePixelRatio(dpr);
        m_splash->setPixmap(pm);
    }
    m_splash->setFixedWidth(kSplashWidth);
    m_splash->setAlignment(Qt::AlignCenter);
    col->addWidget(m_splash);

    auto *text = new QVBoxLayout;
    text->setContentsMargins(24, 4, 24, 0);
    text->setSpacing(6);

    auto *title = new QLabel(
        QStringLiteral("<b>OmegaCat</b>&nbsp;&nbsp;%1").arg(RunBridge::libraryVersion().toHtmlEscaped()), this);
    title->setTextInteractionFlags(Qt::TextSelectableByMouse);
    text->addWidget(title);

    auto *what = new QLabel(tr("Read-only configuration and state capture over SSH, a store that keeps every "
                               "version, structured parsing with TextFSM, and search across all of it."),
                            this);
    what->setWordWrap(true);
    text->addWidget(what);

    m_details = tr("OmegaCat %1\nQt %2 (built with %3)\n%4, %5")
                    .arg(RunBridge::libraryVersion(), QString::fromLatin1(qVersion()), QStringLiteral(QT_VERSION_STR),
                         QSysInfo::prettyProductName(), QSysInfo::currentCpuArchitecture());
    // On screen without the first line, which the title already says; the
    // copied text keeps it, since it goes where there is no title.
    auto *details = new QLabel(m_details.section(QLatin1Char('\n'), 1), this);
    setTone(details, QStringLiteral("muted"));
    details->setTextInteractionFlags(Qt::TextSelectableByMouse);
    text->addWidget(details);

    auto *lineage = new QLabel(tr("The capture engine, vault and inventory come from PathfinderSSH; the template "
                                  "engine is a port of netlapse's tfsm_fire. Built the Omega way: Go, C, C++ and Qt."),
                               this);
    lineage->setWordWrap(true);
    setTone(lineage, QStringLiteral("secondary"));
    text->addWidget(lineage);

    auto *link = new QLabel(QStringLiteral("<a href=\"https://github.com/scottpeterman/omegacatqt\">"
                                           "github.com/scottpeterman/omegacatqt</a>"),
                            this);
    link->setOpenExternalLinks(true);
    text->addWidget(link);

    // The notice GPLv3's "How to Apply" asks an interactive program to show,
    // and the Qt acknowledgement. About Qt below carries The Qt Company's own
    // wording, which stays right across Qt versions without anyone here
    // remembering to update it.
    auto *licence = new QLabel(tr("Copyright \u00A9 2026 Scott Peterman. OmegaCat is free software under the GNU "
                                  "General Public License v3.0 and comes with ABSOLUTELY NO WARRANTY. It uses Qt "
                                  "under the GNU Lesser General Public License v3.0, dynamically linked and "
                                  "unmodified. Attributions lists every component it incorporates."),
                               this);
    licence->setWordWrap(true);
    setTone(licence, QStringLiteral("secondary"));
    text->addWidget(licence);

    // ---- Attributions ----------------------------------------------------
    m_attributions = new QWidget(this);
    auto *attr = new QVBoxLayout(m_attributions);
    attr->setContentsMargins(0, 6, 0, 0);
    attr->setSpacing(6);
    m_documents = new QComboBox(m_attributions);
    for (const Document &d : kDocuments) m_documents->addItem(tr(d.label));
    auto *docRow = new QHBoxLayout;
    docRow->addWidget(m_documents);
    docRow->addStretch(1);
    attr->addLayout(docRow);
    m_browser = new QTextBrowser(m_attributions);
    m_browser->setProperty("role", QStringLiteral("notices"));
    m_browser->setOpenExternalLinks(true);
    m_browser->setFixedHeight(kBrowserHeight);
    attr->addWidget(m_browser);
    connect(m_documents, qOverload<int>(&QComboBox::currentIndexChanged), this, &AboutDialog::showDocument);
    m_attributions->setVisible(false);
    text->addWidget(m_attributions);

    auto *buttons = new QDialogButtonBox(this);
    m_toggle = buttons->addButton(tr("Attributions"), QDialogButtonBox::ActionRole);
    m_toggle->setCheckable(true);
    connect(m_toggle, &QPushButton::toggled, this, &AboutDialog::setAttributionsVisible);
    m_aboutQt = buttons->addButton(tr("About Qt"), QDialogButtonBox::ActionRole);
    connect(m_aboutQt, &QPushButton::clicked, qApp, &QApplication::aboutQt);
    QPushButton *copy = buttons->addButton(tr("Copy details"), QDialogButtonBox::ActionRole);
    connect(copy, &QPushButton::clicked, this, [this] { QGuiApplication::clipboard()->setText(m_details); });
    QPushButton *close = buttons->addButton(QDialogButtonBox::Close);
    close->setDefault(true);
    setStyleProperty(close, "primary", QStringLiteral("true"));
    connect(buttons, &QDialogButtonBox::rejected, this, &QDialog::close);
    text->addSpacing(4);
    text->addWidget(buttons);

    col->addLayout(text);
    setFixedWidth(kSplashWidth);
    showDocument(0);
}

QStringList AboutDialog::documentPaths() {
    QStringList out;
    for (const Document &d : kDocuments) out << QString::fromLatin1(d.path);
    return out;
}

void AboutDialog::showDocument(int i) {
    if (i < 0 || i >= int(std::size(kDocuments))) return;
    if (m_documents->currentIndex() != i) {
        m_documents->setCurrentIndex(i);  // re-enters through the signal
        return;
    }
    const Document &d = kDocuments[i];
    const QString body = readResource(QString::fromLatin1(d.path));
    if (body.isEmpty()) {
        // Only a build that dropped a resource gets here, and it should say so
        // rather than show an empty pane that reads as "nothing to attribute".
        m_browser->setPlainText(tr("%1 is missing from this build (%2).").arg(tr(d.label), QString::fromLatin1(d.path)));
        return;
    }
    if (d.markdown) {
        m_browser->document()->setDefaultFont(QApplication::font());
        m_browser->setMarkdown(body);
    } else {
        // Licence texts are laid out for a fixed-width font: centred headings
        // and indented clauses turn to noise in a proportional one.
        m_browser->document()->setDefaultFont(QFontDatabase::systemFont(QFontDatabase::FixedFont));
        m_browser->setPlainText(body);
    }
    m_browser->moveCursor(QTextCursor::Start);
}

void AboutDialog::setAttributionsVisible(bool on) {
    if (m_toggle->isChecked() != on) {
        m_toggle->setChecked(on);  // re-enters through the signal
        return;
    }
    m_attributions->setVisible(on);
}

// Not isVisible(): that is false for every child while the dialog itself is
// hidden, which would report a closed section for an open one.
bool AboutDialog::attributionsVisible() const { return !m_attributions->isHidden(); }

AboutDialog *AboutDialog::showFor(QWidget *parent) {
    if (!g_open) g_open = new AboutDialog(parent);
    g_open->show();
    g_open->raise();
    g_open->activateWindow();
    return g_open;
}

AboutDialog *AboutDialog::showAttributionsFor(QWidget *parent) {
    AboutDialog *d = showFor(parent);
    d->showDocument(0);
    d->setAttributionsVisible(true);
    return d;
}

void AboutDialog::makeTrigger(QWidget *w) {
    w->setCursor(Qt::PointingHandCursor);
    w->setToolTip(tr("About OmegaCat"));
    w->installEventFilter(new ClickToAbout(w));
}

}  // namespace omegacat
