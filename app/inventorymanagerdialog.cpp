// app/inventorymanagerdialog.cpp
#include "inventorymanagerdialog.h"

#include <QCheckBox>
#include <QComboBox>
#include <QFormLayout>
#include <QFrame>
#include <QHBoxLayout>
#include <QHeaderView>
#include <QInputDialog>
#include <QJsonObject>
#include <QLabel>
#include <QLineEdit>
#include <QMenu>
#include <QMessageBox>
#include <QPushButton>
#include <QRegularExpression>
#include <QSet>
#include <QSpinBox>
#include <QTreeWidget>
#include <QVBoxLayout>

#include "modalframe.h"
#include "theme.h"
#include "vault.h"

namespace omegacat {
namespace {

constexpr int kDialogWidth = 980;
constexpr int KeyRole = Qt::UserRole;
constexpr int FolderRole = Qt::UserRole + 1;

// Combo item data for "leave this field alone" in a multi-selection. Not a
// platform or credential name anyone can have.
const QString kUnchanged = QStringLiteral("\x01unchanged");

bool isDevice(const QTreeWidgetItem *item) { return item && item->parent(); }

}  // namespace

InventoryManagerDialog::InventoryManagerDialog(const QString &inventoryPath, Vault *vault, const QString &focusKey,
                                               QWidget *parent)
    : QDialog(parent), m_path(inventoryPath), m_vault(vault) {
    buildUi();
    reload();
    if (!focusKey.isEmpty()) selectKeys({focusKey});
    else selectionChanged();
}

void InventoryManagerDialog::buildUi() {
    m_frame = new ModalFrame(this, tr("Inventory"), kDialogWidth);
    QWidget *body = m_frame->bodyWidget();

    auto *columns = new QHBoxLayout;
    columns->setSpacing(14);

    // ---- left: the tree and what arranges it
    auto *left = new QVBoxLayout;
    left->setSpacing(6);
    m_filter = new QLineEdit(body);
    m_filter->setPlaceholderText(tr("Filter name, host, platform, credential"));
    m_filter->setClearButtonEnabled(true);
    left->addWidget(m_filter);

    m_tree = new QTreeWidget(body);
    m_tree->setColumnCount(4);
    m_tree->setHeaderLabels({tr("Name"), tr("Host"), tr("Platform"), tr("Credential")});
    m_tree->setSelectionMode(QAbstractItemView::ExtendedSelection);
    m_tree->setUniformRowHeights(true);
    m_tree->setIndentation(14);
    m_tree->header()->setStretchLastSection(false);
    m_tree->header()->setSectionResizeMode(0, QHeaderView::Stretch);
    for (int c = 1; c < 4; ++c) m_tree->header()->setSectionResizeMode(c, QHeaderView::ResizeToContents);
    m_tree->setMinimumHeight(420);
    left->addWidget(m_tree, 1);

    auto *arrange = new QHBoxLayout;
    arrange->setSpacing(6);
    auto *newFolder = new QPushButton(tr("New folder\u2026"), body);
    m_renameFolder = new QPushButton(tr("Rename folder\u2026"), body);
    m_add = new QPushButton(tr("Add device"), body);
    m_move = new QPushButton(tr("Move to\u2026"), body);
    m_delete = new QPushButton(tr("Delete\u2026"), body);
    for (QPushButton *b : {newFolder, m_renameFolder, m_add, m_move, m_delete}) arrange->addWidget(b);
    arrange->addStretch(1);
    left->addLayout(arrange);
    columns->addLayout(left, 3);

    // ---- right: the device form
    QFrame *well = m_frame->addWell();
    well->setMinimumWidth(330);
    auto *wellBox = new QVBoxLayout(well);
    wellBox->setContentsMargins(14, 12, 14, 12);
    wellBox->setSpacing(8);
    m_formTitle = new QLabel(well);
    m_formTitle->setWordWrap(true);
    wellBox->addWidget(m_formTitle);

    auto *form = new QFormLayout;
    form->setHorizontalSpacing(12);
    form->setVerticalSpacing(8);
    form->setFieldGrowthPolicy(QFormLayout::AllNonFixedFieldsGrow);
    m_name = new QLineEdit(well);
    m_host = new QLineEdit(well);
    m_host->setPlaceholderText(QStringLiteral("172.16.1.2"));
    m_port = new QSpinBox(well);
    m_port->setRange(0, 65535);
    m_port->setSpecialValueText(tr("22 (default)"));
    m_platform = new QComboBox(well);
    m_hint = new QLabel(well);
    setTone(m_hint, QStringLiteral("muted"));
    m_hint->setWordWrap(true);
    m_legacy = new QCheckBox(tr("Legacy SSH algorithms"), well);
    m_legacy->setToolTip(tr("SHA-1 key exchange and CBC ciphers for this device, whatever the run is set to"));
    m_credential = new QComboBox(well);
    m_credential->setEditable(true);
    m_credential->setInsertPolicy(QComboBox::NoInsert);
    form->addRow(fieldLabel(tr("Name"), well), m_name);
    form->addRow(fieldLabel(tr("Host"), well), m_host);
    form->addRow(fieldLabel(tr("Port"), well), m_port);
    form->addRow(fieldLabel(tr("Platform"), well), m_platform);
    form->addRow(QString(), m_hint);
    form->addRow(QString(), m_legacy);
    form->addRow(fieldLabel(tr("Credential"), well), m_credential);
    wellBox->addLayout(form);
    wellBox->addStretch(1);
    m_save = new QPushButton(tr("Save"), well);
    m_save->setProperty("primary", QStringLiteral("true"));
    wellBox->addWidget(m_save);

    // addWell() put the well in the body layout; take it into the columns.
    m_frame->body()->removeWidget(well);
    columns->addWidget(well, 2);
    m_frame->body()->addLayout(columns, 1);

    m_notice = new QLabel(body);
    m_notice->setWordWrap(true);
    setTone(m_notice, QStringLiteral("danger"));
    m_notice->hide();
    m_frame->body()->addWidget(m_notice);

    QPushButton *close = m_frame->addButton(tr("Close"), ModalFrame::Primary);
    connect(close, &QPushButton::clicked, this, &QDialog::accept);

    connect(m_tree, &QTreeWidget::itemSelectionChanged, this, &InventoryManagerDialog::selectionChanged);
    connect(m_filter, &QLineEdit::textChanged, this, [this] {
        const QString needle = m_filter->text().trimmed();
        for (int i = 0; i < m_tree->topLevelItemCount(); ++i) {
            QTreeWidgetItem *f = m_tree->topLevelItem(i);
            int shown = 0;
            for (int j = 0; j < f->childCount(); ++j) {
                QTreeWidgetItem *d = f->child(j);
                bool hit = needle.isEmpty();
                for (int c = 0; c < 4 && !hit; ++c) hit = d->text(c).contains(needle, Qt::CaseInsensitive);
                d->setHidden(!hit);
                shown += hit ? 1 : 0;
            }
            f->setHidden(!needle.isEmpty() && shown == 0);
        }
    });
    connect(m_save, &QPushButton::clicked, this, &InventoryManagerDialog::save);
    connect(newFolder, &QPushButton::clicked, this, &InventoryManagerDialog::newFolder);
    connect(m_renameFolder, &QPushButton::clicked, this, &InventoryManagerDialog::renameFolder);
    connect(m_add, &QPushButton::clicked, this, [this] {
        QString folder = selectedFolder();
        if (folder.isEmpty() && !m_folders.isEmpty()) folder = m_folders.first().name;
        if (folder.isEmpty()) {
            setNotice(tr("Create a folder first."));
            return;
        }
        beginAdd(folder);
    });
    connect(m_move, &QPushButton::clicked, this, &InventoryManagerDialog::moveSelection);
    connect(m_delete, &QPushButton::clicked, this, &InventoryManagerDialog::deleteSelection);
}

void InventoryManagerDialog::setNotice(const QString &text) {
    m_notice->setText(text);
    m_notice->setVisible(!text.isEmpty());
}

void InventoryManagerDialog::reload() {
    QString err;
    if (!inventory::load(m_path, &m_folders, &err)) {
        m_folders.clear();
        setNotice(err);
    }
    const QSignalBlocker block(m_tree);
    m_tree->clear();
    for (const InventoryFolder &f : m_folders) {
        auto *folder = new QTreeWidgetItem(m_tree, {f.name});
        folder->setData(0, FolderRole, f.name);
        QFont bold = folder->font(0);
        bold.setBold(true);
        folder->setFont(0, bold);
        for (const InventorySession &s : f.sessions) {
            const QString host = s.port > 0 && s.port != 22 ? QStringLiteral("%1:%2").arg(s.host).arg(s.port) : s.host;
            // A set platform plainly; the map's guess marked as one.
            const QString platform = !s.platform.isEmpty() ? s.platform
                                     : !s.deviceType.isEmpty() ? tr("%1 (guess)").arg(s.deviceType)
                                                               : QString();
            auto *item = new QTreeWidgetItem(folder, {s.name, host, platform, s.credential});
            item->setData(0, KeyRole, s.key);
            item->setData(0, FolderRole, f.name);
        }
        folder->setExpanded(true);
    }
    if (!m_filter->text().isEmpty()) emit m_filter->textChanged(m_filter->text());
}

bool InventoryManagerDialog::applyEdits(const QJsonArray &edits, const QStringList &reselectKeys) {
    InventoryApply result;
    QString err;
    if (!inventory::apply(m_path, edits, &result, &err)) {
        // The C surface names itself and the edit's position; for one action
        // in this dialog the sentence after that is the part worth reading.
        err.remove(QRegularExpression(QStringLiteral("^inventory_apply: (edit \\d+ \\(\\w+\\): )?")));
        setNotice(err);
        return false;
    }
    m_changed = true;
    inventory::composeKeys(&m_keyChanges, result.keys);
    setNotice(QString());
    reload();
    QStringList again;
    for (const QString &k : reselectKeys) {
        const QString now = result.keys.value(k, k);
        if (!now.isEmpty()) again.append(now);
    }
    again.append(result.added);
    selectKeys(again);
    return true;
}

void InventoryManagerDialog::selectKeys(const QStringList &keys) {
    const QSet<QString> want(keys.cbegin(), keys.cend());
    {
        const QSignalBlocker block(m_tree);
        m_tree->clearSelection();
        QTreeWidgetItem *first = nullptr;
        for (int i = 0; i < m_tree->topLevelItemCount(); ++i) {
            QTreeWidgetItem *f = m_tree->topLevelItem(i);
            for (int j = 0; j < f->childCount(); ++j) {
                QTreeWidgetItem *d = f->child(j);
                if (want.contains(d->data(0, KeyRole).toString())) {
                    d->setSelected(true);
                    if (!first) first = d;
                }
            }
        }
        if (first) m_tree->scrollToItem(first);
    }
    selectionChanged();
}

QStringList InventoryManagerDialog::selectedKeys() const {
    QStringList out;
    for (QTreeWidgetItem *item : m_tree->selectedItems())
        if (isDevice(item)) out.append(item->data(0, KeyRole).toString());
    return out;
}

QString InventoryManagerDialog::selectedFolder() const {
    const QList<QTreeWidgetItem *> sel = m_tree->selectedItems();
    return sel.isEmpty() ? QString() : sel.first()->data(0, FolderRole).toString();
}

void InventoryManagerDialog::selectionChanged() {
    m_addFolder.clear();
    fillForm();
    const QList<QTreeWidgetItem *> sel = m_tree->selectedItems();
    const bool oneFolder = sel.size() == 1 && !isDevice(sel.first());
    m_renameFolder->setEnabled(oneFolder);
    m_move->setEnabled(!selectedKeys().isEmpty());
    m_delete->setEnabled(!sel.isEmpty());
}

void InventoryManagerDialog::fillForm() {
    m_filling = true;
    const QStringList keys = selectedKeys();
    const bool adding = !m_addFolder.isEmpty();
    const bool single = adding || keys.size() == 1;
    const bool any = adding || !keys.isEmpty();

    // Platform choices: detect, then every platform; a multi-selection
    // starts on "(unchanged)".
    m_platform->clear();
    if (!single && any) m_platform->addItem(tr("(unchanged)"), kUnchanged);
    m_platform->addItem(tr("(detect)"), QString());
    for (const QString &p : inventory::platforms()) m_platform->addItem(p, p);

    m_credential->clear();
    if (!single && any) m_credential->addItem(tr("(unchanged)"), kUnchanged);
    m_credential->addItem(tr("(ladder)"), QString());
    QVector<CredentialMeta> creds;
    if (m_vault && m_vault->list(&creds) == VaultError::Ok)
        for (const CredentialMeta &c : creds) m_credential->addItem(c.name, c.name);

    m_legacy->setTristate(!single && any);
    m_hint->clear();

    InventorySession s;
    for (const InventoryFolder &f : m_folders)
        for (const InventorySession &x : f.sessions)
            if (single && !adding && x.key == keys.first()) s = x;

    for (QWidget *w : QList<QWidget *>{m_name, m_host, m_port}) w->setEnabled(single);
    for (QWidget *w : QList<QWidget *>{m_platform, m_legacy, m_credential, m_save}) w->setEnabled(any);

    if (adding) {
        m_formTitle->setText(tr("New device in %1").arg(m_addFolder));
    } else if (single) {
        m_formTitle->setText(s.name);
    } else if (any) {
        m_formTitle->setText(tr("%1 devices: fields left \u201C(unchanged)\u201D are not touched").arg(keys.size()));
    } else {
        m_formTitle->setText(tr("Select a device to edit, or several to edit together."));
    }

    m_name->setText(single ? s.name : QString());
    m_host->setText(single ? s.host : QString());
    m_port->setValue(single && s.port != 22 ? s.port : 0);
    if (single) {
        m_platform->setCurrentIndex(qMax(0, m_platform->findData(s.platform)));
        if (!s.deviceType.isEmpty())
            m_hint->setText(s.platform.isEmpty() ? tr("Detected on connect. The map guessed %1.").arg(s.deviceType)
                                                 : tr("The map guessed %1; the platform set here wins.").arg(s.deviceType));
        m_legacy->setCheckState(s.legacy ? Qt::Checked : Qt::Unchecked);
        const int ci = m_credential->findData(s.credential);
        if (ci >= 0) m_credential->setCurrentIndex(ci);
        else m_credential->setEditText(s.credential);  // a name the vault does not list, or a locked vault
    } else {
        m_platform->setCurrentIndex(0);
        m_legacy->setCheckState(any ? Qt::PartiallyChecked : Qt::Unchecked);
        m_credential->setCurrentIndex(0);
    }
    m_hint->setVisible(!m_hint->text().isEmpty());  // no empty row under Platform
    m_filling = false;
}

void InventoryManagerDialog::beginAdd(const QString &folder) {
    {
        const QSignalBlocker block(m_tree);
        m_tree->clearSelection();
    }
    m_addFolder = folder;
    fillForm();
    m_name->setFocus();
}

void InventoryManagerDialog::save() {
    const QStringList keys = selectedKeys();
    const bool adding = !m_addFolder.isEmpty();
    // A typed credential that matches a listed name uses the name; text that
    // does not is kept as typed (a vault that is locked lists nothing).
    const auto credentialText = [this]() -> QString {
        const int i = m_credential->findText(m_credential->currentText());
        if (i >= 0) return m_credential->itemData(i).toString();
        return m_credential->currentText().trimmed();
    };

    if (adding || keys.size() == 1) {
        QJsonObject session{{QStringLiteral("name"), m_name->text().trimmed()},
                            {QStringLiteral("host"), m_host->text().trimmed()},
                            {QStringLiteral("port"), m_port->value()},
                            {QStringLiteral("platform"), m_platform->currentData().toString()},
                            {QStringLiteral("legacy"), m_legacy->checkState() == Qt::Checked},
                            {QStringLiteral("credential"), credentialText()}};
        QJsonObject op{{QStringLiteral("op"), QStringLiteral("save")},
                       {QStringLiteral("folder"), m_addFolder},
                       {QStringLiteral("key"), adding ? QString() : keys.first()},
                       {QStringLiteral("session"), session}};
        applyEdits(QJsonArray{op}, adding ? QStringList() : keys);
        return;
    }
    if (keys.isEmpty()) return;

    QJsonObject op{{QStringLiteral("op"), QStringLiteral("patch")},
                   {QStringLiteral("keys"), QJsonArray::fromStringList(keys)}};
    if (m_platform->currentData().toString() != kUnchanged)
        op.insert(QStringLiteral("platform"), m_platform->currentData().toString());
    if (m_legacy->checkState() != Qt::PartiallyChecked)
        op.insert(QStringLiteral("legacy"), m_legacy->checkState() == Qt::Checked);
    const QString cred = credentialText();
    if (cred != kUnchanged) op.insert(QStringLiteral("credential"), cred);
    if (op.size() == 2) {
        setNotice(tr("Nothing to change: every field is \u201C(unchanged)\u201D."));
        return;
    }
    applyEdits(QJsonArray{op}, keys);
}

void InventoryManagerDialog::newFolder() {
    bool ok = false;
    const QString name = QInputDialog::getText(this, tr("New folder"), tr("Folder name"), QLineEdit::Normal, {}, &ok).trimmed();
    if (!ok || name.isEmpty()) return;
    applyEdits(QJsonArray{QJsonObject{{QStringLiteral("op"), QStringLiteral("add_folder")},
                                      {QStringLiteral("name"), name}}});
}

void InventoryManagerDialog::renameFolder() {
    const QString from = selectedFolder();
    if (from.isEmpty()) return;
    bool ok = false;
    const QString to = QInputDialog::getText(this, tr("Rename folder"), tr("Folder name"), QLineEdit::Normal, from, &ok).trimmed();
    if (!ok || to.isEmpty() || to == from) return;
    applyEdits(QJsonArray{QJsonObject{{QStringLiteral("op"), QStringLiteral("rename_folder")},
                                      {QStringLiteral("folder"), from},
                                      {QStringLiteral("to"), to}}});
}

void InventoryManagerDialog::moveSelection() {
    const QStringList keys = selectedKeys();
    if (keys.isEmpty()) return;
    QMenu menu(this);
    for (const InventoryFolder &f : m_folders) menu.addAction(f.name);
    QAction *chosen = menu.exec(m_move->mapToGlobal(QPoint(0, m_move->height())));
    if (!chosen) return;
    applyEdits(QJsonArray{QJsonObject{{QStringLiteral("op"), QStringLiteral("move")},
                                      {QStringLiteral("keys"), QJsonArray::fromStringList(keys)},
                                      {QStringLiteral("to"), chosen->text()}}},
               keys);
}

void InventoryManagerDialog::deleteSelection() {
    const QStringList keys = selectedKeys();
    QJsonArray edits;
    QString question;
    if (!keys.isEmpty()) {
        question = keys.size() == 1 ? tr("Delete this device from the inventory?")
                                    : tr("Delete %1 devices from the inventory?").arg(keys.size());
        edits.append(QJsonObject{{QStringLiteral("op"), QStringLiteral("delete")},
                                 {QStringLiteral("keys"), QJsonArray::fromStringList(keys)}});
    } else if (!selectedFolder().isEmpty()) {
        question = tr("Delete folder \u201C%1\u201D and every device in it?").arg(selectedFolder());
        edits.append(QJsonObject{{QStringLiteral("op"), QStringLiteral("remove_folder")},
                                 {QStringLiteral("folder"), selectedFolder()}});
    } else {
        return;
    }
    question += QStringLiteral("\n\n") + tr("Captures already in the store are not touched.");
    if (QMessageBox::question(this, tr("Delete"), question) != QMessageBox::Yes) return;
    applyEdits(edits);
}

}  // namespace omegacat
