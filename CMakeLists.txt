# CMakeLists.txt
#
# The C surface and, from step 3, the Qt application on it. The command-line
# tools are not built here -- they are pure Go and scripts/build.sh builds them
# without cgo. This build is the other half: the capture engine as a Go
# c-archive, and what links against it.
#
# The Go archive is a custom command, so `cmake --build` is the only entry
# point. Every environment variable the Go toolchain reads (GOFLAGS, GOPROXY,
# GOTOOLCHAIN, ...) comes from the shell that runs the build, not from the
# configure step.

cmake_minimum_required(VERSION 3.19)
project(omegacat VERSION 0.1.0 LANGUAGES C CXX)

set(CMAKE_C_STANDARD 11)
set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_POSITION_INDEPENDENT_CODE ON)

option(OMEGACAT_BUILD_TESTS "Build the C probe and the fake devices it captures from" ON)
option(OMEGACAT_BUILD_APP "Build the Qt application (needs Qt 6.2+ Widgets and Svg)" ON)

# The version the application reports: About, Copy details, omegacat_version().
# The bundle scripts pass git describe; a plain build leaves it empty and the
# library falls back to its VCS stamp, or "dev". The command-line tools get the
# same value from scripts/build.sh and the bundle scripts, through the same
# -X flag.
set(OMEGACAT_VERSION "" CACHE STRING "Version stamped into the application, e.g. v0.3.0")

# A file whose CONTENT is the version, as a dependency of the archive. The
# Makefile generators do not rerun a custom command because its command line
# changed, so without this a reconfigure with a new version rebuilds nothing
# and the bundle reports the old one. configure_file rewrites the file only
# when the version differs, so an unchanged version rebuilds nothing either.
file(WRITE "${CMAKE_CURRENT_BINARY_DIR}/omegacat_version.in" "@OMEGACAT_VERSION@\n")
configure_file("${CMAKE_CURRENT_BINARY_DIR}/omegacat_version.in"
               "${CMAKE_CURRENT_BINARY_DIR}/omegacat_version.txt" @ONLY)
set(OMEGACAT_GO_LDFLAGS "")
if(NOT OMEGACAT_VERSION STREQUAL "")
    set(OMEGACAT_GO_LDFLAGS
        "-ldflags=-X github.com/scottpeterman/omegacatqt/internal/buildinfo.Version=${OMEGACAT_VERSION}")
endif()

# The Go on PATH, not one under CMAKE_PREFIX_PATH. The build scripts point
# that at a Qt prefix (/usr, /opt/homebrew), and find_program searches it
# first: a distribution's or Homebrew's older Go then builds the archive
# instead of the toolchain the shell selected. The scripts also pass
# -DGO_EXECUTABLE explicitly.
find_program(GO_EXECUTABLE go REQUIRED NO_CMAKE_PATH NO_CMAKE_ENVIRONMENT_PATH)

# The generated header's name follows -o, not the platform.
if(WIN32)
    set(OMEGACAT_ARCHIVE "${CMAKE_CURRENT_BINARY_DIR}/omegacat.lib")
    set(OMEGACAT_GENERATED_HEADER "${CMAKE_CURRENT_BINARY_DIR}/omegacat.h")
else()
    set(OMEGACAT_ARCHIVE "${CMAKE_CURRENT_BINARY_DIR}/libomegacat.a")
    set(OMEGACAT_GENERATED_HEADER "${CMAKE_CURRENT_BINARY_DIR}/libomegacat.h")
endif()

# Windows: mingw-w64 compiles the archive, MSVC links it. Carried over from
# omegamaps / omegassh unchanged -- properties of a Go c-archive meeting
# link.exe, not of any one project.
#   -D__USE_MINGW_ANSI_STDIO=0  cgo's printf calls resolve against the UCRT
#   -gdwarf-4                   gcc 12+ DWARF 5 is implicated in images the
#                               loader refuses with 0xc000007b
# CGO_CFLAGS is replaced, not extended, so -O2 -g are restated.
if(WIN32)
    set(OMEGACAT_GO_ENV CGO_ENABLED=1
        "CGO_CFLAGS=-O2 -g -gdwarf-4 -D__USE_MINGW_ANSI_STDIO=0")
else()
    set(OMEGACAT_GO_ENV CGO_ENABLED=1)
endif()

# MSVC rejects go.o when its .pdata records are out of address order (LNK1223).
# scripts/pdatafix sorts them in place; a no-op on a sorted archive.
if(MSVC)
    set(OMEGACAT_PDATA_FIX
        COMMAND ${GO_EXECUTABLE} run ./scripts/pdatafix ${OMEGACAT_ARCHIVE})
else()
    set(OMEGACAT_PDATA_FIX "")
endif()

# Every Go file under capi/ and internal/: the archive links most of the
# module, and a dependency missed here is a stale archive that builds cleanly
# and runs old code.
file(GLOB_RECURSE OMEGACAT_GO_SOURCES CONFIGURE_DEPENDS
    "${CMAKE_CURRENT_SOURCE_DIR}/capi/*.go"
    "${CMAKE_CURRENT_SOURCE_DIR}/internal/*.go"
    # The template database is go:embed'ed into the archive.
    "${CMAKE_CURRENT_SOURCE_DIR}/internal/*.db")
list(APPEND OMEGACAT_GO_SOURCES
    "${CMAKE_CURRENT_SOURCE_DIR}/go.mod"
    "${CMAKE_CURRENT_SOURCE_DIR}/go.sum"
    "${CMAKE_CURRENT_BINARY_DIR}/omegacat_version.txt")

add_custom_command(
    OUTPUT ${OMEGACAT_ARCHIVE} ${OMEGACAT_GENERATED_HEADER}
    COMMAND ${CMAKE_COMMAND} -E env ${OMEGACAT_GO_ENV}
            ${GO_EXECUTABLE} build -buildmode=c-archive -trimpath ${OMEGACAT_GO_LDFLAGS}
            -o ${OMEGACAT_ARCHIVE} ./capi
    ${OMEGACAT_PDATA_FIX}
    WORKING_DIRECTORY ${CMAKE_CURRENT_SOURCE_DIR}
    DEPENDS ${OMEGACAT_GO_SOURCES}
    COMMENT "Building Go c-archive (libomegacat)"
    VERBATIM)

add_custom_target(omegacat_archive DEPENDS ${OMEGACAT_ARCHIVE})

# The C surface. Consumers include <omegacat/omegacat.h>; the cgo header in the
# build directory is an artifact, not the contract.
add_library(omegacat_c INTERFACE)
add_dependencies(omegacat_c omegacat_archive)
target_include_directories(omegacat_c INTERFACE
    "${CMAKE_CURRENT_SOURCE_DIR}/include")
target_link_libraries(omegacat_c INTERFACE ${OMEGACAT_ARCHIVE})

if(UNIX AND NOT APPLE)
    target_link_libraries(omegacat_c INTERFACE pthread)
elseif(APPLE)
    # crypto/x509, reached through x/crypto/ssh, imports symbols straight from
    # Security.framework on darwin. Go's own link resolves those for a Go
    # binary; here CMake does the final link, so they have to be on its line.
    target_link_libraries(omegacat_c INTERFACE
        "-framework CoreFoundation" "-framework Security")
elseif(WIN32)
    target_link_libraries(omegacat_c INTERFACE ws2_32 winmm ntdll bcrypt)
    if(MSVC)
        # The UCRT inlines fprintf and friends; cgo's mingw-built objects call
        # them out of line.
        target_link_libraries(omegacat_c INTERFACE legacy_stdio_definitions)

        # c-archive starts Go's runtime from .ctors, which link.exe ignores:
        # without this the first call into Go blocks forever (golang/go#42190).
        # msvc/runtime_init.c calls the same initializer through .CRT$XCU, and
        # /INCLUDE: keeps its object from being dropped.
        add_library(omegacat_msvc_init STATIC msvc/runtime_init.c)
        add_dependencies(omegacat_msvc_init omegacat_archive)
        target_link_libraries(omegacat_c INTERFACE omegacat_msvc_init)
        target_link_options(omegacat_c INTERFACE
            "/INCLUDE:omegacat_go_runtime_init")
    endif()
endif()

add_library(omegacat::c ALIAS omegacat_c)

# The application is built when Qt is there to build it. A machine with Go and
# a C compiler and no Qt still builds and tests the C surface; it is told, not
# failed.
set(OMEGACAT_HAVE_APP OFF)
if(OMEGACAT_BUILD_APP)
    find_package(Qt6 6.2 QUIET COMPONENTS Widgets Svg)
    if(Qt6Widgets_FOUND AND Qt6Svg_FOUND)
        add_subdirectory(app)
        set(OMEGACAT_HAVE_APP ON)
    else()
        message(WARNING "OmegaCat application not built: Qt 6.2+ Widgets and Svg were not found. "
                        "Set CMAKE_PREFIX_PATH to a Qt 6 install, or -DOMEGACAT_BUILD_APP=OFF.")
    endif()
endif()

if(OMEGACAT_BUILD_TESTS)
    enable_testing()
    add_subdirectory(tests)
endif()
