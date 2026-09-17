// app/exporting.cpp
#include "exporting.h"

#include <QClipboard>
#include <QDir>
#include <QFileInfo>
#include <QGuiApplication>
#include <QJsonObject>
#include <QPointer>
#include <QPushButton>
#include <QRegularExpression>
#include <QSaveFile>
#include <QSettings>
#include <QTimer>

namespace omegacat {

ExportTable parsedTable(const QStringList &header, const QJsonArray &records, const QString &listSeparator) {
    ExportTable t;
    t.header = header;
    t.rows.reserve(records.size());
    for (const QJsonValue &v : records) {
        const QJsonObject rec = v.toObject();
        QStringList row;
        row.reserve(header.size());
        for (const QString &h : header) {
            const QJsonValue cell = rec.value(h);
            if (cell.isArray()) {
                QStringList parts;
                for (const QJsonValue &e : cell.toArray()) parts << e.toString();
                row << parts.join(listSeparator);
            } else {
                row << cell.toString();
            }
        }
        t.rows.append(row);
    }
    return t;
}

namespace {

void csvField(QByteArray *out, const QString &field) {
    const QByteArray f = field.toUtf8();
    const bool quote = f.contains(',') || f.contains('"') || f.contains('\r') || f.contains('\n');
    if (!quote) {
        out->append(f);
        return;
    }
    out->append('"');
    for (char c : f) {
        if (c == '"') out->append('"');
        out->append(c);
    }
    out->append('"');
}

void csvRecord(QByteArray *out, const QStringList &fields) {
    for (int i = 0; i < fields.size(); ++i) {
        if (i) out->append(',');
        csvField(out, fields.at(i));
    }
    out->append("\r\n");
}

QString tsvField(QString s) {
    static const QRegularExpression breaks(QStringLiteral("[\\t\\r\\n]+"));
    return s.replace(breaks, QStringLiteral(" "));
}

}  // namespace

QByteArray toCsv(const ExportTable &table) {
    QByteArray out;
    csvRecord(&out, table.header);
    for (const QStringList &row : table.rows) csvRecord(&out, row);
    return out;
}

QString toTsv(const ExportTable &table) {
    QStringList lines;
    lines.reserve(table.rows.size() + 1);
    QStringList h;
    for (const QString &f : table.header) h << tsvField(f);
    lines << h.join(QLatin1Char('\t'));
    for (const QStringList &row : table.rows) {
        QStringList r;
        for (const QString &f : row) r << tsvField(f);
        lines << r.join(QLatin1Char('\t'));
    }
    return lines.join(QLatin1Char('\n')) + QLatin1Char('\n');
}

bool writeExport(const QString &path, const QByteArray &data, QString *err) {
    QSaveFile f(path);
    if (!f.open(QIODevice::WriteOnly)) {
        if (err) *err = QStringLiteral("%1: %2").arg(QDir::toNativeSeparators(path), f.errorString());
        return false;
    }
    if (f.write(data) != data.size() || !f.commit()) {
        if (err) *err = QStringLiteral("%1: %2").arg(QDir::toNativeSeparators(path), f.errorString());
        return false;
    }
    return true;
}

QString exportFileName(const QStringList &parts, const QString &suffix) {
    static const QRegularExpression unsafe(QStringLiteral("[^A-Za-z0-9._-]+"));
    QStringList clean;
    for (QString p : parts) {
        p.replace(unsafe, QStringLiteral("_"));
        p = p.trimmed();
        if (!p.isEmpty()) clean << p;
    }
    return clean.join(QLatin1Char('_')) + QLatin1Char('.') + suffix;
}

QString exportDirectory() {
    const QString d = QSettings().value(QStringLiteral("export/directory")).toString();
    return !d.isEmpty() && QFileInfo(d).isDir() ? d : QDir::homePath();
}

void setExportDirectory(const QString &fileInIt) {
    QSettings().setValue(QStringLiteral("export/directory"), QFileInfo(fileInIt).absolutePath());
}

void copyToClipboard(QPushButton *button, const QString &text) {
    QGuiApplication::clipboard()->setText(text);
    if (!button) return;
    // The label the button had before any copy, so two quick copies do not
    // leave it reading "Copied" for good.
    QVariant original = button->property("copyLabel");
    if (!original.isValid()) {
        original = button->text();
        button->setProperty("copyLabel", original);
    }
    button->setText(QStringLiteral("Copied \u2713"));
    const QPointer<QPushButton> b(button);
    QTimer::singleShot(1500, button, [b, original] {
        if (b) b->setText(original.toString());
    });
}

}  // namespace omegacat
