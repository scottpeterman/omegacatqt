// app/main.cpp
#include <QApplication>
#include <QCommandLineParser>
#include <QIcon>
#include <QStyleFactory>

#include "appsettings.h"
#include "mainwindow.h"
#include "runbridge.h"
#include "theme.h"

using namespace omegacat;

int main(int argc, char **argv) {
    QApplication app(argc, argv);
    QApplication::setOrganizationName(QStringLiteral("omegacat"));
    QApplication::setApplicationName(QStringLiteral("omegacat"));
    QApplication::setApplicationVersion(RunBridge::libraryVersion());
    QApplication::setStyle(QStyleFactory::create(QStringLiteral("Fusion")));
    // The window and taskbar icon on Linux and Windows (macOS takes the
    // bundle's .icns), and the .desktop file a Wayland compositor matches the
    // window to -- the AppImage installs omegacat.desktop.
    initResources();  // the icon is in omegacat_ui's resources, a static library
    QApplication::setWindowIcon(QIcon(QStringLiteral(":/omegacat/appicon.png")));
    QGuiApplication::setDesktopFileName(QStringLiteral("omegacat"));
    if (qEnvironmentVariableIntValue("OMEGACAT_QT_DIALOGS") != 0)
        QCoreApplication::setAttribute(Qt::AA_DontUseNativeDialogs);

    QCommandLineParser cli;
    cli.setApplicationDescription(QStringLiteral("OmegaCat: network device capture, store and search"));
    cli.addHelpOption();
    cli.addVersionOption();
    const QCommandLineOption demoOpt(QStringLiteral("demo"),
        QStringLiteral("Play the scripted demo run on start; value is ms between events"),
        QStringLiteral("ms"));
    const QCommandLineOption storeOpt(QStringLiteral("store"), QStringLiteral("Capture store to open"),
                                      QStringLiteral("path"));
    const QCommandLineOption themeOpt(QStringLiteral("theme"),
        QStringLiteral("light, dark or cyber (default: the last one used)"), QStringLiteral("name"));
    cli.addOptions({demoOpt, storeOpt, themeOpt});
    cli.process(app);

    const QString theme = cli.isSet(themeOpt)
                              ? cli.value(themeOpt)
                              : appsettings::theme();
    ThemeManager::instance().setTheme(themeFromKey(theme));

    MainWindow w;
    if (cli.isSet(storeOpt)) w.setStorePath(cli.value(storeOpt));
    w.show();
    w.tryQuietUnlock();
    if (cli.isSet(demoOpt)) w.openDemo(cli.value(demoOpt).toInt());
    return app.exec();
}
