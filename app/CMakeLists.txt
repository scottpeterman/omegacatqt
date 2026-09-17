# app/CMakeLists.txt
#
# The views as a library, so the application and app_probe link the same
# code; then the application. Qt 6.2 is the floor, as for omegamaps.

find_package(Qt6 6.2 REQUIRED COMPONENTS Widgets Svg)

set(CMAKE_AUTOMOC ON)
set(CMAKE_AUTORCC ON)

add_library(omegacat_ui STATIC
    aboutdialog.cpp  aboutdialog.h
    appsettings.cpp  appsettings.h
    settingsdialog.cpp settingsdialog.h
    filedialogs.cpp  filedialogs.h
    inventory.cpp    inventory.h
    inventorymanagerdialog.cpp inventorymanagerdialog.h
    mainwindow.cpp   mainwindow.h
    panel.cpp        panel.h
    runbridge.cpp    runbridge.h
    runtypes.cpp     runtypes.h
    runview.cpp      runview.h
    searchview.cpp   searchview.h
    store.cpp        store.h
    storeview.cpp    storeview.h
    templatebridge.cpp templatebridge.h
    templatelabview.cpp templatelabview.h
    theme.cpp        theme.h
    vault.cpp        vault.h
    credentialeditordialog.cpp  credentialeditordialog.h
    exporting.cpp    exporting.h
    credentialmanagerdialog.cpp credentialmanagerdialog.h
    currenttokens.cpp currenttokens.h
    dialogbuttons.cpp dialogbuttons.h
    modalframe.cpp   modalframe.h
    omegatheme.h
    vaultunlockdialog.cpp vaultunlockdialog.h
    windowbuttons.cpp windowbuttons.h
    widgets.cpp      widgets.h
    resources.qrc)
target_include_directories(omegacat_ui PUBLIC "${CMAKE_CURRENT_SOURCE_DIR}")
target_link_libraries(omegacat_ui PUBLIC Qt6::Widgets Qt6::Svg omegacat::c)

# The version as Windows and macOS want it: numbers only. "v0.3.0-4-gabc123"
# gives 0.3.0; anything without a leading number gives the project version.
string(REGEX MATCH "[0-9]+\\.[0-9]+(\\.[0-9]+)?" OMEGACAT_NUMERIC_VERSION "${OMEGACAT_VERSION}")
if(NOT OMEGACAT_NUMERIC_VERSION)
    set(OMEGACAT_NUMERIC_VERSION "${PROJECT_VERSION}")
endif()
if(NOT OMEGACAT_NUMERIC_VERSION MATCHES "\\.[0-9]+\\.[0-9]+$")
    set(OMEGACAT_NUMERIC_VERSION "${OMEGACAT_NUMERIC_VERSION}.0")
endif()

set(OMEGACAT_APP_SOURCES main.cpp)
if(WIN32)
    # The icon Explorer, the taskbar and Alt-Tab show, and the version details
    # on the file's Properties page.
    string(REPLACE "." "," OMEGACAT_RC_VERSION "${OMEGACAT_NUMERIC_VERSION},0")
    configure_file(appicon/omegacat.rc.in "${CMAKE_CURRENT_BINARY_DIR}/omegacat.rc" @ONLY)
    list(APPEND OMEGACAT_APP_SOURCES "${CMAKE_CURRENT_BINARY_DIR}/omegacat.rc")
elseif(APPLE)
    set(OMEGACAT_ICNS "${CMAKE_CURRENT_SOURCE_DIR}/appicon/omegacat.icns")
    set_source_files_properties(${OMEGACAT_ICNS} PROPERTIES MACOSX_PACKAGE_LOCATION Resources)
    list(APPEND OMEGACAT_APP_SOURCES ${OMEGACAT_ICNS})
endif()

add_executable(omegacat WIN32 MACOSX_BUNDLE ${OMEGACAT_APP_SOURCES})
target_link_libraries(omegacat PRIVATE omegacat_ui)
if(APPLE)
    set_target_properties(omegacat PROPERTIES
        MACOSX_BUNDLE_BUNDLE_NAME "OmegaCat"
        MACOSX_BUNDLE_GUI_IDENTIFIER "io.github.scottpeterman.omegacat"
        MACOSX_BUNDLE_ICON_FILE "omegacat.icns"
        MACOSX_BUNDLE_BUNDLE_VERSION "${OMEGACAT_NUMERIC_VERSION}"
        MACOSX_BUNDLE_SHORT_VERSION_STRING "${OMEGACAT_NUMERIC_VERSION}"
        MACOSX_BUNDLE_COPYRIGHT "Copyright (C) 2026 Scott Peterman. GPL-3.0.")
endif()
