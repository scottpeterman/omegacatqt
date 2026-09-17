// app/exporting.h
//
// Getting data out of the views: CSV files, and text for the clipboard.
//
// CSV is RFC 4180: every field quoted when it holds a comma, a quote, a CR or
// an LF, quotes doubled, CRLF between records -- what Python's csv module
// writes and what Excel, Numbers and LibreOffice read without an import
// dialog. A List value (several strings in one parsed field) is one cell with
// its strings on separate lines, so nothing is lost and nothing is confused
// with a separator.
//
// No formula escaping. Spreadsheet "CSV injection" guards prefix a cell that
// starts with = + - @ with a quote, and device output is full of cells that
// start with "-" (an empty Junos parent interface, a negative counter); the
// guard would change the data to protect against a threat that does not exist
// for a file an engineer exported from their own captures.
#ifndef OMEGACAT_APP_EXPORTING_H
#define OMEGACAT_APP_EXPORTING_H

#include <QByteArray>
#include <QJsonArray>
#include <QString>
#include <QStringList>
#include <QVector>

class QPushButton;

namespace omegacat {

struct ExportTable {
    QStringList header;
    QVector<QStringList> rows;
};

// Parsed records as a table in header order. listSeparator joins a List
// value's strings: "\n" for CSV, ", " for the clipboard.
ExportTable parsedTable(const QStringList &header, const QJsonArray &records, const QString &listSeparator);

QByteArray toCsv(const ExportTable &table);

// Tab-separated with a header line, which pastes into a spreadsheet as
// columns. A tab or line break inside a cell becomes a space: TSV has no
// quoting a spreadsheet agrees on.
QString toTsv(const ExportTable &table);

// Writes atomically (QSaveFile): an export that fails leaves no half file.
bool writeExport(const QString &path, const QByteArray &data, QString *err);

// A file name from parts: characters that are not safe in a file name on any
// platform become '_'.
QString exportFileName(const QStringList &parts, const QString &suffix);

// The directory the last export went to, for the next save dialog.
QString exportDirectory();
void setExportDirectory(const QString &fileInIt);

// Puts text on the clipboard and says so on the button for a moment.
void copyToClipboard(QPushButton *button, const QString &text);

}  // namespace omegacat

#endif
