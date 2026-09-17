// app/appsettings.h
//
// Every persisted application setting, and its default, in one place.
//
// WHERE THEY LIVE. QSettings in its native format, keyed by the organisation
// and application names main.cpp sets ("omegacat"/"omegacat"), as omegamaps
// keeps its own:
//
//   macOS    ~/Library/Preferences/com.omegacat.omegacat.plist
//            (read it with `defaults read com.omegacat.omegacat`; the file is
//            cached by cfprefsd, so an edit to it by hand may not be seen)
//   Linux    ~/.config/omegacat/omegacat.conf
//   Windows  HKEY_CURRENT_USER\Software\omegacat\omegacat
//
// fileName() returns the one in use, and the Settings dialog shows it.
//
// Not here: the vault, inventory, template database and diff rules, which are
// data in ~/.omegacat rather than preferences.
//
// A KEY THAT WAS NEVER SAVED IS NOT A DEFAULT. Capture defaults that nobody
// set follow the library's (capture types marked default, its host key
// policy), so a library change reaches someone who never opened Settings;
// saving writes every value and pins them.
#ifndef OMEGACAT_APP_APPSETTINGS_H
#define OMEGACAT_APP_APPSETTINGS_H

#include <QString>
#include <QStringList>

namespace omegacat {

struct CaptureDefaults {
    QStringList types;
    QString hostKeys;  // "strict" or "tofu"
    bool legacy = false;
    bool parse = true;
    QString credentialTags;

    bool operator==(const CaptureDefaults &o) const {
        return types == o.types && hostKeys == o.hostKeys && legacy == o.legacy && parse == o.parse &&
               credentialTags == o.credentialTags;
    }
};

namespace appsettings {

QString fileName();

// "light", "dark" or "cyber".
QString theme();
void setTheme(const QString &key);

// The store opened at launch: the default store when one is set, otherwise
// the last store opened, otherwise ~/captures.
QString launchStore();
// "" when launch opens the last store used.
QString defaultStore();
void setDefaultStore(const QString &path);
QString lastStore();
void setLastStore(const QString &path);

// The library's defaults, before anything was saved.
CaptureDefaults builtinCaptureDefaults();
CaptureDefaults captureDefaults();
void setCaptureDefaults(const CaptureDefaults &d);

}  // namespace appsettings

}  // namespace omegacat

#endif
