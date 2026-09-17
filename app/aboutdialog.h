// app/aboutdialog.h
//
// The About box: the splash, the version and what it runs on, the licence, and
// an Attributions section. Modeless and one at a time -- a second request
// raises the one already open. Opened from the wordmark and from Help.
//
// ATTRIBUTIONS ARE COMPILED IN, NOT READ FROM BESIDE THE EXECUTABLE.
// licenses/THIRD_PARTY_NOTICES.md and the GPL, LGPL and Apache texts it cites
// are in resources.qrc, so the program carries its own notices however it
// reached someone -- a package, a copied binary, a build tree. A packaging
// script that stages licenses/ is still right, but the dialog does not depend
// on one having run.
//
// The splash is a 256-colour PNG, visually the same as the original: PNG is
// built into QtGui, where JPEG is an image-format plugin, and the application
// is kept free of those so a bundle needs none (app/CMakeLists.txt).

#ifndef OMEGACAT_APP_ABOUTDIALOG_H
#define OMEGACAT_APP_ABOUTDIALOG_H

#include <QDialog>
#include <QStringList>

class QComboBox;
class QLabel;
class QPushButton;
class QTextBrowser;
class QWidget;

namespace omegacat {

class AboutDialog : public QDialog {
    Q_OBJECT
public:
    // Opens the About box over parent, or raises the one already open.
    static AboutDialog *showFor(QWidget *parent);
    // The same, with the Attributions section open.
    static AboutDialog *showAttributionsFor(QWidget *parent);

    // Makes w open the About box when clicked: a pointing cursor, a tooltip
    // and a click handler. For a label that is not a button.
    static void makeTrigger(QWidget *w);

    // The documents the Attributions section offers, in order, as resource
    // paths. The first is the notices file.
    static QStringList documentPaths();

    void setAttributionsVisible(bool on);
    bool attributionsVisible() const;
    // Shows document i of documentPaths().
    void showDocument(int i);

    // The version, Qt and platform lines, as copied by Copy details.
    QString details() const { return m_details; }
    const QLabel *splash() const { return m_splash; }
    QTextBrowser *browser() const { return m_browser; }
    const QPushButton *aboutQtButton() const { return m_aboutQt; }

private:
    explicit AboutDialog(QWidget *parent);

    QLabel *m_splash = nullptr;
    QWidget *m_attributions = nullptr;
    QComboBox *m_documents = nullptr;
    QTextBrowser *m_browser = nullptr;
    QPushButton *m_toggle = nullptr;
    QPushButton *m_aboutQt = nullptr;
    QString m_details;
};

}  // namespace omegacat

#endif
