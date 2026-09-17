// app/templatelabview.cpp
#include "templatelabview.h"

#include <QButtonGroup>
#include <QCheckBox>
#include <QComboBox>
#include <QDialog>
#include <QDialogButtonBox>
#include <QFontDatabase>
#include <QGridLayout>
#include <QHBoxLayout>
#include <QHeaderView>
#include <QJsonObject>
#include <QLabel>
#include <QLineEdit>
#include <QListWidget>
#include <QMessageBox>
#include <QPainter>
#include <QPainterPath>
#include <QPlainTextEdit>
#include <QPushButton>
#include <QSplitter>
#include <QStyle>
#include <QStackedWidget>
#include <QTableWidget>
#include <QTextBlock>
#include <QTimer>
#include <QVBoxLayout>

#include "panel.h"
#include "store.h"
#include "theme.h"

namespace omegacat {

namespace {

// The breakdown's maxima, as netlapse's Lab draws them. The total is their sum,
// 220, whatever tfsm_fire's docstring says about 100.
struct BarSpec {
    const char *label;
    double max;
};
const BarSpec kBars[4] = {{"Records", 90}, {"Fields", 90}, {"Population", 25}, {"Consistency", 15}};

QString num(double v) { return QString::number(v, 'f', 1); }

// "4 records", "1 record". tr()'s %n plural forms need a translation file to
// read as English, and the application ships none.
QString count(qint64 n, const char *one, const char *many) {
    return QStringLiteral("%1 %2").arg(n).arg(QLatin1String(n == 1 ? one : many));
}

QPlainTextEdit *monoEditor(QWidget *parent, const QString &placeholder) {
    auto *e = new QPlainTextEdit(parent);
    e->setProperty("role", QStringLiteral("content"));
    e->setLineWrapMode(QPlainTextEdit::NoWrap);
    e->setFont(QFontDatabase::systemFont(QFontDatabase::FixedFont));
    e->setPlaceholderText(placeholder);
    e->setTabChangesFocus(false);
    return e;
}

QTableWidget *plainTable(QWidget *parent) {
    auto *t = new QTableWidget(parent);
    t->verticalHeader()->hide();
    t->setShowGrid(false);
    t->setAlternatingRowColors(true);
    t->setEditTriggers(QAbstractItemView::NoEditTriggers);
    t->setSelectionBehavior(QAbstractItemView::SelectRows);
    t->verticalHeader()->setDefaultSectionSize(22);
    t->horizontalHeader()->setStretchLastSection(true);
    return t;
}

}  // namespace

// One horizontal score bar, painted from the theme like the Run view's depth
// bar, so a theme change needs nothing but a repaint.
class LabBar : public QWidget {
public:
    explicit LabBar(QWidget *parent) : QWidget(parent) {
        setFixedHeight(8);
        setSizePolicy(QSizePolicy::Expanding, QSizePolicy::Fixed);
        connect(&ThemeManager::instance(), &ThemeManager::changed, this, qOverload<>(&QWidget::update));
    }
    void setFraction(double f) {
        m_f = qBound(0.0, f, 1.0);
        update();
    }
    double fraction() const { return m_f; }

protected:
    void paintEvent(QPaintEvent *) override {
        const Tokens &t = ThemeManager::instance().tokens();
        QPainter p(this);
        p.setRenderHint(QPainter::Antialiasing);
        const QRectF r = QRectF(rect()).adjusted(0.5, 0.5, -0.5, -0.5);
        QPainterPath clip;
        clip.addRoundedRect(r, r.height() / 2, r.height() / 2);
        p.setClipPath(clip);
        p.fillRect(r, t.bgTertiary);
        if (m_f > 0) p.fillRect(QRectF(r.left(), r.top(), r.width() * m_f, r.height()), t.accent);
    }

private:
    double m_f = 0;
};

TemplateLabView::TemplateLabView(QWidget *parent) : QWidget(parent) {
    m_bridge = new TemplateBridge(this);
    connect(m_bridge, &TemplateBridge::sweepFinished, this, &TemplateLabView::sweepFinished);
    connect(m_bridge, &TemplateBridge::saveFinished, this, &TemplateLabView::saveFinished);
    connect(m_bridge, &TemplateBridge::removeFinished, this, [this](qint64 rowid, bool ok, const QString &err) {
        if (!ok) {
            emit status(tr("Delete failed: %1").arg(err));
        } else {
            if (rowid == m_editing) setEditing(0, 0);
            emit status(err.isEmpty() ? tr("Template deleted") : err);
        }
        refresh();
    });

    auto *col = new QVBoxLayout(this);
    col->setContentsMargins(12, 8, 12, 12);
    col->setSpacing(8);

    auto *tabs = new QHBoxLayout;
    tabs->setSpacing(4);
    m_authorTab = new QPushButton(tr("Author && Test"), this);
    m_browserTab = new QPushButton(tr("Template Browser"), this);
    auto *group = new QButtonGroup(this);
    for (QPushButton *b : {m_authorTab, m_browserTab}) {
        b->setProperty("role", QStringLiteral("viewTab"));
        b->setCheckable(true);
        b->setCursor(Qt::PointingHandCursor);
        group->addButton(b);
        tabs->addWidget(b);
    }
    tabs->addStretch(1);
    col->addLayout(tabs);
    connect(m_authorTab, &QPushButton::clicked, this, [this] { showPage(Page::Author); });
    connect(m_browserTab, &QPushButton::clicked, this, [this] { showPage(Page::Browser); });

    m_pages = new QStackedWidget(this);
    m_pages->addWidget(buildAuthor());
    m_pages->addWidget(buildBrowser());
    col->addWidget(m_pages, 1);
    showPage(Page::Author);
    connect(&ThemeManager::instance(), &ThemeManager::changed, this, [this] {
        if (m_ruleLine > 0) markLine(m_template, m_ruleLine);
        if (!m_inputLine.isEmpty()) markRawLine(m_inputLine);
    });
}

QWidget *TemplateLabView::buildAuthor() {
    auto *page = new QWidget(this);
    auto *col = new QVBoxLayout(page);
    col->setContentsMargins(0, 0, 0, 0);
    col->setSpacing(8);

    auto *meta = new QHBoxLayout;
    meta->setSpacing(8);
    m_name = new QLineEdit(page);
    m_name->setProperty("mono", QStringLiteral("true"));
    m_name->setPlaceholderText(tr("template_name (e.g. juniper_junos_show_bgp_summary)"));
    meta->addWidget(m_name, 3);
    m_platform = new QComboBox(page);
    m_platform->setMinimumWidth(170);
    m_platform->addItem(tr("platform\u2026"), QString());
    meta->addWidget(m_platform, 2);
    m_command = new QLineEdit(page);
    m_command->setPlaceholderText(tr("command (optional)"));
    meta->addWidget(m_command, 2);
    m_clean = new QCheckBox(tr("clean preamble"), page);
    m_clean->setChecked(true);
    m_clean->setToolTip(tr("Strip the command echo, banners and trailing prompt the way a parse does. "
                           "Off tests the exact text, so line numbers in errors match what is pasted."));
    meta->addWidget(m_clean);
    m_fromStore = new QPushButton(tr("From store\u2026"), page);
    m_fromStore->setToolTip(tr("Load a stored capture's output, platform and command"));
    meta->addWidget(m_fromStore);
    col->addLayout(meta);

    auto *split = new QSplitter(Qt::Horizontal, page);
    split->setChildrenCollapsible(false);

    // ---- editors -------------------------------------------------------
    auto *left = new QWidget(split);
    auto *leftCol = new QVBoxLayout(left);
    leftCol->setContentsMargins(0, 0, 0, 0);
    leftCol->setSpacing(8);
    auto *edits = new QSplitter(Qt::Vertical, left);
    edits->setChildrenCollapsible(false);

    auto *rawPanel = new Panel(tr("Raw CLI output"), edits);
    m_sample = new QPushButton(tr("Use sample"), rawPanel);
    m_sample->setToolTip(tr("Replace the output with the sample stored beside the loaded template"));
    m_sample->setEnabled(false);
    rawPanel->barLayout()->addWidget(m_sample);
    m_raw = monoEditor(rawPanel, tr("Paste raw CLI output here, or load it from the store\u2026"));
    rawPanel->bodyLayout()->addWidget(m_raw, 1);

    auto *tplPanel = new Panel(tr("TextFSM template"), edits);
    m_editState = new QLabel(tr("New template"), tplPanel);
    setTone(m_editState, QStringLiteral("muted"));
    tplPanel->barLayout()->addWidget(m_editState);
    m_template = monoEditor(tplPanel, QStringLiteral("Value FIELD (\\S+)\n\nStart\n  ^${FIELD} -> Record"));
    tplPanel->bodyLayout()->addWidget(m_template, 1);
    edits->setSizes({220, 420});
    leftCol->addWidget(edits, 1);

    auto *actions = new QHBoxLayout;
    m_test = new QPushButton(tr("Test template"), left);
    m_test->setProperty("primary", QStringLiteral("true"));
    m_sweep = new QPushButton(tr("Test vs database"), left);
    m_save = new QPushButton(tr("Save to database"), left);
    m_clear = new QPushButton(tr("Clear"), left);
    actions->addWidget(m_test);
    actions->addWidget(m_sweep);
    actions->addStretch(1);
    actions->addWidget(m_save);
    actions->addWidget(m_clear);
    leftCol->addLayout(actions);
    split->addWidget(left);

    // ---- result --------------------------------------------------------
    auto *right = new QSplitter(Qt::Vertical, split);
    right->setChildrenCollapsible(false);

    auto *result = new Panel(tr("Result"), right);
    m_pill = new QLabel(result);
    m_pill->setProperty("mono", QStringLiteral("true"));
    result->barLayout()->addWidget(m_pill);
    m_placeholder = new QLabel(result);
    m_placeholder->setWordWrap(true);
    m_placeholder->setAlignment(Qt::AlignCenter);
    setTone(m_placeholder, QStringLiteral("muted"));
    result->bodyLayout()->addWidget(m_placeholder);

    m_match = new QFrame(result);
    m_match->setProperty("role", QStringLiteral("labMatch"));
    auto *matchCol = new QVBoxLayout(m_match);
    matchCol->setContentsMargins(12, 10, 12, 10);
    matchCol->setSpacing(4);
    m_matchCaption = makeCaption(QString(), "labCaption", 8, m_match);
    matchCol->addWidget(m_matchCaption);
    m_matchMeta = new QLabel(m_match);
    m_matchMeta->setWordWrap(true);
    m_matchMeta->setTextInteractionFlags(Qt::TextSelectableByMouse);
    matchCol->addWidget(m_matchMeta);
    m_matchDetail = new QLabel(m_match);
    m_matchDetail->setWordWrap(true);
    m_matchDetail->setProperty("mono", QStringLiteral("true"));
    m_matchDetail->setTextInteractionFlags(Qt::TextSelectableByMouse);
    matchCol->addWidget(m_matchDetail);
    result->bodyLayout()->addWidget(m_match);

    m_bars = new QWidget(result);
    auto *grid = new QGridLayout(m_bars);
    grid->setContentsMargins(0, 6, 0, 0);
    grid->setHorizontalSpacing(12);
    grid->setVerticalSpacing(8);
    for (int i = 0; i < 4; ++i) {
        auto *label = new QLabel(tr(kBars[i].label), m_bars);
        setTone(label, QStringLiteral("secondary"));
        grid->addWidget(label, i, 0);
        m_barWidgets[i] = new LabBar(m_bars);
        grid->addWidget(m_barWidgets[i], i, 1);
        m_barValues[i] = new QLabel(m_bars);
        m_barValues[i]->setProperty("mono", QStringLiteral("true"));
        m_barValues[i]->setAlignment(Qt::AlignRight | Qt::AlignVCenter);
        m_barValues[i]->setMinimumWidth(64);
        grid->addWidget(m_barValues[i], i, 2);
    }
    grid->setColumnStretch(1, 1);
    result->bodyLayout()->addWidget(m_bars);

    m_candidatesToggle = new QPushButton(result);
    m_candidatesToggle->setCheckable(true);
    m_candidatesToggle->setFlat(true);
    result->bodyLayout()->addWidget(m_candidatesToggle, 0, Qt::AlignLeft);
    m_candidates = new QListWidget(result);
    m_candidates->setMaximumHeight(160);
    m_candidates->setFont(QFontDatabase::systemFont(QFontDatabase::FixedFont));
    result->bodyLayout()->addWidget(m_candidates);
    connect(m_candidatesToggle, &QPushButton::toggled, m_candidates, &QWidget::setVisible);
    result->bodyLayout()->addStretch(1);

    auto *parsed = new Panel(tr("Parsed data"), right);
    m_recordCount = new QLabel(parsed);
    setTone(m_recordCount, QStringLiteral("muted"));
    parsed->barLayout()->addWidget(m_recordCount);
    m_parsed = plainTable(parsed);
    m_parsed->setFont(QFontDatabase::systemFont(QFontDatabase::FixedFont));
    parsed->bodyLayout()->addWidget(m_parsed, 1);
    right->setSizes({300, 400});
    split->addWidget(right);
    split->setSizes({640, 620});
    col->addWidget(split, 1);

    connect(m_test, &QPushButton::clicked, this, &TemplateLabView::test);
    connect(m_sweep, &QPushButton::clicked, this, &TemplateLabView::sweep);
    connect(m_save, &QPushButton::clicked, this, &TemplateLabView::save);
    connect(m_clear, &QPushButton::clicked, this, &TemplateLabView::clearEditor);
    connect(m_fromStore, &QPushButton::clicked, this, &TemplateLabView::chooseFromStore);
    connect(m_sample, &QPushButton::clicked, this, [this] { m_raw->setPlainText(m_sampleText); });
    // A mark on a line of a template that has since been edited points at the
    // wrong line; drop it on the first keystroke.
    connect(m_template, &QPlainTextEdit::textChanged, this, [this] {
        // The compile-error warning on Save is about the text that was
        // tested; once the text changes there is no result to warn about.
        m_lastCompileOk = -1;
        if (m_ruleLine) {
            m_ruleLine = 0;
            markLine(m_template, 0);
        }
    });
    connect(m_raw, &QPlainTextEdit::textChanged, this, [this] {
        if (!m_inputLine.isEmpty()) {
            m_inputLine.clear();
            markLine(m_raw, 0);
        }
    });

    showPlaceholder(tr("Paste output, write or load a template, and press Test template.\n"
                       "Nothing is saved until you press Save."));
    setParsed({}, {});
    return page;
}

QWidget *TemplateLabView::buildBrowser() {
    auto *panel = new Panel(tr("Templates"), this);
    m_browserPlatform = new QComboBox(panel);
    m_browserPlatform->setMinimumWidth(180);
    m_browserPlatform->addItem(tr("All platforms"), QString());
    panel->barLayout()->addWidget(m_browserPlatform);
    m_browserSearch = new QLineEdit(panel);
    m_browserSearch->setPlaceholderText(tr("Search templates\u2026"));
    m_browserSearch->setMinimumWidth(220);
    m_browserSearch->setClearButtonEnabled(true);
    panel->barLayout()->addWidget(m_browserSearch);
    m_browserCount = new QLabel(panel);
    setTone(m_browserCount, QStringLiteral("muted"));
    panel->barLayout()->addWidget(m_browserCount);
    auto *add = new QPushButton(tr("New template"), panel);
    add->setProperty("primary", QStringLiteral("true"));
    panel->barLayout()->addWidget(add);

    m_browser = plainTable(panel);
    m_browser->setColumnCount(4);
    m_browser->setHorizontalHeaderLabels({tr("ROW"), tr("ID"), tr("TEMPLATE"), tr("SOURCE")});
    m_browser->setSelectionMode(QAbstractItemView::SingleSelection);
    m_browser->horizontalHeader()->resizeSection(0, 60);
    m_browser->horizontalHeader()->resizeSection(1, 60);
    m_browser->horizontalHeader()->resizeSection(2, 460);
    panel->bodyLayout()->addWidget(m_browser, 1);

    auto *row = new QHBoxLayout;
    auto *hint = new QLabel(tr("Double-click a template to open it in Author & Test."), panel);
    setTone(hint, QStringLiteral("muted"));
    row->addWidget(hint);
    row->addStretch(1);
    auto *open = new QPushButton(tr("Open"), panel);
    auto *del = new QPushButton(tr("Delete\u2026"), panel);
    row->addWidget(open);
    row->addWidget(del);
    panel->bodyLayout()->addLayout(row);

    const auto selectedRowid = [this]() -> qint64 {
        const auto rows = m_browser->selectionModel()->selectedRows();
        if (rows.isEmpty()) return 0;
        return m_browser->item(rows.first().row(), 0)->data(Qt::UserRole).toLongLong();
    };
    const auto openSelected = [this, selectedRowid] {
        const qint64 rowid = selectedRowid();
        QString err;
        if (rowid && !loadTemplate(rowid, &err)) emit status(err);
    };
    connect(open, &QPushButton::clicked, this, openSelected);
    connect(m_browser, &QTableWidget::cellDoubleClicked, this, openSelected);
    connect(del, &QPushButton::clicked, this, [this, selectedRowid] {
        const qint64 rowid = selectedRowid();
        if (!rowid) return;
        const int r = m_browser->selectionModel()->selectedRows().first().row();
        const QString name = m_browser->item(r, 2)->text();
        if (QMessageBox::question(this, tr("Delete template"),
                                  tr("Delete \u201C%1\u201D from the template database?\n\n"
                                     "Captures stop using it with the next run.").arg(name)) != QMessageBox::Yes)
            return;
        removeTemplate(rowid);
    });
    connect(add, &QPushButton::clicked, this, [this] {
        clearEditor();
        showPage(Page::Author);
    });
    connect(m_browserPlatform, qOverload<int>(&QComboBox::currentIndexChanged), this, &TemplateLabView::reloadBrowser);
    auto *debounce = new QTimer(this);
    debounce->setSingleShot(true);
    debounce->setInterval(250);
    connect(debounce, &QTimer::timeout, this, &TemplateLabView::reloadBrowser);
    connect(m_browserSearch, &QLineEdit::textChanged, debounce, qOverload<>(&QTimer::start));
    return panel;
}

void TemplateLabView::setDatabasePath(const QString &path) {
    m_bridge->setDatabasePath(path);
    m_refreshed = false;
}

void TemplateLabView::refresh() {
    m_refreshed = true;
    QVector<LabPlatform> ps;
    QString err;
    if (!m_bridge->platforms(&ps, &err)) {
        emit status(tr("Template database: %1").arg(err));
        return;
    }
    const QString keepAuthor = m_platform->currentData().toString();
    const QString keepBrowser = m_browserPlatform->currentData().toString();
    const QSignalBlocker b1(m_platform), b2(m_browserPlatform);
    while (m_platform->count() > 1) m_platform->removeItem(1);
    while (m_browserPlatform->count() > 1) m_browserPlatform->removeItem(1);
    for (const LabPlatform &p : ps) {
        const QString label = QStringLiteral("%1 (%2)").arg(p.platform).arg(p.count);
        m_platform->addItem(label, p.platform);
        m_browserPlatform->addItem(label, p.platform);
    }
    setPlatform(keepAuthor);
    const int bi = m_browserPlatform->findData(keepBrowser);
    m_browserPlatform->setCurrentIndex(bi < 0 ? 0 : bi);
    if (m_pages->currentIndex() == 1) reloadBrowser();
}

void TemplateLabView::setPlatform(const QString &platform) {
    if (platform.isEmpty()) {
        m_platform->setCurrentIndex(0);
        return;
    }
    int i = m_platform->findData(platform);
    if (i < 0) {
        // A device platform no template is named for (aruba_procurve's
        // templates are hp_procurve_*): kept, so the sweep's filter says so.
        m_platform->addItem(tr("%1 (no templates)").arg(platform), platform);
        i = m_platform->count() - 1;
    }
    m_platform->setCurrentIndex(i);
}

void TemplateLabView::showPage(Page page) {
    m_pages->setCurrentIndex(page == Page::Browser ? 1 : 0);
    m_authorTab->setChecked(page == Page::Author);
    m_browserTab->setChecked(page == Page::Browser);
    if (page == Page::Browser) {
        if (!m_refreshed) refresh();
        reloadBrowser();
    }
}

void TemplateLabView::reloadBrowser() {
    QVector<LabItem> items;
    QString err;
    if (!m_bridge->list(m_browserPlatform->currentData().toString(), m_browserSearch->text().trimmed(), &items,
                        &err)) {
        m_browserCount->setText(err);
        return;
    }
    m_browserCount->setText(count(items.size(), "template", "templates"));
    m_browser->setRowCount(int(items.size()));
    for (int r = 0; r < items.size(); ++r) {
        const LabItem &it = items.at(r);
        auto *rowItem = new QTableWidgetItem(QString::number(it.rowid));
        rowItem->setData(Qt::UserRole, it.rowid);
        m_browser->setItem(r, 0, rowItem);
        auto *idItem = new QTableWidgetItem(it.id >= 0 ? QString::number(it.id) : QStringLiteral("\u2014"));
        if (it.id < 0) idItem->setToolTip(tr("No id: netlapse cannot open this row. Saving it here gives it one."));
        m_browser->setItem(r, 1, idItem);
        m_browser->setItem(r, 2, new QTableWidgetItem(it.name));
        m_browser->setItem(r, 3, new QTableWidgetItem(it.source));
    }
}

// ---- results -------------------------------------------------------------

void TemplateLabView::showPlaceholder(const QString &text) {
    m_placeholder->setText(text);
    m_placeholder->show();
    m_match->hide();
    showBars(false);
    m_candidatesToggle->hide();
    m_candidates->hide();
    m_pill->clear();
}

void TemplateLabView::showMatch(const QString &state, const QString &caption, const QString &meta,
                                const QString &detail) {
    m_placeholder->hide();
    setStyleProperty(m_match, "state", state);
    // The caption's colour follows the box's state through the sheet, which
    // only re-reads a descendant's rule when the descendant is repolished.
    m_matchCaption->setText(caption.toUpper());
    m_matchCaption->style()->unpolish(m_matchCaption);
    m_matchCaption->style()->polish(m_matchCaption);
    m_matchMeta->setText(meta);
    m_matchMeta->setVisible(!meta.isEmpty());
    m_matchDetail->setText(detail);
    m_matchDetail->setVisible(!detail.isEmpty());
    m_match->show();
}

void TemplateLabView::showBars(bool on) { m_bars->setVisible(on); }

double TemplateLabView::barValue(int i) const { return (i >= 0 && i < 4) ? m_barWidgets[i]->fraction() : 0; }

void TemplateLabView::setParsed(const QStringList &header, const QJsonArray &records) {
    m_parsed->clear();
    m_parsed->setColumnCount(int(header.size()));
    m_parsed->setHorizontalHeaderLabels(header);
    // A result with thousands of rows is a MAC table on a core switch; the
    // table shows enough to judge the template, and says it stopped.
    const int shown = qMin(int(records.size()), 500);
    m_parsed->setRowCount(shown);
    for (int r = 0; r < shown; ++r) {
        const QJsonObject rec = records.at(r).toObject();
        for (int c = 0; c < header.size(); ++c) {
            const QJsonValue v = rec.value(header.at(c));
            QString text;
            if (v.isArray()) {
                QStringList parts;
                for (const QJsonValue &e : v.toArray()) parts << e.toString();
                text = parts.join(QStringLiteral(", "));
            } else {
                text = v.toString();
            }
            m_parsed->setItem(r, c, new QTableWidgetItem(text));
        }
    }
    if (!header.isEmpty()) m_parsed->resizeColumnsToContents();
    if (records.isEmpty()) {
        m_recordCount->setText(header.isEmpty() ? QString() : tr("0 records"));
    } else if (shown < records.size()) {
        m_recordCount->setText(tr("first %1 of %2 records").arg(shown).arg(records.size()));
    } else {
        m_recordCount->setText(count(records.size(), "record", "records"));
    }
}

void TemplateLabView::showCandidates(const QVector<LabCandidate> &cands, const QString &winner) {
    m_candidates->clear();
    const Tokens &t = ThemeManager::instance().tokens();
    for (const LabCandidate &c : cands) {
        auto *item = new QListWidgetItem(c.name == winner ? tr("%1   \u2190 winner").arg(c.name) : c.name);
        if (c.name == winner) {
            QFont f = item->font();
            f.setBold(true);
            item->setFont(f);
            item->setForeground(t.success);
        } else if (!c.compileError.isEmpty()) {
            item->setForeground(t.danger);
            item->setToolTip(tr("Does not compile, so never tried: %1").arg(c.compileError));
        }
        item->setData(Qt::UserRole, c.rowid);
        m_candidates->addItem(item);
    }
    m_candidatesToggle->setText(tr("%1 in scope").arg(count(cands.size(), "candidate", "candidates")));
    m_candidatesToggle->setVisible(!cands.isEmpty());
    m_candidatesToggle->setChecked(false);
    m_candidates->hide();
}

void TemplateLabView::markRawLine(const QString &text) {
    // The input line is reported after cleaning, so find it by text rather
    // than by number: a cleaned transcript is shorter than the pasted one.
    m_inputLine = text;
    const QString needle = text.trimmed();
    if (needle.isEmpty()) return;
    for (QTextBlock b = m_raw->document()->begin(); b.isValid(); b = b.next()) {
        if (b.text().trimmed() == needle) {
            const QSignalBlocker block(m_raw);
            markLine(m_raw, b.blockNumber() + 1);
            return;
        }
    }
}

void TemplateLabView::test() {
    const QString raw = m_raw->toPlainText();
    const QString content = m_template->toPlainText();
    if (content.trimmed().isEmpty()) {
        showMatch(QStringLiteral("fail"), tr("Nothing to test"), tr("Write or load a TextFSM template first."), {});
        return;
    }
    if (raw.trimmed().isEmpty()) {
        showMatch(QStringLiteral("fail"), tr("Nothing to test"), tr("Paste raw CLI output to test against."), {});
        return;
    }
    QString command = m_command->text().trimmed();
    if (command.isEmpty()) command = m_name->text().trimmed();
    const LabTest r = TemplateBridge::test(raw, content, command, m_clean->isChecked());
    m_lastCompileOk = r.compiled ? 1 : 0;
    m_candidatesToggle->hide();
    m_candidates->hide();
    markLine(m_template, 0);
    markLine(m_raw, 0);
    m_ruleLine = 0;
    m_inputLine.clear();

    if (!r.compiled) {
        m_pill->clear();
        showMatch(QStringLiteral("fail"), tr("Compile error"), {}, r.error);
        showBars(false);
        setParsed({}, {});
        return;
    }
    if (!r.success) {
        m_pill->setText(tr("no match"));
        QString meta;
        if (r.ruleLine > 0) meta += tr("Template line <b>%1</b>").arg(r.ruleLine);
        if (!r.inputLine.isEmpty())
            meta += (meta.isEmpty() ? QString() : QStringLiteral(" \u00B7 ")) +
                    tr("choked on <code>%1</code>").arg(r.inputLine.toHtmlEscaped());
        meta += tr("<br>A strict <code>-&gt; Error</code> rule turns an unexpected line into a hard failure. "
                   "Loosen the rule or add a skip for this line.");
        showMatch(QStringLiteral("fail"), r.errorType == QLatin1String("runtime") ? tr("State error") : tr("Parse error"),
                  meta, r.error);
        showBars(false);
        setParsed(r.header, {});
        {
            const QSignalBlocker block(m_template);
            m_ruleLine = r.ruleLine;
            markLine(m_template, r.ruleLine);
        }
        markRawLine(r.inputLine);
        return;
    }

    m_pill->setText(tr("score %1").arg(num(r.score)));
    showMatch(QStringLiteral("ok"),
              m_clean->isChecked() ? tr("Parsed OK \u00B7 preamble cleaned") : tr("Parsed OK"),
              QStringLiteral("<b>%1</b> \u00B7 %2 \u00B7 %3")
                  .arg(num(r.score), count(r.recordCount, "record", "records"), count(r.fieldCount, "field", "fields")),
              {});
    const double vals[4] = {r.breakdown.records, r.breakdown.fields, r.breakdown.population, r.breakdown.consistency};
    for (int i = 0; i < 4; ++i) {
        m_barWidgets[i]->setFraction(vals[i] / kBars[i].max);
        m_barValues[i]->setText(QStringLiteral("%1/%2").arg(num(vals[i])).arg(kBars[i].max));
    }
    showBars(true);
    setParsed(r.header, r.records);
}

void TemplateLabView::sweep() {
    const QString raw = m_raw->toPlainText();
    const QString platform = m_platform->currentData().toString();
    if (raw.trimmed().isEmpty()) {
        showMatch(QStringLiteral("fail"), tr("Nothing to sweep"), tr("Paste raw CLI output first."), {});
        return;
    }
    if (platform.isEmpty()) {
        showMatch(QStringLiteral("fail"), tr("Nothing to sweep"), tr("Select a platform for the database sweep."), {});
        return;
    }
    if (!m_bridge->sweep(raw, platform, m_command->text().trimmed())) return;
    m_sweep->setEnabled(false);
    m_save->setEnabled(false);
    m_sweep->setText(tr("Sweeping\u2026"));
}

void TemplateLabView::sweepFinished(const LabSweep &s) {
    m_sweep->setEnabled(true);
    m_save->setEnabled(true);
    m_sweep->setText(tr("Test vs database"));
    showBars(false);
    markLine(m_template, 0);
    markLine(m_raw, 0);
    m_ruleLine = 0;
    m_inputLine.clear();
    if (!s.ok) {
        m_pill->clear();
        showMatch(QStringLiteral("fail"), tr("Sweep failed"), {}, s.error);
        setParsed({}, {});
        return;
    }
    const bool fallback = !s.primary.success && s.hasFallback && s.fallback.success;
    const LabParse &best = fallback ? s.fallback : s.primary;
    const QString mine = m_name->text().trimmed();
    if (best.success) {
        m_pill->setText(tr("db best %1").arg(num(best.score)));
        QString meta = tr("Score <b>%1</b> \u00B7 %2").arg(num(best.score), count(best.recordCount, "record", "records"));
        if (!mine.isEmpty())
            meta += best.templateName == mine ? tr(" \u00B7 your template wins")
                                              : tr(" \u00B7 your template (<code>%1</code>) is not the winner")
                                                    .arg(mine.toHtmlEscaped());
        meta += QStringLiteral("<br>") +
                tr("filter <code>%1</code> \u00B7 %2 tried").arg(best.filter.toHtmlEscaped()).arg(best.tried);
        if (fallback)
            meta += QStringLiteral("<br>") + tr("Nothing named for the command scored %1 or more; this is the best "
                                                "template for the vendor.").arg(15);
        showMatch(fallback ? QStringLiteral("fallback") : QStringLiteral("ok"),
                  fallback ? tr("Database sweep \u2014 fallback match") : tr("Database sweep \u2014 best match"), meta,
                  best.templateName);
        setParsed(best.header, best.records);
    } else {
        m_pill->setText(tr("no db match"));
        QString meta = tr("filter <code>%1</code> \u00B7 %2 tried").arg(s.primary.filter.toHtmlEscaped()).arg(s.primary.tried);
        if (!s.primary.templateName.isEmpty())
            meta += QStringLiteral("<br>") + tr("Best attempt <code>%1</code> scored %2, under the 15 a parse needs.")
                                                 .arg(s.primary.templateName.toHtmlEscaped(), num(s.primary.score));
        if (!s.primary.error.isEmpty()) meta += QStringLiteral("<br>") + s.primary.error.toHtmlEscaped();
        showMatch(QStringLiteral("fail"), tr("No database match"), meta, {});
        setParsed({}, {});
    }
    showCandidates(s.candidates, best.success ? best.templateName : QString());
}

// ---- save, clear, load ---------------------------------------------------

void TemplateLabView::setEditing(qint64 rowid, qint64 id) {
    m_editing = rowid;
    if (rowid <= 0) {
        m_editState->setText(tr("New template"));
        return;
    }
    m_editState->setText(id > 0 ? tr("Editing row %1 \u00B7 id %2").arg(rowid).arg(id) : tr("Editing row %1").arg(rowid));
}

void TemplateLabView::save() {
    const QString name = m_name->text().trimmed();
    const QString content = m_template->toPlainText();
    if (name.isEmpty()) {
        showMatch(QStringLiteral("fail"), tr("Not saved"), tr("A template name is required to save."), {});
        return;
    }
    if (content.trimmed().isEmpty()) {
        showMatch(QStringLiteral("fail"), tr("Not saved"), tr("The template is empty."), {});
        return;
    }
    if (m_lastCompileOk == 0 &&
        QMessageBox::question(this, tr("Save template"),
                              tr("The last test showed a compile error. A template that does not compile is never "
                                 "tried by a parse.\n\nSave anyway?")) != QMessageBox::Yes)
        return;
    if (!m_bridge->save(m_editing, name, content)) return;
    m_save->setEnabled(false);
    m_sweep->setEnabled(false);
    m_save->setText(tr("Saving\u2026"));
}

void TemplateLabView::saveFinished(const LabSaved &r) {
    m_sweep->setEnabled(true);
    m_save->setEnabled(true);
    m_save->setText(tr("Save to database"));
    if (!r.ok) {
        showMatch(QStringLiteral("fail"), tr("Not saved"), r.error.toHtmlEscaped(), {});
        return;
    }
    setEditing(r.rowid, r.id);
    if (!r.reloadError.isEmpty()) {
        showMatch(QStringLiteral("fallback"), tr("Saved \u2014 engine not reloaded"),
                  tr("The template is in the database, but captures keep the previous templates until "
                     "the engine reloads."),
                  r.reloadError);
    }
    m_save->setText(tr("Saved \u2713"));
    QTimer::singleShot(1500, m_save, [b = m_save, this] { b->setText(tr("Save to database")); });
    emit status(tr("Saved %1").arg(r.name));
    refresh();
}

void TemplateLabView::clearEditor() {
    setEditing(0, 0);
    m_lastCompileOk = -1;
    m_name->clear();
    m_template->clear();
    m_command->clear();
    m_sampleText.clear();
    m_sample->setEnabled(false);
    showPlaceholder(tr("Cleared. Write or load a template to test."));
    setParsed({}, {});
}

bool TemplateLabView::loadTemplate(qint64 rowid, QString *err) {
    LabTemplate t;
    if (!m_bridge->get(rowid, &t, err)) return false;
    if (!m_refreshed) refresh();
    m_lastCompileOk = -1;
    m_name->setText(t.item.name);
    m_template->setPlainText(t.content);
    setEditing(t.item.rowid, t.item.id);
    const QStringList parts = t.item.name.split(QLatin1Char('_'));
    if (parts.size() >= 2) {
        const int i = m_platform->findData(parts.at(0) + QLatin1Char('_') + parts.at(1));
        if (i >= 0) m_platform->setCurrentIndex(i);
    }
    m_sampleText = t.sample;
    m_sample->setEnabled(!t.sample.trimmed().isEmpty());
    showPage(Page::Author);
    showPlaceholder(t.sample.trimmed().isEmpty()
                        ? tr("Loaded. Paste output and press Test template.")
                        : tr("Loaded. Paste output, or use the sample stored with it, and press Test template."));
    setParsed({}, {});
    return true;
}

bool TemplateLabView::loadFromStore(const QString &canonical, const QString &type, const QString &file,
                                    QString *err) {
    if (!m_store || !m_store->isOpen()) {
        if (err) *err = tr("No store is open");
        return false;
    }
    QVector<StoreType> types;
    if (!m_store->types(canonical, &types, err)) return false;
    QString chosen = file;
    if (chosen.isEmpty()) {
        for (const StoreType &st : types)
            if (st.type == type) chosen = st.file;
    }
    if (chosen.isEmpty()) {
        if (err) *err = tr("%1 has no stored %2").arg(canonical, type);
        return false;
    }
    QByteArray raw;
    if (!m_store->read(canonical, type, chosen, &raw, err)) return false;

    QString platform, command;
    QVector<StoreDevice> devices;
    QStringList unreadable;
    if (m_store->devices(&devices, &unreadable, nullptr))
        for (const StoreDevice &d : devices)
            if (d.canonical == canonical) platform = d.platform;
    QVector<StoreAttempt> history;
    if (m_store->history(canonical, type, &history, nullptr)) {
        for (const StoreAttempt &a : history) {
            if (!a.command.isEmpty()) command = a.command;  // the latest attempt's, unless this file's is found
            if (a.file == chosen && !a.command.isEmpty()) {
                command = a.command;
                break;
            }
        }
    }
    if (!m_refreshed) refresh();
    m_raw->setPlainText(QString::fromUtf8(raw));
    setPlatform(platform);
    m_command->setText(command);
    showPage(Page::Author);
    showPlaceholder(tr("Loaded %1 / %2 (%3). Test a template against it, or sweep the database.")
                        .arg(canonical, type, chosen));
    setParsed({}, {});
    return true;
}

void TemplateLabView::chooseFromStore() {
    if (!m_store || !m_store->isOpen()) {
        emit status(tr("Open a store first"));
        return;
    }
    QVector<StoreDevice> devices;
    QStringList unreadable;
    QString err;
    if (!m_store->devices(&devices, &unreadable, &err)) {
        emit status(err);
        return;
    }
    QDialog dlg(this);
    dlg.setWindowTitle(tr("Load from store"));
    dlg.resize(560, 420);
    auto *col = new QVBoxLayout(&dlg);
    auto *lists = new QHBoxLayout;
    auto *devList = new QListWidget(&dlg);
    auto *typeList = new QListWidget(&dlg);
    for (const StoreDevice &d : devices) {
        auto *item = new QListWidgetItem(d.platform.isEmpty() ? d.canonical
                                                              : QStringLiteral("%1  \u00B7  %2").arg(d.canonical, d.platform));
        item->setData(Qt::UserRole, d.canonical);
        devList->addItem(item);
    }
    lists->addWidget(devList, 3);
    lists->addWidget(typeList, 2);
    col->addLayout(lists, 1);
    auto *buttons = new QDialogButtonBox(QDialogButtonBox::Cancel, &dlg);
    QPushButton *load = buttons->addButton(tr("Load"), QDialogButtonBox::AcceptRole);
    load->setProperty("primary", QStringLiteral("true"));
    load->setEnabled(false);
    col->addWidget(buttons);
    connect(buttons, &QDialogButtonBox::rejected, &dlg, &QDialog::reject);
    connect(buttons, &QDialogButtonBox::accepted, &dlg, &QDialog::accept);
    connect(devList, &QListWidget::currentItemChanged, &dlg, [this, typeList, load](QListWidgetItem *cur) {
        typeList->clear();
        load->setEnabled(false);
        if (!cur) return;
        QVector<StoreType> types;
        if (!m_store->types(cur->data(Qt::UserRole).toString(), &types, nullptr)) return;
        for (const StoreType &t : types) {
            if (t.file.isEmpty()) continue;
            auto *item = new QListWidgetItem(QStringLiteral("%1  \u00B7  %2").arg(t.type, t.last.toString(QStringLiteral("MM-dd HH:mm"))));
            item->setData(Qt::UserRole, t.type);
            typeList->addItem(item);
        }
    });
    connect(typeList, &QListWidget::currentItemChanged, &dlg, [load](QListWidgetItem *cur) { load->setEnabled(cur); });
    connect(typeList, &QListWidget::itemDoubleClicked, &dlg, &QDialog::accept);
    if (dlg.exec() != QDialog::Accepted || !devList->currentItem() || !typeList->currentItem()) return;
    if (!loadFromStore(devList->currentItem()->data(Qt::UserRole).toString(),
                       typeList->currentItem()->data(Qt::UserRole).toString(), QString(), &err))
        emit status(err);
}

void TemplateLabView::removeTemplate(qint64 rowid) {
    if (rowid > 0) m_bridge->remove(rowid);
}

}  // namespace omegacat
