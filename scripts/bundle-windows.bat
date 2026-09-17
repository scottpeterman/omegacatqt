@echo off
REM scripts\bundle-windows.bat
REM
REM Builds the Windows release: a folder, and optionally a zip, holding the
REM application, the command-line tools, and everything they need to run on a
REM machine with nothing installed.
REM
REM   scripts\bundle-windows.bat
REM   scripts\bundle-windows.bat --qt C:\Qt\6.10.3\msvc2022_64 --zip
REM   scripts\bundle-windows.bat --version v0.3.0 --zip
REM
REM   dist\omegacat\                               omegacat.exe, capture.exe,
REM                                                ocvault.exe, Qt, the MSVC
REM                                                runtime, LICENSE, licenses\
REM   dist\omegacat-<version>-windows-x64.zip       with --zip
REM
REM Run it from an "x64 Native Tools Command Prompt for VS 2022", with a
REM mingw-w64 gcc also on PATH. Both, not either: see TWO COMPILERS below.
REM
REM This is omegamaps' scripts\bundle-windows.bat with the map viewer and its
REM WebEngine payload removed, capture and ocvault as the tools, the version
REM stamped into the application and the tools alike, and the SVG image
REM plugin added to the gates. Everything about finding Qt, the two compilers
REM and the MSVC runtime is unchanged, because none of it is about omegamaps.
REM
REM TWO COMPILERS. The run model is a Go c-archive (capi\), and cgo on Windows
REM compiles with gcc -- there is no MSVC cgo backend. link.exe then links that
REM archive into the Qt application. So the build needs mingw-w64 gcc to
REM produce the archive and MSVC to consume it, and the two have to agree on
REM the target: an i686 gcc, or MSYS2's "msys" (not "mingw64") gcc, produces an
REM archive that either fails to link or links into an image the loader
REM refuses with 0xc000007b -- a dialog that names no DLL and no reason.
REM
REM   Qt          --qt <prefix>
REM               %OMEGACAT_QT%
REM               %CMAKE_PREFIX_PATH%
REM               %Qt6_DIR%  (walked back up from lib\cmake\Qt6)
REM               the newest C:\Qt\6.*\msvc*_64
REM
REM windeployqt is taken from the Qt prefix, NOT from PATH: one from a
REM different Qt copies DLLs that do not match what the binary asks for, and
REM the result runs on the build machine and fails on a clean one.
REM
REM Options:
REM   --zip              also write the zip
REM   --version <v>      stamp this version (default: git describe --tags)
REM   --build-dir <dir>  default build-win
REM   --help

setlocal enabledelayedexpansion
set "ROOT=%~dp0.."
pushd "%ROOT%"

set "QTPREFIX="
set "MAKEZIP=0"
set "BUILD_DIR=build-win"
set "STAGE=dist\omegacat"
set "VERSION="
set "VERSIONSET=0"

REM --- options ---------------------------------------------------------------
:parse
if "%~1"=="" goto parsed
if /i "%~1"=="--zip" (set "MAKEZIP=1" & shift & goto parse)
if /i "%~1"=="--qt" (
    if "%~2"=="" (echo error: --qt needs a path 1>&2 & goto fail)
    set "QTPREFIX=%~f2" & shift & shift & goto parse
)
if /i "%~1"=="--version" (
    if "%~2"=="" (echo error: --version needs a value 1>&2 & goto fail)
    set "VERSION=%~2" & set "VERSIONSET=1" & shift & shift & goto parse
)
if /i "%~1"=="--build-dir" (
    if "%~2"=="" (echo error: --build-dir needs a path 1>&2 & goto fail)
    set "BUILD_DIR=%~2" & shift & shift & goto parse
)
if /i "%~1"=="--help" goto usage
if /i "%~1"=="-h" goto usage
echo error: unknown option: %~1 1>&2
goto fail

:parsed

REM --- Qt --------------------------------------------------------------------

if "%QTPREFIX%"=="" if not "%OMEGACAT_QT%"=="" set "QTPREFIX=%OMEGACAT_QT%"
if "%QTPREFIX%"=="" if not "%CMAKE_PREFIX_PATH%"=="" set "QTPREFIX=%CMAKE_PREFIX_PATH%"

REM Qt6_DIR points at <prefix>\lib\cmake\Qt6, so walk back up three.
if "%QTPREFIX%"=="" if not "%Qt6_DIR%"=="" (
    for %%P in ("%Qt6_DIR%\..\..\..") do set "QTPREFIX=%%~fP"
)

REM Newest first, and NOT with dir /o-n. That sorts as TEXT, which puts 6.8.3
REM above 6.10.3 -- and the resulting mismatch is the exact thing this script
REM is supposed to prevent, because it deploys one Qt's DLLs beside a binary
REM linked against another. The parts are compared as numbers instead, in
REM :findqt below.
REM
REM CALLED, NOT INLINED, and that is not tidiness. This used to be a for /f
REM around a PowerShell one-liner sitting inside "if ... if exist ( ... )".
REM cmd parses a parenthesised block in full before running any of it, which
REM eats one level of caret escaping -- so the ^\| inside the for /f command
REM string became a real pipe, split the statement, and the loop produced
REM nothing. QTPREFIX stayed empty and the script reported "no Qt prefix" on a
REM machine with Qt exactly where it was looking. A subroutine is parsed when
REM it is called, at which point single carets mean what they say.
if "%QTPREFIX%"=="" call :findqt

if not "%QTPREFIX%"=="" if "%QTGUESSED%"=="1" (
    echo ==^> guessed Qt at %QTPREFIX%
    echo     ^(pass --qt or set OMEGACAT_QT to choose a different one^)
)

if "%QTPREFIX%"=="" (
    echo error: no Qt prefix. Either:
    echo         scripts\bundle-windows.bat --qt C:\Qt\6.10.3\msvc2022_64
    echo     or  set OMEGACAT_QT=C:\Qt\6.10.3\msvc2022_64
    if exist "C:\Qt" (
        echo     C:\Qt exists but holds no 6.x directory with an msvc*_64 kit:
        for /f "delims=" %%D in ('dir /b /ad "C:\Qt" 2^>nul') do echo         C:\Qt\%%D
    ) else (
        echo     C:\Qt does not exist, so nothing could be guessed.
    )
    goto fail
)

REM Strip a trailing backslash once, here. It is harmless in every path this
REM script builds -- "%QTPREFIX%\bin\windeployqt.exe" tolerates the double
REM separator -- but the last gate compares %QTPREFIX% against the cached
REM Qt6_DIR as a substring, and a prefix ending in \ becomes one ending in /
REM that the cache entry does not contain. The result is the bundle failing
REM its own consistency check over a typed backslash.
if "%QTPREFIX:~-1%"=="\" set "QTPREFIX=%QTPREFIX:~0,-1%"
if not exist "%QTPREFIX%\bin\windeployqt.exe" (
    echo error: no windeployqt.exe under %QTPREFIX%\bin 1>&2
    echo     That does not look like a Qt prefix. It should be the directory
    echo     holding bin\, lib\ and include\ -- e.g. C:\Qt\6.10.3\msvc2022_64,
    echo     not C:\Qt and not the lib\cmake\Qt6 inside it.
    goto fail
)

REM A MINGW Qt CANNOT BE USED HERE even though mingw gcc is also required. The
REM Qt libraries are linked by link.exe into an MSVC binary; a mingw_64 Qt
REM exports a different C++ ABI and link.exe cannot resolve a symbol of it.
REM The failure is several hundred unresolved Qt symbols, which reads like a
REM missing library rather than the wrong kit.
echo %QTPREFIX% | findstr /i /c:"mingw" >nul
if not errorlevel 1 (
    echo error: %QTPREFIX% looks like a MinGW Qt kit. 1>&2
    echo     The application is linked by MSVC, so Qt must be an msvc*_64 kit.
    echo     mingw gcc is still needed -- but only to compile the Go c-archive.
    goto fail
)

REM --- toolchain -------------------------------------------------------------

where cl.exe >nul 2>nul
if errorlevel 1 (
    echo error: cl.exe not on PATH. 1>&2
    echo     Run this from an "x64 Native Tools Command Prompt for VS 2022",
    echo     or run vcvars64.bat in this shell first.
    goto fail
)

where go.exe >nul 2>nul
if errorlevel 1 (
    echo error: go.exe not on PATH; the run model is a Go c-archive. 1>&2
    goto fail
)

where gcc.exe >nul 2>nul
if errorlevel 1 (
    echo error: gcc.exe not on PATH. 1>&2
    echo     cgo has no MSVC backend: capi\ is compiled by mingw-w64 gcc and
    echo     only then linked by link.exe. Install the MSYS2 mingw-w64 x86_64
    echo     toolchain -- or w64devkit -- and put its bin\ on PATH.
    echo         pacman -S mingw-w64-x86_64-gcc
    echo     and use C:\msys64\mingw64\bin, NOT C:\msys64\usr\bin.
    goto fail
)

REM The target triple, not the version. See TWO COMPILERS at the top.
set "GCCTRIPLE="
for /f "delims=" %%T in ('gcc -dumpmachine 2^>nul') do set "GCCTRIPLE=%%T"
echo !GCCTRIPLE! | findstr /i /c:"x86_64-w64-mingw32" >nul
if errorlevel 1 (
    echo error: gcc targets !GCCTRIPLE!, not x86_64-w64-mingw32. 1>&2
    echo     A 32-bit or MSYS-target gcc produces a c-archive that link.exe
    echo     either rejects or links into an image the loader refuses with
    echo     0xc000007b at startup -- a dialog that names no DLL.
    for /f "delims=" %%W in ('where gcc.exe') do echo         found: %%W
    goto fail
)

REM --- what it settled on ----------------------------------------------------

echo ==^> Qt         %QTPREFIX%
echo ==^> gcc        !GCCTRIPLE!
echo ==^> build dir  %BUILD_DIR%

REM --- version -----------------------------------------------------------------
REM A subroutine for the same reason as :findqt: a for /f with 2^>nul inside a
REM parenthesised block loses its caret to the block parse.
if "%VERSIONSET%"=="0" call :gitversion
set "NAMEVERSION=%VERSION%"
if "%NAMEVERSION%"=="" set "NAMEVERSION=dev"
set "ZIPNAME=dist\omegacat-%NAMEVERSION%-windows-x64.zip"
if "%VERSION%"=="" (echo ==^> version    ^(unstamped^)) else (echo ==^> version    %VERSION%)

REM --- build -----------------------------------------------------------------

REM A CACHED Qt6_DIR BEATS -DCMAKE_PREFIX_PATH, so an existing build directory
REM configured against a different Qt keeps using it and says nothing: the
REM binary links one Qt while windeployqt below deploys another. Wiping is the
REM only reliable answer.
if exist "%BUILD_DIR%\CMakeCache.txt" (
    findstr /c:"Qt6_DIR:PATH=" "%BUILD_DIR%\CMakeCache.txt" > "%TEMP%\omegacat_qtdir.txt" 2>nul
    set "CACHED="
    for /f "tokens=2 delims==" %%V in ('type "%TEMP%\omegacat_qtdir.txt"') do set "CACHED=%%V"
    del "%TEMP%\omegacat_qtdir.txt" >nul 2>nul
    if not "!CACHED!"=="" (
        echo !CACHED! | findstr /i /c:"%QTPREFIX:\=/%" >nul
        if errorlevel 1 (
            echo ==^> %BUILD_DIR% was configured against a different Qt:
            echo         cached: !CACHED!
            echo         wanted: %QTPREFIX%
            echo     wiping it, or the build and windeployqt would disagree.
            rmdir /s /q "%BUILD_DIR%"
        )
    )
)

REM The notices are compiled into the application, so a stale file ships
REM inside the executable, not only beside it. Checked when Python is here --
REM asked for its version first, because on a machine without Python the
REM python.exe on PATH is the Microsoft Store alias, which `where` finds and
REM which exits 9009 without running anything: taken at its word, that is a
REM stale-notices failure on a machine whose notices are fine.
python --version >nul 2>nul
if not errorlevel 1 (
    python scripts\gen-third-party-notices.py --check
    if errorlevel 1 (
        echo error: licenses\THIRD_PARTY_NOTICES.md is stale; a release must carry current notices 1>&2
        goto fail
    )
)

echo ==^> configuring
cmake -S . -B "%BUILD_DIR%" -DCMAKE_PREFIX_PATH="%QTPREFIX%" ^
      -DCMAKE_BUILD_TYPE=Release -DOMEGACAT_BUILD_TESTS=OFF ^
      -DOMEGACAT_VERSION="%VERSION%"
if errorlevel 1 goto fail

echo ==^> building
cmake --build "%BUILD_DIR%" --config Release --target omegacat
if errorlevel 1 goto fail

REM Multi-config generators (MSBuild, the default here) put it under Release\;
REM single-config ones (Ninja) do not.
set "APPDIR=%BUILD_DIR%\app\Release"
if not exist "%APPDIR%\omegacat.exe" set "APPDIR=%BUILD_DIR%\app"
if not exist "%APPDIR%\omegacat.exe" (
    echo error: no omegacat.exe after the build 1>&2
    echo     Looked in %BUILD_DIR%\app\Release\ and %BUILD_DIR%\app\
    goto fail
)

REM --- stage -----------------------------------------------------------------
echo ==^> staging into %STAGE%
if exist "%STAGE%" rmdir /s /q "%STAGE%"
mkdir "%STAGE%"
copy /y "%APPDIR%\omegacat.exe" "%STAGE%\omegacat.exe" >nul
if errorlevel 1 goto fail

REM --- the command-line tools ------------------------------------------------
REM Pure Go with CGO_ENABLED=0 -- no gcc and no DLLs -- so they run from this
REM folder with nothing installed. In the package because the package is for
REM somebody without a toolchain: ocvault is how a vault is created and
REM inspected, and capture is the whole engine without the window.
echo ==^> building the command-line tools
set "LDFLAGS=-s -w"
if not "%VERSION%"=="" set "LDFLAGS=-s -w -X github.com/scottpeterman/omegacatqt/internal/buildinfo.Version=%VERSION%"
set "CGO_ENABLED=0"
for %%T in (capture ocvault) do (
    go build -trimpath -ldflags "!LDFLAGS!" -o "%STAGE%\%%T.exe" "./cmd/%%T"
    if errorlevel 1 (
        echo error: go build ./cmd/%%T failed 1>&2
        set "CGO_ENABLED="
        goto fail
    )
)
set "CGO_ENABLED="

REM --- licensing -------------------------------------------------------------
REM NOT OPTIONAL. GPLv3 requires the licence text to accompany the binary; the
REM MIT, BSD and Apache components require their notices. licenses\ goes WHOLE:
REM the notices cite GPL-3.0.txt, LGPL-3.0.txt and Apache-2.0.txt "beside this
REM file".
echo ==^> staging the licences
if not exist "LICENSE" (
    echo error: LICENSE is missing from the repository. 1>&2
    goto fail
)
copy /y "LICENSE" "%STAGE%\" >nul
if errorlevel 1 goto fail
if not exist "licenses\THIRD_PARTY_NOTICES.md" (
    echo error: licenses\THIRD_PARTY_NOTICES.md is missing. 1>&2
    echo     Generate it with: python scripts\gen-third-party-notices.py
    goto fail
)
xcopy /e /i /q /y "licenses" "%STAGE%\licenses" >nul
if errorlevel 1 goto fail

REM --- windeployqt -----------------------------------------------------------
REM --no-translations: the application ships no translations of its own, and
REM without WebEngine there is no locale .pak for dropping them to break.
echo ==^> running windeployqt from %QTPREFIX%\bin
"%QTPREFIX%\bin\windeployqt.exe" --release --no-translations --no-system-d3d-compiler ^
    --compiler-runtime "%STAGE%\omegacat.exe"
if errorlevel 1 goto fail

REM --- the MSVC runtime ------------------------------------------------------
REM WITHOUT THIS THE PACKAGE DIES ON A CLEAN BOX and runs fine here, because
REM this machine has the VS 2022 redistributable installed. --compiler-runtime
REM above is asked for but not trusted: it may drop vc_redist.x64.exe into the
REM folder INSTEAD of the DLLs. So the DLLs are checked by name and copied.
set "CRTOK=1"
for %%F in (VCRUNTIME140.dll VCRUNTIME140_1.dll MSVCP140.dll) do (
    if not exist "%STAGE%\%%F" set "CRTOK=0"
)
if "!CRTOK!"=="0" (
    if "%VCToolsRedistDir%"=="" (
        echo error: MSVC runtime DLLs missing from %STAGE%, and 1>&2
        echo         %%VCToolsRedistDir%% is unset so they cannot be located.
        echo     Run this from an "x64 Native Tools Command Prompt for VS 2022".
        goto fail
    )
    call :findcrt
    if "!CRTDIR!"=="" (
        echo error: no Microsoft.VC*.CRT directory under 1>&2
        echo         !VCToolsRedistDir!x64
        echo     Add the v143 redistributable component in the VS Installer.
        goto fail
    )
    echo ==^> copying the MSVC runtime from !CRTDIR!
    copy /y "!CRTDIR!\VCRUNTIME140.dll"   "%STAGE%\" >nul
    copy /y "!CRTDIR!\VCRUNTIME140_1.dll" "%STAGE%\" >nul
    copy /y "!CRTDIR!\MSVCP140.dll"       "%STAGE%\" >nul
)
if exist "%STAGE%\vc_redist.x64.exe" del /q "%STAGE%\vc_redist.x64.exe"

REM --- verify ----------------------------------------------------------------
if not exist "%STAGE%\platforms\qwindows.dll" (
    echo error: platforms\qwindows.dll missing; the package would not start 1>&2
    goto fail
)
for %%F in (Qt6Core.dll Qt6Gui.dll Qt6Widgets.dll Qt6Svg.dll) do (
    if not exist "%STAGE%\%%F" (
        echo error: %%F missing; windeployqt did not do its job 1>&2
        goto fail
    )
)
REM The theme draws its checkboxes and dropdown arrows from SVG through this
REM image plugin. Missing, nothing fails: every checkbox is an empty square.
if not exist "%STAGE%\imageformats\qsvg.dll" (
    echo error: imageformats\qsvg.dll missing; every checkbox and dropdown arrow would draw blank 1>&2
    goto fail
)
for %%F in (capture.exe ocvault.exe) do (
    if not exist "%STAGE%\%%F" (
        echo error: %%F missing from the package 1>&2
        goto fail
    )
)
if not exist "%STAGE%\LICENSE" (
    echo error: LICENSE missing from the package; it may not be distributed 1>&2
    goto fail
)
for %%F in (THIRD_PARTY_NOTICES.md GPL-3.0.txt LGPL-3.0.txt Apache-2.0.txt) do (
    if not exist "%STAGE%\licenses\%%F" (
        echo error: licenses\%%F missing from the package 1>&2
        goto fail
    )
)
for %%F in (VCRUNTIME140.dll VCRUNTIME140_1.dll MSVCP140.dll) do (
    if not exist "%STAGE%\%%F" (
        echo error: %%F missing; the package dies at launch on a machine 1>&2
        echo        without the VS 2022 redistributable.
        goto fail
    )
)

REM The right files, not only present ones: a Qt6Core.dll from a different Qt
REM than the binary was linked against still runs here, where that Qt is on
REM PATH.
for /f "tokens=2 delims==" %%V in ('findstr /c:"Qt6_DIR:PATH=" "%BUILD_DIR%\CMakeCache.txt"') do set "USEDQT=%%V"
echo !USEDQT! | findstr /i /c:"%QTPREFIX:\=/%" >nul
if errorlevel 1 (
    echo error: the build and windeployqt used different Qt installations. 1>&2
    echo         linked against: !USEDQT!
    echo         deployed from:  %QTPREFIX%
    echo     Delete %BUILD_DIR% and run this again.
    goto fail
)

REM The stamp, asked of both tools; each gets it from its own -X flag.
REM omegacat.exe gets it through CMake and is a GUI-subsystem program with no
REM console to print --version to, so check that one by hand: Help > About.
if not "%VERSION%"=="" (
    call :checkversion capture
    if errorlevel 1 goto fail
    call :checkversion ocvault
    if errorlevel 1 goto fail
)
echo ==^> platform plugin, Qt DLLs, SVG plugin, tools, licences, MSVC runtime present, one Qt throughout

REM --- zip -------------------------------------------------------------------
if "%MAKEZIP%"=="1" (
    echo ==^> writing %ZIPNAME%
    if exist "%ZIPNAME%" del /q "%ZIPNAME%"
    powershell -NoProfile -Command "Compress-Archive -Path 'dist\omegacat' -DestinationPath '%ZIPNAME%'"
    if errorlevel 1 goto fail
)

echo.
echo the package contains omegacat.exe, capture.exe and ocvault.exe, Qt, the
echo MSVC runtime, LICENSE and licenses\.
echo.
echo run it with:
echo       %STAGE%\omegacat.exe
popd
exit /b 0

REM ===========================================================================
REM Subroutines. Everything below runs via CALL, outside any parenthesised
REM block, so a single caret escapes what it looks like it escapes.
REM ===========================================================================

REM :gitversion -- VERSION from git describe, or empty outside a checkout.
:gitversion
set "VERSION="
for /f "delims=" %%V in ('git describe --tags --always --dirty 2^>nul') do set "VERSION=%%V"
exit /b 0

REM :checkversion <tool> -- errorlevel 1 unless <tool>.exe -version names
REM %VERSION%. Here and not inline: the 2^>nul below would be inside a block.
:checkversion
set "GOTVERSION="
for /f "tokens=*" %%V in ('"%STAGE%\%~1.exe" -version 2^>nul') do set "GOTVERSION=%%V"
echo !GOTVERSION! | findstr /c:"%VERSION%" >nul
if errorlevel 1 (
    echo error: %~1.exe reports "!GOTVERSION!", expected %VERSION% 1>&2
    exit /b 1
)
exit /b 0

REM :findqt -- sets QTPREFIX to the newest C:\Qt\6.x with an msvc*_64 kit,
REM and QTGUESSED=1 if it found one.
:findqt
set "QTGUESSED=0"
if not exist "C:\Qt" exit /b 0
set "QTBESTNUM=0"
set "QTBEST="
for /f "delims=" %%D in ('dir /b /ad "C:\Qt\6.*" 2^>nul') do call :considerqt "%%D"
if "%QTBEST%"=="" exit /b 0
set "QTPREFIX=%QTBEST%"
set "QTGUESSED=1"
exit /b 0

REM :considerqt <version-dir-name> -- keep it if it is newer than QTBEST and
REM actually holds an MSVC kit. A 6.x with only a mingw kit is not a candidate:
REM link.exe cannot resolve a mingw Qt's C++ symbols.
:considerqt
set "QTV=%~1"
set "QTKIT="
for /f "delims=" %%E in ('dir /b /ad "C:\Qt\%~1\msvc*_64" 2^>nul') do if "!QTKIT!"=="" set "QTKIT=%%E"
if "%QTKIT%"=="" exit /b 0
if not exist "C:\Qt\%~1\%QTKIT%\bin\windeployqt.exe" exit /b 0

set "QTMAJ=" & set "QTMIN=" & set "QTPAT="
REM Parenthesised, because "do set A & set B" puts only the first SET in the
REM loop body -- the rest run once after it, where %%b is not a loop variable
REM and expands to the literal text %b.
for /f "tokens=1-3 delims=." %%a in ("%QTV%") do (
    set "QTMAJ=%%a"
    set "QTMIN=%%b"
    set "QTPAT=%%c"
)
call :decimal "!QTMAJ!" QTMAJ
call :decimal "!QTMIN!" QTMIN
call :decimal "!QTPAT!" QTPAT
set /a QTNUM=%QTMAJ%*1000000 + %QTMIN%*1000 + %QTPAT%
if %QTNUM% GTR %QTBESTNUM% (
    set "QTBESTNUM=%QTNUM%"
    set "QTBEST=C:\Qt\%QTV%\%QTKIT%"
)
exit /b 0

REM :decimal <text> <outvar> -- a number set/a will not misread. Empty becomes
REM 0, and leading zeros are stripped because set/a reads 08 as octal and
REM fails on it. Qt has not shipped a zero-padded component, but this costs
REM nothing and the failure it prevents is "invalid number" mid-loop.
:decimal
set "DEC=%~1"
if "%DEC%"=="" set "DEC=0"
:decimalstrip
if "%DEC%"=="0" goto decimaldone
if not "%DEC:~0,1%"=="0" goto decimaldone
set "DEC=%DEC:~1%"
if "%DEC%"=="" set "DEC=0"
goto decimalstrip
:decimaldone
set "%~2=%DEC%"
exit /b 0

REM :findcrt -- newest Microsoft.VC*.CRT under %VCToolsRedistDir%x64.
:findcrt
set "CRTDIR="
REM !CRTDIR! and not %CRTDIR%: /o-n lists newest first and this wants the
REM FIRST hit, but %CRTDIR% is expanded once when the FOR is parsed, so every
REM iteration would test the same empty string and the last -- oldest --
REM directory would win.
for /f "delims=" %%D in ('dir /b /ad /o-n "%VCToolsRedistDir%x64\Microsoft.VC*.CRT" 2^>nul') do (
    if "!CRTDIR!"=="" set "CRTDIR=%VCToolsRedistDir%x64\%%D"
)
exit /b 0

:usage
echo Builds the Windows release folder, and with --zip the zip.
echo.
echo   scripts\bundle-windows.bat [--qt ^<prefix^>] [--version ^<v^>] [--zip]
echo                              [--build-dir ^<dir^>]
echo.
echo Needs cl.exe ^(MSVC^), gcc.exe ^(mingw-w64 x86_64^) and go.exe on PATH.
echo Environment: OMEGACAT_QT, CMAKE_PREFIX_PATH, Qt6_DIR
popd
exit /b 0

:fail
echo.
echo bundle failed
popd
exit /b 1
