// app/settingsdialog.cpp
#include "settingsdialog.h"

#include <QButtonGroup>
#include <QCheckBox>
#include <QComboBox>
#include <QDir>
#include <QFormLayout>
#include <QHBoxLayout>
#include <QLabel>
#include <QLineEdit>
#include <QListWidget>
#include <QPushButton>
#include <QRadioButton>
#include <QTabWidget>
#include <QVBoxLayout>

#include "filedialogs.h"
#include "modalframe.h"
#include "runbridge.h"
#include "theme.h"

namespace omegacat {

namespace {

constexpr int kDialogWidth = 600;

void styleForm(QFormLayout *form) {
    form->setHorizontalSpacing(14);
    form->setVerticalSpacing(10);
    form->setFieldGrowthPolicy(QFormLayout::AllNonFixedFieldsGrow);
    form->setLabelAlignment(Qt::AlignLeft | Qt::AlignVCenter);
}

QWidget *page(QTabWidget *tabs, QFormLayout **form) {
    auto *w = new QWidget(tabs);
    w->setProperty("bare", true);
    auto *col = new QVBoxLayout(w);
    col->setContentsMargins(14, 16, 14, 14);
    col->setSpacing(12);
    *form = new QFormLayout;
    styleForm(*form);
    col->addLayout(*form);
    col->addStretch(1);
    return w;
}

}  // namespace

SettingsDialog::SettingsDialog(QWidget *parent) : QDialog(parent) {
    m_themeAtOpen = omegacat::themeKey(ThemeManager::instance().id());
    m_frame = new ModalFrame(this, tr("Settings"), kDialogWidth);

    m_tabs = new QTabWidget(m_frame->bodyWidget());
    m_tabs->setDocumentMode(true);

    // ---- General -------------------------------------------------------
    QFormLayout *general = nullptr;
    QWidget *generalPage = page(m_tabs, &general);

    m_theme = new QComboBox(generalPage);
    for (ThemeId id : {ThemeId::Light, ThemeId::Dark, ThemeId::Cyber})
        m_theme->addItem(tokensFor(id).name, omegacat::themeKey(id));
    general->addRow(fieldLabel(tr("Theme"), generalPage), m_theme);

    auto *storeBox = new QWidget(generalPage);
    auto *storeCol = new QVBoxLayout(storeBox);
    storeCol->setContentsMargins(0, 0, 0, 0);
    storeCol->setSpacing(6);
    m_useLast = new QRadioButton(tr("The last store opened"), storeBox);
    m_useFixed = new QRadioButton(tr("This store:"), storeBox);
    auto *which = new QButtonGroup(storeBox);
    which->addButton(m_useLast);
    which->addButton(m_useFixed);
    storeCol->addWidget(m_useLast);
    auto *fixedRow = new QHBoxLayout;
    fixedRow->addWidget(m_useFixed);
    m_store = new QLineEdit(storeBox);
    m_store->setProperty("mono", QStringLiteral("true"));
    m_store->setPlaceholderText(QStringLiteral("~/captures"));
    fixedRow->addWidget(m_store, 1);
    m_browse = new QPushButton(tr("Browse\u2026"), storeBox);
    fixedRow->addWidget(m_browse);
    storeCol->addLayout(fixedRow);
    general->addRow(fieldLabel(tr("Open at launch"), generalPage), storeBox);
    general->addRow(QString(), descLabel(tr("File \u203A Open store and the header's Change\u2026 still switch "
                                             "stores for the session; this only decides where OmegaCat starts."),
                                          generalPage));

    auto *where = monoLabel(appsettings::fileName(), generalPage, "hint");
    where->setWordWrap(true);
    general->addRow(fieldLabel(tr("Saved in"), generalPage), where);
    m_tabs->addTab(generalPage, tr("General"));

    // ---- Capture -------------------------------------------------------
    QFormLayout *capture = nullptr;
    QWidget *capturePage = page(m_tabs, &capture);

    m_types = new QListWidget(capturePage);
    for (const CaptureType &ct : RunBridge::types()) {
        auto *item = new QListWidgetItem(ct.type, m_types);
        item->setFlags(item->flags() | Qt::ItemIsUserCheckable);
        item->setCheckState(Qt::Unchecked);
        item->setToolTip(ct.description);
    }
    m_types->setVerticalScrollBarPolicy(Qt::ScrollBarAlwaysOff);
    capture->addRow(fieldLabel(tr("Capture types"), capturePage), m_types);

    m_hostKeys = new QComboBox(capturePage);
    m_hostKeys->addItem(tr("Strict host keys"), QStringLiteral("strict"));
    m_hostKeys->addItem(tr("Trust on first use"), QStringLiteral("tofu"));
    capture->addRow(fieldLabel(tr("Host keys"), capturePage), m_hostKeys);

    m_legacy = new QCheckBox(tr("Legacy algorithms (SHA-1 KEX, CBC)"), capturePage);
    capture->addRow(QString(), m_legacy);
    m_parse = new QCheckBox(tr("Parse ARP and MAC tables"), capturePage);
    capture->addRow(QString(), m_parse);

    m_credTags = new QLineEdit(capturePage);
    m_credTags->setPlaceholderText(tr("(every credential)"));
    capture->addRow(fieldLabel(tr("Credentials tagged"), capturePage), m_credTags);
    capture->addRow(QString(), descLabel(tr("The Run form starts with these, and takes them again when you save. "
                                             "A capture already running is not affected."),
                                          capturePage));
    m_tabs->addTab(capturePage, tr("Capture"));

    m_frame->body()->addWidget(m_tabs);

    m_error = new QLabel(m_frame->bodyWidget());
    m_error->setWordWrap(true);
    setTone(m_error, QStringLiteral("danger"));
    m_error->hide();
    m_frame->body()->addWidget(m_error);

    QPushButton *restore = m_frame->addButton(tr("Restore defaults"), ModalFrame::Secondary);
    connect(restore, &QPushButton::clicked, this, &SettingsDialog::restoreDefaults);
    QPushButton *cancel = m_frame->addButton(tr("Cancel"), ModalFrame::Secondary);
    connect(cancel, &QPushButton::clicked, this, &SettingsDialog::reject);
    QPushButton *saveBtn = m_frame->addButton(tr("Save"), ModalFrame::Primary);
    connect(saveBtn, &QPushButton::clicked, this, [this] {
        if (save()) accept();
    });
    m_frame->setFooterHint(tr("Return saves."));

    // The theme previews as it is picked; reject() puts back the one the
    // dialog opened on.
    connect(m_theme, qOverload<int>(&QComboBox::currentIndexChanged), this, [this] {
        ThemeManager::instance().setTheme(themeFromKey(m_theme->currentData().toString()));
    });
    const auto syncStore = [this] {
        const bool fixed = m_useFixed->isChecked();
        m_store->setEnabled(fixed);
        m_browse->setEnabled(fixed);
    };
    connect(m_useFixed, &QRadioButton::toggled, this, syncStore);
    connect(m_browse, &QPushButton::clicked, this, [this] {
        const QString dir = pickDirectory(this, tr("Store to open at launch"),
                                          m_store->text().isEmpty() ? appsettings::launchStore() : m_store->text());
        if (!dir.isEmpty()) m_store->setText(QDir::toNativeSeparators(dir));
    });

    load();
    syncStore();
}

void SettingsDialog::load() {
    const QSignalBlocker block(m_theme);
    m_theme->setCurrentIndex(qMax(0, m_theme->findData(m_themeAtOpen)));

    const QString fixed = appsettings::defaultStore();
    (fixed.isEmpty() ? m_useLast : m_useFixed)->setChecked(true);
    m_store->setText(QDir::toNativeSeparators(fixed.isEmpty() ? appsettings::lastStore() : fixed));

    const CaptureDefaults d = appsettings::captureDefaults();
    for (int i = 0; i < m_types->count(); ++i)
        m_types->item(i)->setCheckState(d.types.contains(m_types->item(i)->text()) ? Qt::Checked : Qt::Unchecked);
    m_hostKeys->setCurrentIndex(qMax(0, m_hostKeys->findData(d.hostKeys)));
    m_legacy->setChecked(d.legacy);
    m_parse->setChecked(d.parse);
    m_credTags->setText(d.credentialTags);
}

void SettingsDialog::restoreDefaults() {
    m_theme->setCurrentIndex(qMax(0, m_theme->findData(QStringLiteral("light"))));
    m_useLast->setChecked(true);
    const CaptureDefaults d = appsettings::builtinCaptureDefaults();
    for (int i = 0; i < m_types->count(); ++i)
        m_types->item(i)->setCheckState(d.types.contains(m_types->item(i)->text()) ? Qt::Checked : Qt::Unchecked);
    m_hostKeys->setCurrentIndex(qMax(0, m_hostKeys->findData(d.hostKeys)));
    m_legacy->setChecked(d.legacy);
    m_parse->setChecked(d.parse);
    m_credTags->clear();
    m_error->hide();
}

QString SettingsDialog::themeKey() const { return m_theme->currentData().toString(); }

QString SettingsDialog::defaultStore() const {
    return m_useFixed->isChecked() ? QDir::fromNativeSeparators(m_store->text().trimmed()) : QString();
}

CaptureDefaults SettingsDialog::captureDefaults() const {
    CaptureDefaults d;
    for (int i = 0; i < m_types->count(); ++i)
        if (m_types->item(i)->checkState() == Qt::Checked) d.types << m_types->item(i)->text();
    d.hostKeys = m_hostKeys->currentData().toString();
    d.legacy = m_legacy->isChecked();
    d.parse = m_parse->isChecked();
    d.credentialTags = m_credTags->text().trimmed();
    return d;
}

void SettingsDialog::showError(const QString &text) {
    m_error->setText(text);
    m_error->show();
    m_frame->fitToContent();
}

bool SettingsDialog::save() {
    const CaptureDefaults d = captureDefaults();
    if (d.types.isEmpty()) {
        m_tabs->setCurrentIndex(1);
        showError(tr("Pick at least one capture type: a Run form that starts with none cannot capture."));
        return false;
    }
    if (m_useFixed->isChecked() && defaultStore().isEmpty()) {
        m_tabs->setCurrentIndex(0);
        showError(tr("Name the store to open at launch, or choose the last store opened."));
        return false;
    }
    m_error->hide();
    appsettings::setTheme(themeKey());
    appsettings::setDefaultStore(defaultStore());
    appsettings::setCaptureDefaults(d);
    m_themeAtOpen = themeKey();  // saved: nothing for reject() to put back
    return true;
}

void SettingsDialog::reject() {
    ThemeManager::instance().setTheme(themeFromKey(m_themeAtOpen));
    QDialog::reject();
}

void SettingsDialog::showEvent(QShowEvent *event) {
    QDialog::showEvent(event);
    if (m_aligned) return;
    m_aligned = true;
    ensurePolished();
    // Every type visible without scrolling. Measured here, not in the
    // constructor: the row height comes from the stylesheet's padding, which
    // an unpolished list does not have yet, and the constructor's measure left
    // the last type below the fold.
    m_types->ensurePolished();
    int rows = 0;
    for (int i = 0; i < m_types->count(); ++i) rows += m_types->sizeHintForRow(i);
    m_types->setFixedHeight(rows + 2 * m_types->frameWidth() + 8);
    if (layout()) layout()->activate();
    alignFieldLabels(m_tabs);
    m_frame->fitToContent();
}

}  // namespace omegacat
