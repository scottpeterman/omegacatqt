# tests/CMakeLists.txt
#
# capi_probe drives the whole C surface -- vault, a live capture of fake
# devices, the store, search, the demo -- from plain C. fakedevice is the Go
# program it captures from: loopback only, no privileges, never shipped.
#
#   cmake --build build && ctest --test-dir build --output-on-failure

set(OMEGACAT_FAKEDEVICE "${CMAKE_CURRENT_BINARY_DIR}/fakedevice${CMAKE_EXECUTABLE_SUFFIX}")
file(GLOB OMEGACAT_FAKEDEVICE_SOURCES CONFIGURE_DEPENDS
    "${PROJECT_SOURCE_DIR}/tests/fakedevice/*.go"
    "${PROJECT_SOURCE_DIR}/internal/fakedev/*.go"
    "${PROJECT_SOURCE_DIR}/internal/sshcore/*.go")
add_custom_command(
    OUTPUT ${OMEGACAT_FAKEDEVICE}
    COMMAND ${CMAKE_COMMAND} -E env CGO_ENABLED=0
            ${GO_EXECUTABLE} build -trimpath -o ${OMEGACAT_FAKEDEVICE} ./tests/fakedevice
    WORKING_DIRECTORY ${PROJECT_SOURCE_DIR}
    DEPENDS ${OMEGACAT_FAKEDEVICE_SOURCES} "${PROJECT_SOURCE_DIR}/go.mod"
    COMMENT "Building the fake devices"
    VERBATIM)
add_custom_target(fakedevice ALL DEPENDS ${OMEGACAT_FAKEDEVICE})

add_executable(capi_probe capi_probe.c)
target_link_libraries(capi_probe PRIVATE omegacat::c)
if(NOT MSVC)
    target_compile_options(capi_probe PRIVATE -Wall -Wextra)
    target_compile_definitions(capi_probe PRIVATE _DEFAULT_SOURCE)
endif()
add_dependencies(capi_probe fakedevice)

# The exit status is the failure count.
# The template database is the shipped seed, from the source tree. The engine
# opens it read-only, so the probe never writes to it -- and never to the
# user's own copy in ~/.omegacat, which a default path would use.
add_test(NAME capi_probe COMMAND capi_probe ${OMEGACAT_FAKEDEVICE}
         "${PROJECT_SOURCE_DIR}/internal/tfsmfire/seed/tfsm_templates.db")

# app_probe drives the application window offscreen: the demo through the Run
# view, then a live capture of the fake devices, the Store view with a parsed
# ARP table, and a search. Built where the application is.
if(OMEGACAT_HAVE_APP)
    add_executable(app_probe app_probe.cpp)
    target_link_libraries(app_probe PRIVATE omegacat_ui)
    # For go.mod: the probe checks the notices compiled into the application
    # against the modules and versions it pins.
    target_compile_definitions(app_probe PRIVATE OMEGACAT_SOURCE_DIR="${PROJECT_SOURCE_DIR}")
    add_dependencies(app_probe fakedevice)
    add_test(NAME app_probe
             COMMAND app_probe "${CMAKE_CURRENT_BINARY_DIR}/app" ${OMEGACAT_FAKEDEVICE}
                     "${PROJECT_SOURCE_DIR}/internal/tfsmfire/seed/tfsm_templates.db")
    set_tests_properties(app_probe PROPERTIES ENVIRONMENT "QT_QPA_PLATFORM=offscreen")
endif()
