include(FetchContent)

# -----------------------------------------------------------------------
# Snappy — fast block compressor (used by SSTable data blocks by default).
# -----------------------------------------------------------------------
FetchContent_Declare(
    snappy
    GIT_REPOSITORY https://github.com/google/snappy.git
    GIT_TAG        1.2.1
    GIT_SHALLOW    TRUE
    GIT_SUBMODULES ""   # skip nested googletest/benchmark (TLS-fragile on WSL)
)
# Avoid building snappy's own tests/benchmarks; just the library.
set(SNAPPY_BUILD_TESTS  OFF CACHE BOOL "" FORCE)
set(SNAPPY_BUILD_BENCHMARKS OFF CACHE BOOL "" FORCE)
FetchContent_MakeAvailable(snappy)

# -----------------------------------------------------------------------
# Zstd — high-ratio block compressor (used for archival / cold SSTables).
# -----------------------------------------------------------------------
FetchContent_Declare(
    zstd
    GIT_REPOSITORY https://github.com/facebook/zstd.git
    GIT_TAG        v1.5.6
    GIT_SHALLOW    TRUE
    SOURCE_SUBDIR  build/cmake
)
set(ZSTD_BUILD_PROGRAMS OFF CACHE BOOL "" FORCE)
set(ZSTD_BUILD_TESTS     OFF CACHE BOOL "" FORCE)
set(ZSTD_BUILD_CONTRIB   OFF CACHE BOOL "" FORCE)
set(ZSTD_MULTITHREAD_SUPPORT OFF CACHE BOOL "" FORCE)
FetchContent_MakeAvailable(zstd)