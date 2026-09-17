// app/searchview.cpp
#include "searchview.h"

#include <QCheckBox>
#include <QDir>
#include <QComboBox>
#include <QFontDatabase>
#include <QHBoxLayout>
#include <QHeaderView>
#include <QLabel>
#include <QLineEdit>
#include <QPlainTextEdit>
#include <QPushButton>
#include <QSplitter>
#include <QTableWidget>
#include <QTextBlock>
#include <QVBoxLayout>

#include "exporting.h"
#include "filedialogs.h"
#include "panel.h"
#include "runbridge.h"
#include "theme.h"

namespace omegacat {

SearchView::SearchView(QWidget *parent) : QWidget(parent) {
    m_bridge = new SearchBridge(this);
    connect(m_bridge, &SearchBridge::progress, this, [this](int done, int total) {
        m_status->setText(tr("Searching\u2026 %1 of %2").arg(done).arg(total));
    });
    connect(m_bridge, &SearchBridge::finished, this, &SearchView::finished);

    auto *row = new QHBoxLayout(this);
    row->setContentsMargins(12, 12, 12, 12);
    auto *split = new QSplitter(Qt::Horizontal, this);
    split->setChildrenCollapsible(false);

    auto *form = new Panel(tr("Search"), this);
    form->setMinimumWidth(240);
    m_query = new QLineEdit(form);
    m_query->setPlaceholderText(QStringLiteral("ntp server 172.16.0.10"));
    addField(form->bodyLayout(), tr("Find"), m_query, tr("Literal text, in each device's own format."));
    m_case = new QCheckBox(tr("Case sensitive"), form);
    form->bodyLayout()->addWidget(m_case);
    m_types = new QComboBox(form);
    m_types->addItem(tr("Every type"), QString());
    for (const CaptureType &ct : RunBridge::types()) m_types->addItem(ct.type, ct.type);
    addField(form->bodyLayout(), tr("In"), m_types);
    m_go = new QPushButton(tr("Search"), form);
    m_go->setProperty("primary", QStringLiteral("true"));
    m_go->setMinimumHeight(32);
    form->bodyLayout()->addWidget(m_go);
    m_status = new QLabel(form);
    m_status->setWordWrap(true);
    setTone(m_status, QStringLiteral("secondary"));
    form->bodyLayout()->addWidget(m_status);
    form->bodyLayout()->addStretch(1);
    m_skips = new QLabel(form);
    m_skips->setWordWrap(true);
    setTone(m_skips, QStringLiteral("warning"));
    form->bodyLayout()->addWidget(m_skips);
    split->addWidget(form);

    auto *results = new Panel(tr("Hits"), this);
    m_summary = new QLabel(results);
    setTone(m_summary, QStringLiteral("muted"));
    results->barLayout()->addWidget(m_summary);
    m_export = new QPushButton(tr("Export CSV\u2026"), results);
    m_export->setToolTip(tr("Save every hit as a CSV file"));
    m_export->setEnabled(false);
    results->barLayout()->addWidget(m_export);
    m_hits = new QTableWidget(0, 4, results);
    m_hits->setHorizontalHeaderLabels({tr("DEVICE"), tr("TYPE"), tr("LINE"), tr("TEXT")});
    m_hits->verticalHeader()->hide();
    m_hits->setShowGrid(false);
    m_hits->setAlternatingRowColors(true);
    m_hits->setEditTriggers(QAbstractItemView::NoEditTriggers);
    m_hits->setSelectionBehavior(QAbstractItemView::SelectRows);
    m_hits->setSelectionMode(QAbstractItemView::SingleSelection);
    m_hits->verticalHeader()->setDefaultSectionSize(22);
    m_hits->horizontalHeader()->setStretchLastSection(true);
    m_hits->horizontalHeader()->resizeSection(0, 150);
    m_hits->horizontalHeader()->resizeSection(1, 110);
    m_hits->horizontalHeader()->resizeSection(2, 50);
    results->bodyLayout()->addWidget(m_hits, 1);
    split->addWidget(results);

    auto *view = new Panel(tr("In context"), this);
    view->setMinimumWidth(300);
    m_hitTitle = new QLabel(view);
    setTone(m_hitTitle, QStringLiteral("secondary"));
    view->barLayout()->addWidget(m_hitTitle);
    m_open = new QPushButton(tr("Open in store"), view);
    m_open->setEnabled(false);
    view->barLayout()->addWidget(m_open);
    m_copy = new QPushButton(tr("Copy"), view);
    m_copy->setToolTip(tr("Copy the whole capture this hit is in"));
    m_copy->setEnabled(false);
    view->barLayout()->addWidget(m_copy);
    m_content = new QPlainTextEdit(view);
    m_content->setProperty("role", QStringLiteral("content"));
    m_content->setReadOnly(true);
    m_content->setLineWrapMode(QPlainTextEdit::NoWrap);
    m_content->setFont(QFontDatabase::systemFont(QFontDatabase::FixedFont));
    view->bodyLayout()->addWidget(m_content, 1);
    split->addWidget(view);

    split->setStretchFactor(1, 1);
    split->setSizes({260, 620, 460});
    row->addWidget(split);

    connect(&ThemeManager::instance(), &ThemeManager::changed, this, [this] { markLine(m_content, m_line); });
    connect(m_go, &QPushButton::clicked, this, &SearchView::start);
    connect(m_query, &QLineEdit::returnPressed, this, &SearchView::start);
    connect(m_hits, &QTableWidget::itemSelectionChanged, this, &SearchView::showHit);
    connect(m_copy, &QPushButton::clicked, this, [this] { copyToClipboard(m_copy, m_content->toPlainText()); });
    connect(m_export, &QPushButton::clicked, this, [this] {
        const QString suggested =
            QDir(exportDirectory()).filePath(exportFileName({QStringLiteral("search"), m_lastQuery.left(40)}, QStringLiteral("csv")));
        const QString path = pickSaveFile(this, tr("Export search hits"), suggested, tr("CSV files (*.csv)"));
        if (path.isEmpty()) return;
        QString err;
        if (exportHits(path, &err)) {
            setExportDirectory(path);
            m_status->setText(tr("Exported %1 hits to %2").arg(m_result.hits.size()).arg(QDir::toNativeSeparators(path)));
        } else {
            m_status->setText(err);
        }
    });
    connect(m_open, &QPushButton::clicked, this, [this] {
        const auto rows = m_hits->selectionModel()->selectedRows();
        if (rows.isEmpty()) return;
        const SearchHit &h = m_result.hits.at(rows.first().row());
        emit openInStore(h.device, h.type, h.file, h.line);
    });
}

void SearchView::setStore(const Store *store) { m_store = store; }

void SearchView::runQuery(const QString &query) {
    m_query->setText(query);
    start();
}

void SearchView::start() {
    if (!m_store || !m_store->isOpen()) {
        m_status->setText(tr("No store open."));
        return;
    }
    if (m_query->text().trimmed().isEmpty()) {
        m_status->setText(tr("Enter something to find."));
        return;
    }
    const QString type = m_types->currentData().toString();
    QString err;
    m_hits->setRowCount(0);
    m_content->clear();
    m_copy->setEnabled(false);
    m_export->setEnabled(false);
    m_result = SearchResult();
    m_lastQuery = m_query->text().trimmed();
    m_skips->clear();
    if (!m_bridge->start(*m_store, m_query->text(), m_case->isChecked(),
                         type.isEmpty() ? QStringList{} : QStringList{type}, &err)) {
        m_status->setText(err);
        return;
    }
    m_go->setEnabled(false);
    m_status->setText(tr("Searching\u2026"));
}

bool SearchView::exportHits(const QString &path, QString *err) const {
    if (m_result.hits.isEmpty()) {
        if (err) *err = tr("No hits to export");
        return false;
    }
    ExportTable t;
    t.header = {QStringLiteral("device"), QStringLiteral("type"), QStringLiteral("file"), QStringLiteral("line"),
                QStringLiteral("text"), QStringLiteral("truncated")};
    for (const SearchHit &h : m_result.hits)
        t.rows.append({h.device, h.type, h.file, QString::number(h.line), h.text,
                       h.truncated ? QStringLiteral("true") : QStringLiteral("false")});
    return writeExport(path, toCsv(t), err);
}

void SearchView::finished(const SearchResult &r) {
    m_result = r;
    m_export->setEnabled(!r.hits.isEmpty());
    m_go->setEnabled(true);
    m_status->setText(!r.error.isEmpty() ? r.error
                      : r.state == QLatin1String("cancelled") ? tr("Cancelled")
                                                              : QString());
    // Capped and skipped are said where they cannot be missed: a truncated or
    // partial answer that does not admit it is a wrong answer.
    QString summary = r.summary;
    if (r.capped) summary += tr("  \u00B7  showing the first %1").arg(r.limit);
    m_summary->setText(summary);
    setTone(m_summary, r.capped ? QStringLiteral("warning") : QStringLiteral("muted"));
    m_skips->setText(r.skips.isEmpty() ? QString() : tr("Not searched:\n%1").arg(r.skips.join(QLatin1Char('\n'))));

    m_hits->setRowCount(int(r.hits.size()));
    for (int i = 0; i < r.hits.size(); ++i) {
        const SearchHit &h = r.hits.at(i);
        m_hits->setItem(i, 0, new QTableWidgetItem(h.device));
        m_hits->setItem(i, 1, new QTableWidgetItem(h.type));
        auto *line = new QTableWidgetItem(QString::number(h.line));
        line->setTextAlignment(Qt::AlignRight | Qt::AlignVCenter);
        m_hits->setItem(i, 2, line);
        m_hits->setItem(i, 3, new QTableWidgetItem(h.truncated ? h.text : h.text));
    }
    if (!r.hits.isEmpty()) m_hits->selectRow(0);
    emit searchFinished();
}

void SearchView::showHit() {
    const auto rows = m_hits->selectionModel()->selectedRows();
    m_open->setEnabled(!rows.isEmpty());
    m_copy->setEnabled(false);
    if (rows.isEmpty() || !m_store) return;
    const SearchHit &h = m_result.hits.at(rows.first().row());
    QByteArray raw;
    QString err;
    if (!m_store->read(h.device, h.type, h.file, &raw, &err)) {
        m_hitTitle->setText(err);
        return;
    }
    m_hitTitle->setText(QStringLiteral("%1 / %2 : %3").arg(h.device, h.type).arg(h.line));
    m_content->setPlainText(QString::fromUtf8(raw));
    m_copy->setEnabled(true);
    m_line = h.line;
    markLine(m_content, h.line);
}

}  // namespace omegacat
