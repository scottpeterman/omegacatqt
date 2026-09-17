// app/currenttokens.cpp
//
// Omega kept its own token store, set when a theme was applied. OmegaCat has
// one theme, owned by ThemeManager, so the tokens are read from it on every
// call and setCurrentTokens has nothing to do.

#include "currenttokens.h"

#include "theme.h"

namespace omegacat {
namespace {

theme::Rgba rgba(const QColor &c) { return {c.red(), c.green(), c.blue(), c.alpha()}; }

}  // namespace

const theme::Tokens &currentTokens() {
    static thread_local theme::Tokens t;
    const Tokens &k = ThemeManager::instance().tokens();
    t.ink = rgba(k.textPrimary);
    t.inkMuted = rgba(k.textSecondary);
    t.bgSelected = rgba(k.bgSelected);
    t.bgChrome = rgba(k.bgSecondary);
    t.danger = rgba(k.danger);
    return t;
}

void setCurrentTokens(const theme::Tokens &) {}

}  // namespace omegacat
