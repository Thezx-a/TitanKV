cmake_minimum_required(VERSION 3.16)
project(MiniKV VERSION 0.1.0 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

if(NOT CMAKE_BUILD_TYPE)
    set(CMAKE_BUILD_TYPE Release)
endif()

option(ENABLE_TESTS "Enable unit tests" OFF)
option(ENABLE_BENCHMARKS "Enable benchmarks" OFF)
option(ENABLE_SANITIZERS "Enable AddressSanitizer and ThreadSanitizer" OFF)

include(${CMAKE_CURRENT_SOURCE_DIR}/cmake/CompilerWarnings.cmake)
include(${CMAKE_CURRENT_SOURCE_DIR}/cmake/Sanitizers.cmake)

# Compression dependencies (Snappy + Zstd). Pulled via FetchContent.
# These are required for SSTable block compression in WP 1.2.1.
if(NOT TARGET snappy)
    include(${CMAKE_CURRENT_SOURCE_DIR}/cmake/FetchCompression.cmake)
endif()

add_library(minikv STATIC
    src/core/wal.cpp
    src/core/memtable.cpp
    src/core/sstable_builder.cpp
    src/core/sstable_reader.cpp
    src/core/block.cpp
    src/core/compaction.cpp
    src/core/version.cpp
    src/core/db_impl.cpp
    src/core/status.cpp
    src/core/memtable_iterator.cpp
    src/core/sstable_iterator.cpp
    src/core/merging_iterator.cpp
    src/core/compression.cpp
    src/core/manifest.cpp
    src/core/internal_key.cpp
    src/network/event_loop.cpp
    src/network/event_loop_thread.cpp
    src/network/server.cpp
    src/network/connection.cpp
    src/utils/crc32.cpp
    src/utils/env_posix.cpp
)

target_include_directories(minikv PUBLIC
    ${CMAKE_CURRENT_SOURCE_DIR}/include
    ${CMAKE_CURRENT_SOURCE_DIR}/src
)

target_compile_features(minikv PUBLIC cxx_std_17)
find_package(Threads REQUIRED)
target_link_libraries(minikv PUBLIC Threads::Threads)
apply_compiler_warnings(minikv)
apply_sanitizers(minikv)

# Link compression libraries. Snappy exports a CMake target named `snappy`;
# Zstd's CMake build defines `libzstd_static` (or `libzstd_shared`).
target_link_libraries(minikv PUBLIC snappy)

if(TARGET libzstd_static)
    target_link_libraries(minikv PUBLIC libzstd_static)
elseif(TARGET libzstd_shared)
    target_link_libraries(minikv PUBLIC libzstd_shared)
elseif(TARGET zstd::libzstd_static)
    target_link_libraries(minikv PUBLIC zstd::libzstd_static)
else()
    # Fallback: try pkg-config / system library.
    find_package(PkgConfig QUIET)
    if(PkgConfig_FOUND)
        pkg_check_modules(ZSTD IMPORTED_TARGET libzstd)
        if(ZSTD_FOUND)
            target_link_libraries(minikv PUBLIC PkgConfig::ZSTD)
        endif()
    endif()
endif()

# Build server/tests when this is the top-level project, OR when the monorepo
# root (TitanKV) enables them via ENABLE_TESTS / explicit options.
option(MINIKV_BUILD_SERVER "Build minikv_server executable" ON)
option(MINIKV_BUILD_TESTS "Build MiniKV unit tests" ${ENABLE_TESTS})

if(MINIKV_BUILD_SERVER AND EXISTS "${CMAKE_CURRENT_SOURCE_DIR}/src/main.cpp")
    add_executable(minikv_server src/main.cpp)
    target_link_libraries(minikv_server PRIVATE minikv)
endif()

if(MINIKV_BUILD_TESTS)
    if(NOT TARGET GTest::gtest_main)
        include(${CMAKE_CURRENT_SOURCE_DIR}/cmake/FetchGoogleTest.cmake)
    endif()
    enable_testing()
    add_subdirectory(tests)
endif()

if(ENABLE_BENCHMARKS AND CMAKE_SOURCE_DIR STREQUAL CMAKE_CURRENT_SOURCE_DIR)
    add_subdirectory(benches)
endif()