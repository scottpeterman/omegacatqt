// app/panel.h
//
// The container every section of the window sits in: a titled card, after
// sc2's widgets/panel.py. Title on the left, a slot on the right of the bar
// for a panel's own controls, and a body layout below.
//
// Unlike sc2's Panel it carries no colours and no apply_theme(): it sets
// style roles and the application stylesheet does the rest.

#ifndef OMEGACAT_APP_PANEL_H
#define OMEGACAT_APP_PANEL_H

#include <QFrame>

class QHBoxLayout;
#include <QStringList>
class QLabel;
class QPlainTextEdit;
class QVBoxLayout;

namespace omegacat {

class Panel : public QFrame {
    Q_OBJECT
public:
    explicit Panel(const QString &title, QWidget *parent = nullptr);

    // Right-hand side of the title bar, for the panel's own controls.
    QHBoxLayout *barLayout() const { return m_bar; }
    QVBoxLayout *bodyLayout() const { return m_body; }

private:
    QHBoxLayout *m_bar = nullptr;
    QVBoxLayout *m_body = nullptr;
};

// An uppercase, letter-spaced caption in the sc2 style.
QLabel *makeCaption(const QString &text, const char *role, int pointSize,
                    QWidget *parent = nullptr);

// Caption, field, and an optional line of explanation under it.
void addField(QVBoxLayout *col, const QString &caption, QWidget *field,
              const QString &hint = QString());

// Marks one line (1-based) of a plain-text view in the current theme's
// selection colour and scrolls it to the middle. Line 0 clears the mark.
// Colours are resolved when called, so a view re-calls this on a theme change.
void markLine(QPlainTextEdit *view, int line);

// Splits a free-text list on commas, semicolons and whitespace.
QStringList splitList(const QString &text);

}  // namespace omegacat

#endif
