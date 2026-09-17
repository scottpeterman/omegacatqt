// app/inventorymanagerdialog.h
//
// The inventory manager: edit one device, edit many, and arrange folders.
//
// Every action is one inventory::apply -- a list of edits written once or not
// at all -- followed by a reload from the file. The dialog never patches its
// own tree to match what it believes it wrote: the file decides, as the
// credential manager lets the vault decide.
//
// Selecting one device edits all of its fields. Selecting several edits
// platform, legacy and credential across all of them, each left "(unchanged)"
// unless touched, as one patch. Name, host and port are per device and are
// disabled for a multi-selection.
//
// Keys change when a host or port is edited, and devices disappear when
// deleted. keyChanges() is every such change over the dialog's life, original
// key to final key, for the Run view to carry its ticks across.
#pragma once

#include <QDialog>
#include <QHash>
#include <QJsonArray>
#include <QStringList>

#include "inventory.h"

class QCheckBox;
class QComboBox;
class QLabel;
class QLineEdit;
class QPushButton;
class QSpinBox;
class QTreeWidget;
class QTreeWidgetItem;

namespace omegacat {

class ModalFrame;
class Vault;

class InventoryManagerDialog : public QDialog {
    Q_OBJECT

public:
    // focusKey selects that device on open.
    InventoryManagerDialog(const QString &inventoryPath, Vault *vault, const QString &focusKey = {},
                           QWidget *parent = nullptr);

    bool changed() const { return m_changed; }
    const QHash<QString, QString> &keyChanges() const { return m_keyChanges; }

    // Runs edits, reloads, and reselects. False with the reason shown inline.
    bool applyEdits(const QJsonArray &edits, const QStringList &reselectKeys = {});

    // Selection by key; the probe drives the dialog through these and the
    // form widgets below, exactly as a click would.
    void selectKeys(const QStringList &keys);
    QStringList selectedKeys() const;
    QString selectedFolder() const;

    QTreeWidget *tree() const { return m_tree; }
    QLineEdit *nameEdit() const { return m_name; }
    QLineEdit *hostEdit() const { return m_host; }
    QSpinBox *portSpin() const { return m_port; }
    QComboBox *platformBox() const { return m_platform; }
    QCheckBox *legacyBox() const { return m_legacy; }
    QComboBox *credentialBox() const { return m_credential; }
    QPushButton *saveButton() const { return m_save; }
    QLabel *notice() const { return m_notice; }

    // Puts the form in add mode for a folder. Save then creates the device.
    void beginAdd(const QString &folder);
    void save();

private:
    void buildUi();
    void reload();
    void selectionChanged();
    void fillForm();
    void setNotice(const QString &text);

    void newFolder();
    void renameFolder();
    void moveSelection();
    void deleteSelection();

    QString m_path;
    Vault *m_vault = nullptr;
    QVector<InventoryFolder> m_folders;
    QHash<QString, QString> m_keyChanges;
    bool m_changed = false;
    bool m_filling = false;
    QString m_addFolder;  // non-empty: the form is adding a device here

    ModalFrame *m_frame = nullptr;
    QLineEdit *m_filter = nullptr;
    QTreeWidget *m_tree = nullptr;
    QLabel *m_formTitle = nullptr;
    QLineEdit *m_name = nullptr;
    QLineEdit *m_host = nullptr;
    QSpinBox *m_port = nullptr;
    QComboBox *m_platform = nullptr;
    QLabel *m_hint = nullptr;
    QCheckBox *m_legacy = nullptr;
    QComboBox *m_credential = nullptr;
    QPushButton *m_save = nullptr;
    QPushButton *m_renameFolder = nullptr;
    QPushButton *m_add = nullptr;
    QPushButton *m_move = nullptr;
    QPushButton *m_delete = nullptr;
    QLabel *m_notice = nullptr;
};

}  // namespace omegacat
