// app/runview.cpp
#include "runview.h"

#include <QButtonGroup>
#include <QCheckBox>
#include <QComboBox>
#include <QDir>
#include <QGridLayout>
#include <QHBoxLayout>
#include <QHeaderView>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QLabel>
#include <QLineEdit>
#include <QListWidget>
#include <QMenu>
#include <QPlainTextEdit>
#include <QProgressBar>
#include <QPushButton>
#include <QSet>
#include <QSplitter>
#include <QStackedWidget>
#include <QTableView>
#include <QTreeWidget>
#include <QVBoxLayout>

#include "panel.h"
#include "runbridge.h"
#include "theme.h"
#include "widgets.h"

namespace omegacat {

// ---------------------------------------------------------------------------
// ResultsModel

ResultsModel::ResultsModel(QObject *parent) : QAbstractTableModel(parent) {
    connect(&ThemeManager::instance(), &ThemeManager::changed, this, [this] {
        if (!m_rows.isEmpty()) emit dataChanged(index(0, 0), index(int(m_rows.size()) - 1, ColumnCount - 1));
    });
}

int ResultsModel::rowCount(const QModelIndex &parent) const { return parent.isValid() ? 0 : int(m_rows.size()); }
int ResultsModel::columnCount(const QModelIndex &parent) const { return parent.isValid() ? 0 : ColumnCount; }

QVariant ResultsModel::headerData(int section, Qt::Orientation o, int role) const {
    if (o != Qt::Horizontal || role != Qt::DisplayRole) return {};
    static const char *names[] = {"DEVICE", "TYPE", "STATE", "PLATFORM", "BYTES", "TIME", "PARSED", "DETAIL"};
    return section >= 0 && section < ColumnCount ? tr(names[section]) : QVariant();
}

QVariant ResultsModel::data(const QModelIndex &index, int role) const {
    if (!index.isValid() || index.row() >= m_rows.size()) return {};
    const RunRow &r = m_rows.at(index.row());
    const Tokens &t = ThemeManager::instance().tokens();
    if (role == Qt::DisplayRole) {
        switch (index.column()) {
        case Device: return r.display;
        case Type: return r.type;
        case State: return r.state;
        case Platform: return r.platform;
        case Bytes: return r.bytes > 0 ? humanBytes(r.bytes) : QString();
        case Time: return r.durationMs > 0 ? humanDuration(r.durationMs) : QString();
        case Parsed:
            if (r.parseStatus == QLatin1String("parsed"))
                return tr("%1 rows").arg(r.parseRecords);
            if (r.parseStatus == QLatin1String("no-match")) return tr("no match");
            return QString();
        case Detail: return r.detail;
        }
    }
    if (role == Qt::ForegroundRole) {
        if (index.column() == State) return toneColor(t, stateTone(r.state));
        if (index.column() == Parsed && r.parseStatus == QLatin1String("no-match")) return t.warning;
        if (index.column() == Detail && r.state == QLatin1String("failed")) return t.danger;
        if (index.column() == Platform || index.column() == Time || index.column() == Bytes) return t.textSecondary;
    }
    if (role == Qt::ToolTipRole) {
        if (index.column() == Parsed && !r.templateName.isEmpty())
            return tr("%1 (score %2)").arg(r.templateName).arg(r.parseScore, 0, 'f', 1);
        if (index.column() == Device && r.display != r.identity) return tr("reached as %1").arg(r.identity);
        if (index.column() == Detail) return r.detail;
    }
    if (role == Qt::TextAlignmentRole && (index.column() == Bytes || index.column() == Time))
        return int(Qt::AlignRight | Qt::AlignVCenter);
    return {};
}

void ResultsModel::clear() {
    beginResetModel();
    m_rows.clear();
    m_index.clear();
    endResetModel();
}

void ResultsModel::merge(const QVector<RunRow> &rows) {
    for (const RunRow &r : rows) {
        const auto it = m_index.constFind(r.key());
        if (it != m_index.constEnd()) {
            m_rows[*it] = r;
            emit dataChanged(index(*it, 0), index(*it, ColumnCount - 1));
            continue;
        }
        const int at = int(m_rows.size());
        beginInsertRows(QModelIndex(), at, at);
        m_rows.append(r);
        m_index.insert(r.key(), at);
        endInsertRows();
    }
}

// ---------------------------------------------------------------------------
// RunView

RunView::RunView(QWidget *parent) : QWidget(parent) {
    auto *row = new QHBoxLayout(this);
    row->setContentsMargins(12, 12, 12, 12);
    auto *split = new QSplitter(Qt::Horizontal, this);
    split->setChildrenCollapsible(false);
    split->addWidget(buildForm());
    split->addWidget(buildResults());
    split->addWidget(buildProgress());
    split->setStretchFactor(0, 0);
    split->setStretchFactor(1, 1);
    split->setStretchFactor(2, 0);
    split->setSizes({300, 760, 300});
    row->addWidget(split);
}

QWidget *RunView::buildForm() {
    auto *panel = new Panel(tr("Capture"), this);
    panel->setMinimumWidth(260);
    auto *body = panel->bodyLayout();

    // The device field takes whatever height the panel has spare: an inventory
    // of hundreds is the thing on this form that needs room.
    body->addWidget(buildDevicesField(panel), 1);

    m_types = new QListWidget(panel);
    for (const CaptureType &ct : RunBridge::types()) {
        auto *item = new QListWidgetItem(ct.type, m_types);
        item->setFlags(item->flags() | Qt::ItemIsUserCheckable);
        item->setCheckState(ct.isDefault ? Qt::Checked : Qt::Unchecked);
        item->setToolTip(ct.keep > 0 ? tr("%1 (keeps %2 versions)").arg(ct.description).arg(ct.keep) : ct.description);
    }
    m_types->setMaximumHeight(120);
    // Every type visible: the inventory tree above can take the height, and a
    // five-line checklist that scrolls hides what the capture will do.
    m_types->setMinimumHeight(qMin(120, m_types->sizeHintForRow(0) * m_types->count() + 2 * m_types->frameWidth() + 4));
    addField(body, tr("Capture types"), m_types);

    m_store = new QLineEdit(panel);
    addField(body, tr("Store"), m_store);

    auto *opts = new QHBoxLayout;
    m_hostKeys = new QComboBox(panel);
    m_hostKeys->addItem(tr("Strict host keys"), QStringLiteral("strict"));
    m_hostKeys->addItem(tr("Trust on first use"), QStringLiteral("tofu"));
    opts->addWidget(m_hostKeys, 1);
    body->addLayout(opts);
    m_legacy = new QCheckBox(tr("Legacy algorithms (SHA-1 KEX, CBC)"), panel);
    m_parse = new QCheckBox(tr("Parse ARP and MAC tables"), panel);
    m_parse->setChecked(true);
    body->addWidget(m_legacy);
    body->addWidget(m_parse);

    m_credTags = new QLineEdit(panel);
    m_credTags->setPlaceholderText(tr("(every credential)"));
    addField(body, tr("Credentials tagged"), m_credTags);

    auto *vaultRow = new QHBoxLayout;
    m_vaultState = new QLabel(panel);
    m_vaultState->setWordWrap(true);
    m_unlock = new QPushButton(tr("Unlock\u2026"), panel);
    vaultRow->addWidget(m_vaultState, 1);
    vaultRow->addWidget(m_unlock);
    body->addLayout(vaultRow);
    connect(m_unlock, &QPushButton::clicked, this, &RunView::unlockRequested);

    m_formError = new QLabel(panel);
    m_formError->setWordWrap(true);
    setTone(m_formError, QStringLiteral("danger"));
    m_formError->hide();
    body->addWidget(m_formError);

    m_start = new QPushButton(tr("Start capture"), panel);
    m_start->setProperty("primary", QStringLiteral("true"));
    m_start->setMinimumHeight(34);
    body->addWidget(m_start);
    auto *secondary = new QHBoxLayout;
    m_cancel = new QPushButton(tr("Cancel"), panel);
    m_cancel->setEnabled(false);
    m_demo = new QPushButton(tr("Run demo"), panel);
    m_demo->setToolTip(tr("Play a scripted run through these views; nothing is contacted"));
    secondary->addWidget(m_cancel);
    secondary->addWidget(m_demo);
    body->addLayout(secondary);

    connect(m_start, &QPushButton::clicked, this, &RunView::startClicked);
    connect(m_cancel, &QPushButton::clicked, this, &RunView::cancelRequested);
    connect(m_demo, &QPushButton::clicked, this, &RunView::demoRequested);
    return panel;
}

QWidget *RunView::buildResults() {
    auto *panel = new Panel(tr("Results"), this);
    m_runLabel = new QLabel(tr("No run yet"), panel);
    setTone(m_runLabel, QStringLiteral("muted"));
    panel->barLayout()->addWidget(m_runLabel);

    m_model = new ResultsModel(panel);
    m_table = new QTableView(panel);
    m_table->setModel(m_model);
    m_table->verticalHeader()->hide();
    m_table->setShowGrid(false);
    m_table->setAlternatingRowColors(true);
    m_table->setSelectionBehavior(QAbstractItemView::SelectRows);
    m_table->setSelectionMode(QAbstractItemView::SingleSelection);
    m_table->setEditTriggers(QAbstractItemView::NoEditTriggers);
    m_table->setWordWrap(false);
    m_table->verticalHeader()->setDefaultSectionSize(24);
    // Device and Detail share the slack; the short columns size to their
    // contents. Fixed pixel widths pushed Detail -- the reason a row failed --
    // off the right edge behind a scrollbar at the window's default size.
    auto *hh = m_table->horizontalHeader();
    hh->setStretchLastSection(false);
    for (int c = 0; c < ResultsModel::ColumnCount; ++c)
        hh->setSectionResizeMode(c, (c == ResultsModel::Device || c == ResultsModel::Detail)
                                        ? QHeaderView::Stretch
                                        : QHeaderView::ResizeToContents);
    m_table->setTextElideMode(Qt::ElideRight);
    panel->bodyLayout()->addWidget(m_table, 1);

    auto *foot = new QHBoxLayout;
    m_selection = new QLabel(panel);
    m_selection->setWordWrap(true);
    setTone(m_selection, QStringLiteral("secondary"));
    m_open = new QPushButton(tr("Open in store"), panel);
    m_open->setEnabled(false);
    foot->addWidget(m_selection, 1);
    foot->addWidget(m_open);
    panel->bodyLayout()->addLayout(foot);

    connect(m_table->selectionModel(), &QItemSelectionModel::selectionChanged, this, &RunView::selectionChanged);
    connect(m_open, &QPushButton::clicked, this, [this] {
        const auto rows = m_table->selectionModel()->selectedRows();
        if (rows.isEmpty()) return;
        const RunRow &r = m_model->rowAt(rows.first().row());
        if (r.openable()) emit openInStore(r.name, r.type, r.file);
    });
    connect(m_table, &QTableView::doubleClicked, m_open, &QPushButton::click);
    return panel;
}

QWidget *RunView::buildProgress() {
    auto *col = new QWidget(this);
    col->setMinimumWidth(260);
    auto *v = new QVBoxLayout(col);
    v->setContentsMargins(0, 0, 0, 0);
    v->setSpacing(10);

    auto *panel = new Panel(tr("Progress"), col);
    m_bar = new QProgressBar(panel);
    m_bar->setRange(0, 1);
    m_bar->setValue(0);
    m_bar->setFixedHeight(10);
    panel->bodyLayout()->addWidget(m_bar);
    m_barText = new QLabel(tr("Idle"), panel);
    setTone(m_barText, QStringLiteral("secondary"));
    panel->bodyLayout()->addWidget(m_barText);

    auto *grid = new QGridLayout;
    grid->setSpacing(8);
    const std::tuple<const char *, const char *, const char *> stats[] = {
        {"stored", "Stored", "success"},        {"unchanged", "Unchanged", "secondary"},
        {"not_applicable", "N/A", "warning"},   {"failed", "Failed", "danger"},
        {"host_keys", "New keys", "info"},      {"rejections", "Auth rejects", "danger"},
    };
    int i = 0;
    for (const auto &[key, caption, tone] : stats) {
        auto *box = new StatBox(tr(caption), QString::fromLatin1(tone), panel);
        m_stats.insert(QString::fromLatin1(key), box);
        grid->addWidget(box, i / 2, i % 2);
        ++i;
    }
    panel->bodyLayout()->addLayout(grid);
    v->addWidget(panel);

    auto *dec = new Panel(tr("Decisions"), col);
    m_decisions = new QListWidget(dec);
    m_decisions->setWordWrap(true);
    m_decisions->setResizeMode(QListView::Adjust);
    m_decisions->setHorizontalScrollBarPolicy(Qt::ScrollBarAlwaysOff);
    m_decisions->setSpacing(2);
    m_decisions->setSelectionMode(QAbstractItemView::NoSelection);
    dec->bodyLayout()->addWidget(m_decisions, 1);
    v->addWidget(dec, 1);
    return col;
}

// ---------------------------------------------------------------------------
// Devices: a typed list, or the inventory

namespace {

// Session rows carry their key in KeyRole on column 0.
constexpr int KeyRole = Qt::UserRole;
constexpr int PlatformRole = Qt::UserRole + 1;

bool isSession(const QTreeWidgetItem *item) { return item && item->parent() != nullptr; }

}  // namespace

QWidget *RunView::buildDevicesField(QWidget *panel) {
    auto *field = new QWidget(panel);
    auto *box = new QVBoxLayout(field);
    box->setContentsMargins(0, 0, 0, 0);
    box->setSpacing(4);

    auto *head = new QHBoxLayout;
    head->setSpacing(4);
    head->addWidget(makeCaption(tr("Devices").toUpper(), "fieldLabel", 8, field));
    head->addStretch(1);
    m_listBtn = new QPushButton(tr("List"), field);
    m_inventoryBtn = new QPushButton(tr("Inventory"), field);
    auto *group = new QButtonGroup(field);
    for (QPushButton *b : {m_listBtn, m_inventoryBtn}) {
        b->setCheckable(true);
        b->setFocusPolicy(Qt::TabFocus);
        group->addButton(b);
        head->addWidget(b);
    }
    m_listBtn->setChecked(true);
    box->addLayout(head);

    m_sourceStack = new QStackedWidget(field);

    m_devices = new QPlainTextEdit(m_sourceStack);
    m_devices->setPlaceholderText(QStringLiteral("172.16.1.2\nlab-r1.lab.local\n# comments are fine"));
    m_devices->setMinimumHeight(90);
    m_sourceStack->addWidget(m_devices);

    auto *invPage = new QWidget(m_sourceStack);
    auto *inv = new QVBoxLayout(invPage);
    inv->setContentsMargins(0, 0, 0, 0);
    inv->setSpacing(4);
    auto *filterRow = new QHBoxLayout;
    filterRow->setSpacing(4);
    m_inventoryFilter = new QLineEdit(invPage);
    m_inventoryFilter->setPlaceholderText(tr("Filter"));
    m_inventoryFilter->setToolTip(tr("Matches name, host, platform or folder"));
    m_inventoryFilter->setClearButtonEnabled(true);
    m_inventoryNone = new QPushButton(tr("None"), invPage);
    m_inventoryNone->setToolTip(tr("Untick every device, including any the filter hides"));
    auto *importBtn = new QPushButton(tr("Import map\u2026"), invPage);
    importBtn->setToolTip(tr("Add the devices in an omegamaps map.json to the inventory"));
    filterRow->addWidget(m_inventoryFilter, 1);
    filterRow->addWidget(m_inventoryNone);
    filterRow->addWidget(importBtn);
    inv->addLayout(filterRow);

    m_inventory = new QTreeWidget(invPage);
    // Two columns: the form is ~300 px wide, and a third squeezed Name -- the
    // column somebody picks by -- to an ellipsis. Platform is in the tooltip
    // and still matches the filter.
    m_inventory->setColumnCount(2);
    m_inventory->setHeaderLabels({tr("Name"), tr("Host")});
    m_inventory->setRootIsDecorated(true);
    m_inventory->setIndentation(14);
    m_inventory->setUniformRowHeights(true);
    m_inventory->setSelectionMode(QAbstractItemView::NoSelection);
    m_inventory->setContextMenuPolicy(Qt::CustomContextMenu);
    m_inventory->header()->setStretchLastSection(false);
    m_inventory->header()->setSectionResizeMode(0, QHeaderView::Stretch);
    m_inventory->header()->setSectionResizeMode(1, QHeaderView::ResizeToContents);
    m_inventory->setMinimumHeight(140);
    inv->addWidget(m_inventory, 1);
    m_sourceStack->addWidget(invPage);
    box->addWidget(m_sourceStack, 1);

    m_sourceHint = new QLabel(field);
    m_sourceHint->setProperty("tone", QStringLiteral("muted"));
    m_sourceHint->setWordWrap(true);
    box->addWidget(m_sourceHint);

    connect(m_listBtn, &QPushButton::clicked, this, [this] { setSource(Source::List); });
    connect(m_inventoryBtn, &QPushButton::clicked, this, [this] { setSource(Source::Inventory); });
    connect(importBtn, &QPushButton::clicked, this, &RunView::importMapRequested);
    connect(m_inventoryFilter, &QLineEdit::textChanged, this, [this] { applyInventoryFilter(); });
    connect(m_inventory, &QTreeWidget::itemChanged, this, &RunView::inventoryItemChanged);
    connect(m_inventoryNone, &QPushButton::clicked, this, [this] {
        m_syncingChecks = true;
        for (int i = 0; i < m_inventory->topLevelItemCount(); ++i) {
            QTreeWidgetItem *folder = m_inventory->topLevelItem(i);
            for (int j = 0; j < folder->childCount(); ++j) folder->child(j)->setCheckState(0, Qt::Unchecked);
        }
        m_syncingChecks = false;
        refreshFolderStates();
        updateInventoryCount();
    });
    connect(m_inventory, &QTreeWidget::customContextMenuRequested, this, [this](const QPoint &pos) {
        QTreeWidgetItem *item = m_inventory->itemAt(pos);
        if (!item) return;
        QTreeWidgetItem *folder = isSession(item) ? item->parent() : item;
        QMenu menu(m_inventory);
        const QString key = isSession(item) ? item->data(0, KeyRole).toString() : QString();
        QAction *edit = menu.addAction(isSession(item) ? tr("Edit \u201C%1\u201D\u2026").arg(item->text(0))
                                                       : tr("Manage inventory\u2026"));
        menu.addSeparator();
        QAction *remove = menu.addAction(tr("Remove folder \u201C%1\u201D\u2026").arg(folder->text(0)));
        QAction *chosen = menu.exec(m_inventory->viewport()->mapToGlobal(pos));
        if (chosen == edit) emit manageInventoryRequested(key);
        else if (chosen == remove) emit removeFolderRequested(folder->text(0));
    });

    setSource(Source::List);
    return field;
}

RunView::Source RunView::source() const {
    return m_sourceStack->currentIndex() == 1 ? Source::Inventory : Source::List;
}

void RunView::setSource(Source s) {
    const bool inv = s == Source::Inventory;
    const bool changed = (m_sourceStack->currentIndex() == 1) != inv;
    m_sourceStack->setCurrentIndex(inv ? 1 : 0);
    (inv ? m_inventoryBtn : m_listBtn)->setChecked(true);
    updateInventoryCount();
    if (changed) emit sourceChanged(s);
}

void RunView::setInventory(const QString &path, const QVector<InventoryFolder> &folders,
                           const QHash<QString, QString> &keyMap) {
    QSet<QString> keep;
    for (const QString &k : checkedKeys()) {
        const QString now = keyMap.value(k, k);
        if (!now.isEmpty()) keep.insert(now);
    }
    m_inventoryPath = path;

    const QSignalBlocker block(m_inventory);
    m_inventory->clear();
    for (const InventoryFolder &f : folders) {
        auto *folder = new QTreeWidgetItem(m_inventory, {f.name});
        // Not ItemIsAutoTristate: Qt's version ticks every child, including
        // ones the filter hides. inventoryItemChanged does it for visible ones.
        folder->setFlags(folder->flags() | Qt::ItemIsUserCheckable);
        for (const InventorySession &s : f.sessions) {
            const QString host = s.port > 0 && s.port != 22 ? QStringLiteral("%1:%2").arg(s.host).arg(s.port) : s.host;
            auto *item = new QTreeWidgetItem(folder, {s.name, host});
            item->setData(0, KeyRole, s.key);
            item->setData(0, PlatformRole, s.platform.isEmpty() ? s.deviceType : s.platform);
            item->setFlags(item->flags() | Qt::ItemIsUserCheckable);
            item->setCheckState(0, keep.contains(s.key) ? Qt::Checked : Qt::Unchecked);
            QString tip = s.name + QLatin1Char('\n') + host;
            if (!s.platform.isEmpty())
                tip += QLatin1Char('\n') + tr("platform: %1").arg(s.platform);
            else if (!s.deviceType.isEmpty())
                tip += QLatin1Char('\n') + tr("platform: detected (map guessed %1)").arg(s.deviceType);
            if (s.legacy) tip += QLatin1Char('\n') + tr("legacy SSH algorithms");
            if (!s.credential.isEmpty()) tip += QLatin1Char('\n') + tr("credential: %1").arg(s.credential);
            if (!s.transport.isEmpty() && s.transport != QLatin1String("ssh"))
                tip += QLatin1Char('\n') + tr("%1 session: capture connects over SSH and will skip it").arg(s.transport);
            item->setToolTip(0, tip);
            item->setToolTip(1, tip);
        }
        folder->setCheckState(0, Qt::Unchecked);
        folder->setExpanded(true);
    }
    applyInventoryFilter();  // also derives the folder states
    updateInventoryCount();
}

void RunView::inventoryItemChanged(QTreeWidgetItem *item, int column) {
    if (column != 0 || m_syncingChecks) return;
    if (!isSession(item)) {
        // A folder clicked: its new state goes to the devices a person can see
        // in it. Ticking a filtered folder must not select what the filter hid.
        const Qt::CheckState target = item->checkState(0) == Qt::Unchecked ? Qt::Unchecked : Qt::Checked;
        m_syncingChecks = true;
        for (int j = 0; j < item->childCount(); ++j)
            if (!item->child(j)->isHidden()) item->child(j)->setCheckState(0, target);
        m_syncingChecks = false;
    }
    refreshFolderStates();
    updateInventoryCount();
}

void RunView::refreshFolderStates() {
    // A folder shows the state of its visible devices, so clicking it always
    // does what its box shows: a checked box unticks them, anything else ticks.
    m_syncingChecks = true;
    for (int i = 0; i < m_inventory->topLevelItemCount(); ++i) {
        QTreeWidgetItem *folder = m_inventory->topLevelItem(i);
        int visible = 0, checked = 0;
        for (int j = 0; j < folder->childCount(); ++j) {
            const QTreeWidgetItem *item = folder->child(j);
            if (item->isHidden()) continue;
            ++visible;
            if (item->checkState(0) == Qt::Checked) ++checked;
        }
        folder->setCheckState(0, visible == 0 || checked == 0 ? Qt::Unchecked
                                 : checked == visible          ? Qt::Checked
                                                               : Qt::PartiallyChecked);
    }
    m_syncingChecks = false;
}

QStringList RunView::checkedKeys() const {
    QStringList out;
    QSet<QString> seen;
    for (int i = 0; i < m_inventory->topLevelItemCount(); ++i) {
        QTreeWidgetItem *folder = m_inventory->topLevelItem(i);
        for (int j = 0; j < folder->childCount(); ++j) {
            QTreeWidgetItem *item = folder->child(j);
            const QString key = item->data(0, KeyRole).toString();
            if (item->checkState(0) == Qt::Checked && !key.isEmpty() && !seen.contains(key)) {
                seen.insert(key);
                out.append(key);
            }
        }
    }
    return out;
}

bool RunView::setSessionChecked(const QString &key, bool checked) {
    bool found = false;
    for (int i = 0; i < m_inventory->topLevelItemCount(); ++i) {
        QTreeWidgetItem *folder = m_inventory->topLevelItem(i);
        for (int j = 0; j < folder->childCount(); ++j) {
            QTreeWidgetItem *item = folder->child(j);
            if (item->data(0, KeyRole).toString() == key) {
                item->setCheckState(0, checked ? Qt::Checked : Qt::Unchecked);
                found = true;
            }
        }
    }
    return found;
}

void RunView::applyInventoryFilter() {
    const QString needle = m_inventoryFilter->text().trimmed();
    for (int i = 0; i < m_inventory->topLevelItemCount(); ++i) {
        QTreeWidgetItem *folder = m_inventory->topLevelItem(i);
        const bool folderHit = !needle.isEmpty() && folder->text(0).contains(needle, Qt::CaseInsensitive);
        int visible = 0;
        for (int j = 0; j < folder->childCount(); ++j) {
            QTreeWidgetItem *item = folder->child(j);
            bool hit = needle.isEmpty() || folderHit;
            for (int col = 0; col < 2 && !hit; ++col) hit = item->text(col).contains(needle, Qt::CaseInsensitive);
            if (!hit) hit = item->data(0, PlatformRole).toString().contains(needle, Qt::CaseInsensitive);
            item->setHidden(!hit);
            if (hit) ++visible;
        }
        folder->setHidden(!needle.isEmpty() && visible == 0 && !folderHit);
    }
    refreshFolderStates();
    updateInventoryCount();
}

void RunView::updateInventoryCount() {
    setTone(m_sourceHint, QStringLiteral("muted"));
    if (source() == Source::List) {
        m_sourceHint->setText(tr("One per line; host:port for a non-standard port."));
        return;
    }
    int total = 0;
    for (int i = 0; i < m_inventory->topLevelItemCount(); ++i) total += m_inventory->topLevelItem(i)->childCount();
    if (total == 0) {
        m_sourceHint->setText(tr("No inventory yet. Import an omegamaps map.json to fill it."));
        return;
    }
    const int ticked = int(checkedKeys().size());
    int hiddenTicked = 0;
    for (int i = 0; i < m_inventory->topLevelItemCount(); ++i) {
        QTreeWidgetItem *folder = m_inventory->topLevelItem(i);
        for (int j = 0; j < folder->childCount(); ++j) {
            const QTreeWidgetItem *item = folder->child(j);
            if ((item->isHidden() || folder->isHidden()) && item->checkState(0) == Qt::Checked) ++hiddenTicked;
        }
    }
    QString text = tr("%1 of %2 selected").arg(ticked).arg(total);
    // What Start will capture includes ticks the filter is hiding; say so
    // where the count is read, not only in the results after the run.
    if (hiddenTicked > 0) text += tr("  \u00B7  %1 hidden by the filter").arg(hiddenTicked);
    m_sourceHint->setText(text);
    if (hiddenTicked > 0) setTone(m_sourceHint, QStringLiteral("warning"));
}

QByteArray RunView::request() const {
    QJsonObject o = QJsonDocument::fromJson(RunBridge::defaults()).object();
    if (source() == Source::Inventory) {
        o.insert(QStringLiteral("devices"), QJsonArray());
        o.insert(QStringLiteral("session_file"), m_inventoryPath);
        o.insert(QStringLiteral("session_keys"), QJsonArray::fromStringList(checkedKeys()));
    } else {
        o.insert(QStringLiteral("devices"), QJsonArray::fromStringList({m_devices->toPlainText()}));
    }
    QStringList types;
    for (int i = 0; i < m_types->count(); ++i)
        if (m_types->item(i)->checkState() == Qt::Checked) types.append(m_types->item(i)->text());
    o.insert(QStringLiteral("types"), QJsonArray::fromStringList(types));
    o.insert(QStringLiteral("store_path"), QDir::fromNativeSeparators(m_store->text().trimmed()));
    o.insert(QStringLiteral("host_keys"), m_hostKeys->currentData().toString());
    o.insert(QStringLiteral("legacy"), m_legacy->isChecked());
    o.insert(QStringLiteral("no_parse"), !m_parse->isChecked());
    const QStringList tags = splitList(m_credTags->text());
    if (!tags.isEmpty()) o.insert(QStringLiteral("cred_tags"), QJsonArray::fromStringList(tags));
    for (auto it = m_requestExtras.constBegin(); it != m_requestExtras.constEnd(); ++it) o.insert(it.key(), it.value());
    return QJsonDocument(o).toJson(QJsonDocument::Compact);
}

void RunView::setStorePath(const QString &path) { m_store->setText(QDir::toNativeSeparators(path)); }

void RunView::applyCaptureDefaults(const CaptureDefaults &d) {
    for (int i = 0; i < m_types->count(); ++i)
        m_types->item(i)->setCheckState(d.types.contains(m_types->item(i)->text()) ? Qt::Checked : Qt::Unchecked);
    const int hk = m_hostKeys->findData(d.hostKeys);
    if (hk >= 0) m_hostKeys->setCurrentIndex(hk);
    m_legacy->setChecked(d.legacy);
    m_parse->setChecked(d.parse);
    m_credTags->setText(d.credentialTags);
}

void RunView::setVaultState(const QString &text, const QString &tone, const QString &action) {
    m_vaultState->setText(text);
    setTone(m_vaultState, tone);
    m_unlock->setText(action);
}

void RunView::startClicked() {
    // Said here in the form's own words; the engine's version of this names
    // a request field the person never saw.
    if (source() == Source::Inventory && checkedKeys().isEmpty()) {
        m_formError->setText(tr("Tick at least one device in the inventory."));
        m_formError->setVisible(true);
        return;
    }
    const QByteArray req = request();
    const auto problems = RunBridge::validate(req);
    QStringList lines;
    for (const auto &[field, message] : problems) lines.append(field + QStringLiteral(": ") + message);
    m_formError->setText(lines.join(QLatin1Char('\n')));
    m_formError->setVisible(!lines.isEmpty());
    if (lines.isEmpty()) emit captureRequested(req);
}

void RunView::setRunning(bool running) {
    m_start->setEnabled(!running);
    m_demo->setEnabled(!running);
    m_cancel->setEnabled(running);
}

void RunView::runStarted(const QString &label) {
    m_model->clear();
    m_decisions->clear();
    m_statValues.clear();
    for (StatBox *b : std::as_const(m_stats)) b->setValue(0);
    m_formError->hide();
    m_runLabel->setText(label);
    m_bar->setRange(0, 1);
    m_bar->setValue(0);
    m_barText->setText(tr("Starting\u2026"));
    setRunning(true);
}

void RunView::addRows(const QVector<RunRow> &rows) { m_model->merge(rows); }

void RunView::addDecisions(const QVector<RunDecision> &decisions) {
    const Tokens &t = ThemeManager::instance().tokens();
    for (const RunDecision &d : decisions) {
        auto *item = new QListWidgetItem(d.text, m_decisions);
        const bool bad = d.kind.contains(QLatin1String("fail")) || d.kind == QLatin1String("auth-reject") ||
                         d.kind == QLatin1String("cred-parked");
        item->setForeground(bad ? t.danger
                                : d.kind == QLatin1String("not-applicable") ? t.warning
                                : d.kind == QLatin1String("host-key-new")   ? t.info
                                                                            : t.textSecondary);
    }
    m_decisions->scrollToBottom();
}

void RunView::setProgress(const RunProgress &p) {
    m_bar->setRange(0, qMax(1, p.total));
    m_bar->setValue(p.settled);
    const QString devices = p.counts.devices == 1 ? tr("1 device") : tr("%1 devices").arg(p.counts.devices);
    m_barText->setText(tr("%1 of %2 settled  \u00B7  %3  \u00B7  %4")
                           .arg(p.settled).arg(p.total).arg(devices, humanDuration(p.elapsedMs)));
    const std::pair<const char *, int> values[] = {
        {"stored", p.counts.stored},       {"unchanged", p.counts.unchanged},
        {"not_applicable", p.counts.notApplicable}, {"failed", p.counts.failed},
        {"host_keys", p.counts.newHostKeys}, {"rejections", p.counts.credRejections},
    };
    for (const auto &[key, v] : values) {
        m_stats.value(QString::fromLatin1(key))->setValue(v);
        m_statValues.insert(QString::fromLatin1(key), v);
    }
}

int RunView::statValue(const QString &name) const { return m_statValues.value(name, -1); }

void RunView::runFinished(const RunResult &result) {
    setRunning(false);
    QString text = tr("%1 %2").arg(result.kind == QLatin1String("demo") ? tr("Demo") : tr("Capture"), result.state);
    if (!result.error.isEmpty()) text += QStringLiteral(": ") + result.error;
    m_runLabel->setText(text);
    if (!result.error.isEmpty()) {
        m_formError->setText(result.error);
        m_formError->show();
    }
}

void RunView::selectionChanged() {
    const auto rows = m_table->selectionModel()->selectedRows();
    if (rows.isEmpty()) {
        m_selection->clear();
        m_open->setEnabled(false);
        return;
    }
    const RunRow &r = m_model->rowAt(rows.first().row());
    QString text = QStringLiteral("%1 / %2").arg(r.display, r.type);
    if (!r.command.isEmpty()) text += QStringLiteral("  \u00B7  ") + r.command;
    if (!r.templateName.isEmpty())
        text += tr("  \u00B7  %1 (%2)").arg(r.templateName).arg(r.parseScore, 0, 'f', 1);
    if (r.display != r.identity) text += tr("  \u00B7  reached as %1").arg(r.identity);
    m_selection->setText(text);
    m_open->setEnabled(r.openable());
}

}  // namespace omegacat
