// app/omegatheme.h
//
// The piece of Omega's theme (omegasshqt theme/tokens.h) that the dialogs
// copied from Omega read directly: the window-button colours. Mapped from
// OmegaCat's own tokens rather than bringing Omega's theme system across --
// one theme drives the application, and these five values follow it.
//
// Everything else those dialogs style comes through the application
// stylesheet, where OmegaCat's theme.cpp carries rules for their roles
// (chrome, well, bar, noticebox, fieldlabel, chips, mono, destructive).
#ifndef OMEGACAT_APP_OMEGATHEME_H
#define OMEGACAT_APP_OMEGATHEME_H

#include <algorithm>

namespace omegacat::theme {

struct Rgba {
    int r = 0, g = 0, b = 0, a = 255;
};

struct Tokens {
    Rgba ink;
    Rgba inkMuted;
    Rgba bgSelected;
    Rgba bgChrome;
    Rgba danger;
};

inline Rgba mix(const Rgba &a, const Rgba &b, double t) {
    const auto lerp = [t](int x, int y) { return std::clamp(int(x + (y - x) * t + 0.5), 0, 255); };
    return {lerp(a.r, b.r), lerp(a.g, b.g), lerp(a.b, b.b), lerp(a.a, b.a)};
}

}  // namespace omegacat::theme

#endif
