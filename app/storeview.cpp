// app/storeview.cpp
#include "storeview.h"

#include <QButtonGroup>
#include <QDir>
#include <QFileInfo>
#include <QFontDatabase>
#include <QHBoxLayout>
#include <QHeaderView>
#include <QJsonArray>
#include <QJsonObject>
#include <QLabel>
#include <QPlainTextEdit>
#include <QPushButton>
#include <QSplitter>
#include <QStackedWidget>
#include <QTableWidget>
#include <QTextBlock>
#include <QTreeWidget>
#include <QVBoxLayout>
#include <algorithm>

#include "exporting.h"
#include "widgets.h"
#include "filedialogs.h"
#include <QSet>

#include "panel.h"
#include "runtypes.h"
#include "theme.h"

namespace omegacat {

namespace {

QTableWidget *plainTable(int columns, const QStringList &headers, QWidget *parent) {
    auto *t = new QTableWidget(0, columns, parent);
    t->setHorizontalHeaderLabels(headers);
    t->verticalHeader()->hide();
    t->setShowGrid(false);
    t->setAlternatingRowColors(true);
    t->setEditTriggers(QAbstractItemView::NoEditTriggers);
    t->setSelectionBehavior(QAbstractItemView::SelectRows);
    t->setSelectionMode(QAbstractItemView::SingleSelection);  // History widens this to two rows
    t->verticalHeader()->setDefaultSectionSize(22);
    t->horizontalHeader()->setStretchLastSection(true);
    return t;
}

// One column takes the slack and the rest size to their contents, so a narrow
// side panel shows every column instead of scrolling sideways.
void fitColumns(QTableWidget *t, int stretch) {
    auto *h = t->horizontalHeader();
    h->setStretchLastSection(false);
    for (int c = 0; c < t->columnCount(); ++c)
        h->setSectionResizeMode(c, c == stretch ? QHeaderView::Stretch : QHeaderView::ResizeToContents);
    t->setHorizontalScrollBarPolicy(Qt::ScrollBarAlwaysOff);
}

QString when(const QDateTime &d) {
    return d.isValid() ? d.toLocalTime().toString(QStringLiteral("MM-dd HH:mm")) : QString();
}

QString cell(const QJsonValue &v) {
    if (v.isArray()) {
        QStringList parts;
        for (const QJsonValue &e : v.toArray()) parts.append(e.toString());
        return parts.join(QStringLiteral(", "));
    }
    return v.toString();
}

}  // namespace

StoreView::StoreView(QWidget *parent) : QWidget(parent) {
    auto *row = new QHBoxLayout(this);
    row->setContentsMargins(12, 12, 12, 12);
    auto *split = new QSplitter(Qt::Horizontal, this);
    split->setChildrenCollapsible(false);
    split->addWidget(buildDevices());
    split->addWidget(buildContent());
    split->addWidget(buildDetails());
    split->setStretchFactor(1, 1);
    split->setSizes({240, 760, 340});
    row->addWidget(split);
    connect(&ThemeManager::instance(), &ThemeManager::changed, this, [this] { markLine(m_content, m_line); });
}

QWidget *StoreView::buildDevices() {
    auto *panel = new Panel(tr("Devices"), this);
    panel->setMinimumWidth(200);
    auto *refreshBtn = new QPushButton(tr("Refresh"), panel);
    panel->barLayout()->addWidget(refreshBtn);
    connect(refreshBtn, &QPushButton::clicked, this, &StoreView::refresh);

    m_devices = new QTreeWidget(panel);
    m_devices->setHeaderLabels({tr("DEVICE"), tr("PLATFORM")});
    m_devices->setRootIsDecorated(false);
    m_devices->header()->setStretchLastSection(false);
    m_devices->header()->setSectionResizeMode(0, QHeaderView::Stretch);
    m_devices->header()->setSectionResizeMode(1, QHeaderView::ResizeToContents);
    m_devices->setHorizontalScrollBarPolicy(Qt::ScrollBarAlwaysOff);
    panel->bodyLayout()->addWidget(m_devices, 1);
    m_unreadable = new QLabel(panel);
    m_unreadable->setWordWrap(true);
    setTone(m_unreadable, QStringLiteral("warning"));
    m_unreadable->hide();
    panel->bodyLayout()->addWidget(m_unreadable);
    connect(m_devices, &QTreeWidget::itemSelectionChanged, this, &StoreView::deviceSelected);
    return panel;
}

QWidget *StoreView::buildContent() {
    auto *panel = new Panel(tr("Capture"), this);
    m_contentTitle = new ElidedLabel(tr("Select a device"), panel);
    // Preferred, over ElidedLabel's small minimum: the whole title when there is
    // room, elided down to a sliver when the buttons need it. Right-aligned so
    // an elided title still sits against what it describes.
    m_contentTitle->setSizePolicy(QSizePolicy::Preferred, QSizePolicy::Preferred);
    m_contentTitle->setAlignment(Qt::AlignRight | Qt::AlignVCenter);
    setTone(m_contentTitle, QStringLiteral("secondary"));
    panel->barLayout()->addWidget(m_contentTitle);
    m_rawBtn = new QPushButton(tr("Raw"), panel);
    m_parsedBtn = new QPushButton(tr("Parsed"), panel);
    m_diffBtn = new QPushButton(tr("Diff"), panel);
    m_diffBtn->setToolTip(tr("Compare with the previous stored version, or select two versions in History"));
    auto *group = new QButtonGroup(panel);
    for (QPushButton *b : {m_rawBtn, m_parsedBtn, m_diffBtn}) {
        b->setCheckable(true);
        group->addButton(b);
        panel->barLayout()->addWidget(b);
    }
    // Copy and Export sit before the mode buttons' neighbour, the Lab: they
    // act on what is shown, whichever mode that is.
    m_copyBtn = new QPushButton(tr("Copy"), panel);
    m_copyBtn->setToolTip(tr("Copy what is shown: the capture as stored, the parsed records as columns, or the diff"));
    m_copyBtn->setEnabled(false);
    panel->barLayout()->addWidget(m_copyBtn);
    connect(m_copyBtn, &QPushButton::clicked, this, [this] { copyToClipboard(m_copyBtn, copyText()); });
    m_exportBtn = new QPushButton(tr("Export CSV\u2026"), panel);
    m_exportBtn->setToolTip(tr("Save the parsed records as a CSV file"));
    m_exportBtn->setEnabled(false);
    panel->barLayout()->addWidget(m_exportBtn);
    connect(m_exportBtn, &QPushButton::clicked, this, [this] {
        if (!m_shownParse.valid) return;
        const QString stamp = QFileInfo(m_file).completeBaseName();
        const QString suggested = QDir(exportDirectory()).filePath(exportFileName({m_device, m_type, stamp}, QStringLiteral("csv")));
        const QString path = pickSaveFile(this, tr("Export parsed records"), suggested, tr("CSV files (*.csv)"));
        if (path.isEmpty()) return;
        QString err;
        if (exportParsed(path, &err)) {
            setExportDirectory(path);
            m_contentTitle->setText(tr("Exported %1 records to %2").arg(m_shownParse.records.size()).arg(QDir::toNativeSeparators(path)));
        } else {
            m_contentTitle->setText(err);
        }
    });

    m_labBtn = new QPushButton(tr("Template Lab"), panel);
    m_labBtn->setToolTip(tr("Open this output in the Template Lab to write or fix the template that parses it"));
    m_labBtn->setEnabled(false);
    panel->barLayout()->addWidget(m_labBtn);
    connect(m_labBtn, &QPushButton::clicked, this, [this] {
        if (!m_file.isEmpty()) emit openInLab(m_device, m_type, m_file);
    });
    m_rawBtn->setChecked(true);
    m_parsedBtn->setEnabled(false);
    m_diffBtn->setEnabled(false);
    connect(m_rawBtn, &QPushButton::clicked, this, [this] { showMode(Mode::Raw); });
    connect(m_parsedBtn, &QPushButton::clicked, this, [this] { showMode(Mode::Parsed); });
    connect(m_diffBtn, &QPushButton::clicked, this, [this] { showMode(Mode::Diff); });

    m_stack = new QStackedWidget(panel);
    m_content = new QPlainTextEdit(panel);
    m_content->setProperty("role", QStringLiteral("content"));
    m_content->setReadOnly(true);
    m_content->setLineWrapMode(QPlainTextEdit::NoWrap);
    m_content->setFont(QFontDatabase::systemFont(QFontDatabase::FixedFont));
    m_stack->addWidget(m_content);

    auto *parsedPage = new QWidget(panel);
    auto *pv = new QVBoxLayout(parsedPage);
    pv->setContentsMargins(0, 0, 0, 0);
    m_parseInfo = new QLabel(parsedPage);
    m_parseInfo->setWordWrap(true);
    pv->addWidget(m_parseInfo);
    m_parsed = plainTable(0, {}, parsedPage);
    pv->addWidget(m_parsed, 1);
    m_stack->addWidget(parsedPage);

    auto *diffPage = new QWidget(panel);
    auto *dv = new QVBoxLayout(diffPage);
    dv->setContentsMargins(0, 0, 0, 0);
    m_diffInfo = new QLabel(diffPage);
    m_diffInfo->setWordWrap(true);
    dv->addWidget(m_diffInfo);
    m_diffView = new QPlainTextEdit(diffPage);
    m_diffView->setProperty("role", QStringLiteral("content"));
    m_diffView->setReadOnly(true);
    m_diffView->setLineWrapMode(QPlainTextEdit::NoWrap);
    m_diffView->setFont(QFontDatabase::systemFont(QFontDatabase::FixedFont));
    dv->addWidget(m_diffView, 1);
    m_stack->addWidget(diffPage);

    panel->bodyLayout()->addWidget(m_stack, 1);
    return panel;
}

QWidget *StoreView::buildDetails() {
    auto *col = new QWidget(this);
    col->setMinimumWidth(280);
    auto *v = new QVBoxLayout(col);
    v->setContentsMargins(0, 0, 0, 0);
    v->setSpacing(10);

    auto *types = new Panel(tr("Types"), col);
    m_types = plainTable(4, {tr("TYPE"), tr("VERS"), tr("TRIES"), tr("LAST")}, types);
    fitColumns(m_types, 0);
    types->bodyLayout()->addWidget(m_types);
    v->addWidget(types, 2);

    auto *hist = new Panel(tr("History"), col);
    m_history = plainTable(3, {tr("WHEN"), tr("RESULT"), tr("BYTES")}, hist);
    fitColumns(m_history, 0);
    hist->bodyLayout()->addWidget(m_history);
    v->addWidget(hist, 3);

    connect(m_types, &QTableWidget::itemSelectionChanged, this, &StoreView::typeSelected);
    m_history->setSelectionMode(QAbstractItemView::ExtendedSelection);
    connect(m_history, &QTableWidget::itemSelectionChanged, this, &StoreView::historySelected);
    return col;
}

void StoreView::setStore(const Store *store) {
    m_store = store;
    refresh();
}

void StoreView::refresh() {
    const QString keep = m_device;
    m_devices->clear();
    m_types->setRowCount(0);
    m_history->setRowCount(0);
    m_content->clear();
    m_parsed->setRowCount(0);
    // Everything that describes the old selection goes, including the page
    // not currently shown: a refreshed view left on Diff or Parsed would
    // otherwise keep showing the previous file under a reset title.
    m_parseInfo->clear();
    m_diffView->clear();
    m_diffView->setExtraSelections({});
    m_diffInfo->clear();
    m_diffOlder.clear();
    m_diffNewer.clear();
    m_parsedBtn->setEnabled(false);
    m_diffBtn->setEnabled(false);
    m_labBtn->setEnabled(false);
    m_shownParse = StoreParsed();
    showMode(Mode::Raw);
    m_unreadable->hide();
    if (!m_store || !m_store->isOpen()) {
        m_contentTitle->setText(tr("No store open"));
        return;
    }
    QVector<StoreDevice> devs;
    QStringList unreadable;
    QString err;
    if (!m_store->devices(&devs, &unreadable, &err)) {
        m_contentTitle->setText(err);
        return;
    }
    // Before the loop, not after it: re-selecting the kept device loads its
    // file and titles the panel with it, and a title set afterwards would
    // overwrite that with "Select a device" over a file that is showing.
    m_contentTitle->setText(devs.isEmpty() ? tr("The store is empty") : tr("Select a device"));
    for (const StoreDevice &d : devs) {
        auto *item = new QTreeWidgetItem(m_devices, {d.canonical, d.platform});
        item->setToolTip(0, tr("aliases: %1\nlast seen %2").arg(d.aliases.join(QStringLiteral(", ")), when(d.lastSeen)));
        if (d.canonical == keep) m_devices->setCurrentItem(item);
    }
    if (!unreadable.isEmpty()) {
        m_unreadable->setText(tr("Unreadable: %1").arg(unreadable.join(QStringLiteral(", "))));
        m_unreadable->show();
    }
}

void StoreView::deviceSelected() {
    const auto items = m_devices->selectedItems();
    m_types->setRowCount(0);
    m_history->setRowCount(0);
    if (items.isEmpty() || !m_store) return;
    m_device = items.first()->text(0);
    QVector<StoreType> types;
    QString err;
    if (!m_store->types(m_device, &types, &err)) {
        m_contentTitle->setText(err);
        return;
    }
    m_types->blockSignals(true);
    m_types->setRowCount(int(types.size()));
    for (int i = 0; i < types.size(); ++i) {
        const StoreType &t = types.at(i);
        auto *name = new QTableWidgetItem(t.type);
        name->setData(Qt::UserRole, t.file);
        name->setData(Qt::UserRole + 1, t.sha256);
        m_types->setItem(i, 0, name);
        m_types->setItem(i, 1, new QTableWidgetItem(QString::number(t.stored)));
        m_types->setItem(i, 2, new QTableWidgetItem(QString::number(t.attempts)));
        m_types->setItem(i, 3, new QTableWidgetItem(when(t.last)));
    }
    m_types->blockSignals(false);
    if (!types.isEmpty()) m_types->selectRow(0);
}

void StoreView::typeSelected() {
    const auto rows = m_types->selectionModel()->selectedRows();
    if (rows.isEmpty() || !m_store) return;
    const QTableWidgetItem *item = m_types->item(rows.first().row(), 0);
    m_type = item->text();
    QVector<StoreAttempt> hist;
    QString err;
    m_history->blockSignals(true);
    m_history->setRowCount(0);
    if (m_store->history(m_device, m_type, &hist, &err)) {
        const Tokens &tk = ThemeManager::instance().tokens();
        // Newest first: the question is what happened last night.
        m_history->setRowCount(int(hist.size()));
        for (int i = 0; i < hist.size(); ++i) {
            const StoreAttempt &a = hist.at(hist.size() - 1 - i);
            auto *at = new QTableWidgetItem(when(a.at));
            at->setData(Qt::UserRole, a.file);
            at->setData(Qt::UserRole + 1, a.sha256);
            auto *res = new QTableWidgetItem(a.unchanged ? tr("unchanged") : tr("stored"));
            res->setForeground(a.unchanged ? tk.textSecondary : tk.success);
            m_history->setItem(i, 0, at);
            m_history->setItem(i, 1, res);
            m_history->setItem(i, 2, new QTableWidgetItem(humanBytes(a.bytes)));
        }
    }
    m_history->blockSignals(false);
    m_fileSha = item->data(Qt::UserRole + 1).toString();
    load(item->data(Qt::UserRole).toString(), 0);
}

void StoreView::historySelected() {
    auto rows = m_history->selectionModel()->selectedRows();
    if (rows.isEmpty()) return;
    std::sort(rows.begin(), rows.end(), [](const QModelIndex &a, const QModelIndex &b) { return a.row() < b.row(); });
    m_diffOlder.clear();
    m_diffNewer.clear();
    // Rows are newest first: the top selected row is the newer version.
    const QTableWidgetItem *item = m_history->item(rows.first().row(), 0);
    if (rows.size() >= 2) {
        m_diffNewer = item->data(Qt::UserRole).toString();
        m_diffOlder = m_history->item(rows.last().row(), 0)->data(Qt::UserRole).toString();
    }
    m_fileSha = item->data(Qt::UserRole + 1).toString();
    load(item->data(Qt::UserRole).toString(), 0);
    if (rows.size() >= 2 && m_diffBtn->isEnabled()) showMode(Mode::Diff);
}

void StoreView::updateDiffAvailability() {
    QSet<QString> files;
    for (int i = 0; i < m_history->rowCount(); ++i) files.insert(m_history->item(i, 0)->data(Qt::UserRole).toString());
    const bool can = Store::diffable(m_type) && files.size() >= 2;
    m_diffBtn->setEnabled(can);
    m_diffBtn->setToolTip(can ? tr("Compare with the previous stored version, or select two versions in History")
                          : Store::diffable(m_type) ? tr("Only one stored version so far")
                                                    : tr("Diff covers running-config, startup-config and inventory"));
    if (!can && m_stack->currentIndex() == 2) showMode(Mode::Raw);
}

void StoreView::renderDiff() {
    m_diffView->clear();
    m_diffView->setExtraSelections({});
    if (!m_store || m_file.isEmpty()) return;

    QString older = m_diffOlder, newer = m_diffNewer;
    if (newer.isEmpty()) {
        // One version selected: against the nearest older row with a
        // different file (unchanged rows name the file they matched).
        newer = m_file;
        int at = -1;
        for (int i = 0; i < m_history->rowCount(); ++i)
            if (m_history->item(i, 0)->data(Qt::UserRole).toString() == newer && m_history->item(i, 0)->isSelected()) at = i;
        if (at < 0)
            for (int i = 0; i < m_history->rowCount() && at < 0; ++i)
                if (m_history->item(i, 0)->data(Qt::UserRole).toString() == newer) at = i;
        for (int i = at + 1; i >= 1 && i < m_history->rowCount(); ++i) {
            const QString f = m_history->item(i, 0)->data(Qt::UserRole).toString();
            if (f != newer) {
                older = f;
                break;
            }
        }
    }
    const Tokens &tk = ThemeManager::instance().tokens();
    if (older.isEmpty()) {
        m_diffInfo->setText(tr("This is the oldest stored version; there is nothing before it to compare with."));
        setTone(m_diffInfo, QStringLiteral("secondary"));
        return;
    }

    StoreDiff d;
    QString err;
    if (!m_store->diff(m_device, m_type, older, newer, &d, &err)) {
        m_diffInfo->setText(err);
        setTone(m_diffInfo, QStringLiteral("warning"));
        return;
    }
    m_diffInfo->setToolTip(tr("Ignore rules: %1").arg(d.rulesPath));
    const QString warning = d.rulesWarning.isEmpty() ? QString() : QStringLiteral("\n") + d.rulesWarning;
    if (d.identical) {
        m_diffInfo->setText(tr("%1 and %2 have the same lines.").arg(older, newer) + warning);
        setTone(m_diffInfo, warning.isEmpty() ? QStringLiteral("secondary") : QStringLiteral("warning"));
        return;
    }
    if (d.added == 0 && d.removed == 0) {
        m_diffInfo->setText(tr("%1 and %2 differ only in %3 line(s) the ignore rules cover.").arg(older, newer).arg(d.ignored) +
                            warning);
        setTone(m_diffInfo, warning.isEmpty() ? QStringLiteral("secondary") : QStringLiteral("warning"));
        return;
    }
    QString summary = tr("%1  \u2192  %2   \u00B7   +%3  \u2212%4").arg(older, newer).arg(d.added).arg(d.removed);
    if (d.ignored > 0) summary += tr("   \u00B7   %1 ignored").arg(d.ignored);
    summary += tr("   \u00B7   %1 hunk(s)").arg(d.hunks.size());
    m_diffInfo->setText(summary + warning);
    setTone(m_diffInfo, warning.isEmpty() ? QStringLiteral("secondary") : QStringLiteral("warning"));

    // One text block per line, so a line's background is one extra
    // selection. Numbers: older, newer.
    QStringList text;
    QVector<QChar> kinds;
    for (const StoreDiffHunk &h : d.hunks) {
        text.append(QStringLiteral("@@ -%1,%2 +%3,%4 @@").arg(h.aStart).arg(h.aLen).arg(h.bStart).arg(h.bLen));
        kinds.append(QLatin1Char('@'));
        for (const StoreDiffLine &l : h.lines) {
            const QString a = l.a > 0 ? QString::number(l.a) : QString();
            const QString b = l.b > 0 ? QString::number(l.b) : QString();
            const QChar mark = l.op == QLatin1Char('=') ? QLatin1Char(' ') : l.op;  // unified-diff convention
            text.append(QStringLiteral("%1 %2 %3  %4").arg(a, 6).arg(b, 6).arg(mark).arg(l.text));
            kinds.append(l.ignored ? QLatin1Char('~') : l.op);
        }
    }
    m_diffView->setPlainText(text.join(QLatin1Char('\n')));

    QList<QTextEdit::ExtraSelection> marks;
    QColor added = tk.success, removed = tk.danger;
    added.setAlpha(46);
    removed.setAlpha(46);
    QTextBlock block = m_diffView->document()->firstBlock();
    for (int i = 0; block.isValid() && i < kinds.size(); ++i, block = block.next()) {
        QTextEdit::ExtraSelection sel;
        sel.cursor = QTextCursor(block);
        sel.format.setProperty(QTextFormat::FullWidthSelection, true);
        if (kinds.at(i) == QLatin1Char('+')) sel.format.setBackground(added);
        else if (kinds.at(i) == QLatin1Char('-')) sel.format.setBackground(removed);
        else if (kinds.at(i) == QLatin1Char('@') || kinds.at(i) == QLatin1Char('~')) sel.format.setForeground(tk.textSecondary);
        else continue;
        marks.append(sel);
    }
    m_diffView->setExtraSelections(marks);
}

void StoreView::load(const QString &file, int line) {
    m_file = file;
    m_labBtn->setEnabled(false);
    m_shownParse = StoreParsed();
    updateOutputButtons();
    m_content->clear();
    m_parsed->clear();
    m_parsed->setRowCount(0);
    m_parsed->setColumnCount(0);
    if (!m_store || file.isEmpty()) return;

    QByteArray raw;
    QString err;
    if (!m_store->read(m_device, m_type, file, &raw, &err)) {
        m_contentTitle->setText(err);
        return;
    }
    m_contentTitle->setText(QStringLiteral("%1 / %2  \u00B7  %3").arg(m_device, m_type, file));
    m_content->setPlainText(QString::fromUtf8(raw));
    m_labBtn->setEnabled(true);
    updateOutputButtons();
    updateDiffAvailability();
    const bool diffing = m_stack->currentIndex() == 2 && m_diffBtn->isEnabled();
    m_line = line;
    markLine(m_content, line);

    StoreParsed p;
    const bool hasParse = m_store->parsed(m_device, m_type, file, &p, &err) && p.valid;
    m_parsedBtn->setEnabled(hasParse);
    const Tokens &tk = ThemeManager::instance().tokens();
    if (diffing) showMode(Mode::Diff);
    if (!hasParse) {
        if (!diffing) showParsed(false);
        return;
    }
    // A parse describes exact bytes. One that names different ones is left
    // over from something else and is not shown as this file's.
    if (!m_fileSha.isEmpty() && p.rawSha256 != m_fileSha) {
        m_parseInfo->setText(tr("This parse was made from different content and is not shown."));
        setTone(m_parseInfo, QStringLiteral("warning"));
        return;
    }
    if (p.status != QLatin1String("parsed")) {
        m_parseInfo->setText(p.templateName.isEmpty()
                                 ? tr("No template matched (hint %1).").arg(p.hint)
                                 : tr("No template matched well enough: closest was %1 at %2 (hint %3).")
                                       .arg(p.templateName).arg(p.score, 0, 'f', 1).arg(p.hint));
        setTone(m_parseInfo, QStringLiteral("warning"));
        return;
    }
    m_parseInfo->setText(tr("%1 records  \u00B7  %2  \u00B7  score %3")
                             .arg(p.records.size()).arg(p.templateName).arg(p.score, 0, 'f', 1));
    setTone(m_parseInfo, QStringLiteral("secondary"));
    m_parsed->setColumnCount(int(p.header.size()));
    m_parsed->setHorizontalHeaderLabels(p.header);
    m_parsed->setRowCount(int(p.records.size()));
    for (int r = 0; r < p.records.size(); ++r) {
        const QJsonObject rec = p.records.at(r).toObject();
        for (int c = 0; c < p.header.size(); ++c)
            m_parsed->setItem(r, c, new QTableWidgetItem(cell(rec.value(p.header.at(c)))));
    }
    m_parsed->resizeColumnsToContents();
    m_shownParse = p;
    updateOutputButtons();
    (void)tk;
}

void StoreView::showParsed(bool parsed) { showMode(parsed ? Mode::Parsed : Mode::Raw); }

void StoreView::showMode(Mode mode) {
    if (mode == Mode::Parsed && !m_parsedBtn->isEnabled()) mode = Mode::Raw;
    if (mode == Mode::Diff && !m_diffBtn->isEnabled()) mode = Mode::Raw;
    m_stack->setCurrentIndex(mode == Mode::Raw ? 0 : mode == Mode::Parsed ? 1 : 2);
    m_rawBtn->setChecked(mode == Mode::Raw);
    m_parsedBtn->setChecked(mode == Mode::Parsed);
    m_diffBtn->setChecked(mode == Mode::Diff);
    if (mode == Mode::Diff) renderDiff();
    updateOutputButtons();
}

void StoreView::updateOutputButtons() {
    const int page = m_stack->currentIndex();
    const bool parsedShown = page == 1 && m_shownParse.valid && m_shownParse.status == QLatin1String("parsed");
    m_exportBtn->setEnabled(parsedShown);
    // By what is on screen, not by m_file: a refresh clears the views and
    // leaves the last file name behind.
    const bool text = (page == 0 && !m_content->document()->isEmpty()) ||
                      (page == 2 && !m_diffView->document()->isEmpty());
    m_copyBtn->setEnabled(text || parsedShown);
}

QString StoreView::copyText() const {
    switch (m_stack->currentIndex()) {
    case 0:
        return m_content->toPlainText();
    case 1:
        if (!m_shownParse.valid) return {};
        return toTsv(omegacat::parsedTable(m_shownParse.header, m_shownParse.records, QStringLiteral(", ")));
    default:
        return m_diffView->toPlainText();
    }
}

bool StoreView::exportParsed(const QString &path, QString *err) const {
    if (!m_shownParse.valid || m_shownParse.status != QLatin1String("parsed")) {
        if (err) *err = tr("No parsed records are shown");
        return false;
    }
    return writeExport(path, toCsv(omegacat::parsedTable(m_shownParse.header, m_shownParse.records, QStringLiteral("\n"))), err);
}

bool StoreView::openFile(const QString &canonical, const QString &type, const QString &file, int line) {
    refresh();
    const auto found = m_devices->findItems(canonical, Qt::MatchExactly, 0);
    if (found.isEmpty()) return false;
    m_devices->setCurrentItem(found.first());
    for (int i = 0; i < m_types->rowCount(); ++i) {
        if (m_types->item(i, 0)->text() != type) continue;
        m_types->selectRow(i);
        if (!file.isEmpty() && file != m_file) {
            for (int h = 0; h < m_history->rowCount(); ++h) {
                if (m_history->item(h, 0)->data(Qt::UserRole).toString() == file) {
                    m_fileSha = m_history->item(h, 0)->data(Qt::UserRole + 1).toString();
                    break;
                }
            }
            load(file, line);
        } else if (line > 0) {
            load(m_file, line);
        }
        return true;
    }
    return false;
}

}  // namespace omegacat
