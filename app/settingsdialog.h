// app/settingsdialog.h
//
// File > Settings: the application's defaults.
//
//   General   theme, and which store opens at launch
//   Capture   the Run form's starting values: capture types, host key policy,
//             legacy algorithms, parsing, credential tags
//
// The theme previews while the dialog is open and reverts on Cancel, so a
// theme can be judged against the dialog itself. Nothing else applies until
// Save. Restore defaults puts the library's defaults into the fields and saves
// nothing on its own.
#ifndef OMEGACAT_APP_SETTINGSDIALOG_H
#define OMEGACAT_APP_SETTINGSDIALOG_H

#include <QDialog>

#include "appsettings.h"

class QButtonGroup;
class QCheckBox;
class QComboBox;
class QLabel;
class QLineEdit;
class QListWidget;
class QPushButton;
class QRadioButton;
class QTabWidget;

namespace omegacat {

class ModalFrame;

class SettingsDialog : public QDialog {
    Q_OBJECT
public:
    explicit SettingsDialog(QWidget *parent = nullptr);

    // What the fields say now.
    QString themeKey() const;
    QString defaultStore() const;  // "" for the last store used
    CaptureDefaults captureDefaults() const;

    // Checks the fields and writes them. False, with the reason shown in the
    // dialog, when a field is not acceptable.
    bool save();
    void restoreDefaults();

    // For the probe.
    QTabWidget *tabs() const { return m_tabs; }
    QComboBox *themeBox() const { return m_theme; }
    QRadioButton *lastStoreButton() const { return m_useLast; }
    QRadioButton *fixedStoreButton() const { return m_useFixed; }
    QLineEdit *storeEdit() const { return m_store; }
    QListWidget *typesList() const { return m_types; }
    QComboBox *hostKeysBox() const { return m_hostKeys; }
    QCheckBox *legacyBox() const { return m_legacy; }
    QCheckBox *parseBox() const { return m_parse; }
    QLineEdit *credentialTagsEdit() const { return m_credTags; }
    QLabel *errorLabel() const { return m_error; }

    // Cancel: puts back the theme the dialog opened on.
    void reject() override;

protected:
    void showEvent(QShowEvent *event) override;

private:
    void load();
    void showError(const QString &text);

    ModalFrame *m_frame = nullptr;
    QTabWidget *m_tabs = nullptr;
    QString m_themeAtOpen;
    bool m_aligned = false;

    QComboBox *m_theme = nullptr;
    QRadioButton *m_useLast = nullptr;
    QRadioButton *m_useFixed = nullptr;
    QLineEdit *m_store = nullptr;
    QPushButton *m_browse = nullptr;

    QListWidget *m_types = nullptr;
    QComboBox *m_hostKeys = nullptr;
    QCheckBox *m_legacy = nullptr;
    QCheckBox *m_parse = nullptr;
    QLineEdit *m_credTags = nullptr;

    QLabel *m_error = nullptr;
};

}  // namespace omegacat

#endif
