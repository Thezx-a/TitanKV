cmake_minimum_required(VERSION 3.20)

# 独立构建 practice 时才声明 project；作为 hellocpp 子目录时复用父项目
if(CMAKE_SOURCE_DIR STREQUAL CMAKE_CURRENT_SOURCE_DIR)
    project(titankv_practice LANGUAGES CXX)
    set(CMAKE_CXX_STANDARD 17)
    set(CMAKE_CXX_STANDARD_REQUIRED ON)
    set(CMAKE_CXX_EXTENSIONS OFF)
    set(CMAKE_EXPORT_COMPILE_COMMANDS ON)
    enable_testing()
endif()

# Module 00-14 教学练习（每章可单独 cmake -S moduleXX）
add_subdirectory(module00_env_check)
add_subdirectory(module01_overview)
add_subdirectory(module02_cpp_core)
add_subdirectory(module03_modern_cpp)
add_subdirectory(module04_go_ts_concepts)
add_subdirectory(module05_skiplist)
add_subdirectory(module06_bloom_hash)
add_subdirectory(module07_lsm_engine)
add_subdirectory(module08_compaction_mvcc)
add_subdirectory(module09_epoll_coro)
add_subdirectory(module10_http_proxy)
add_subdirectory(module11_raft_sharding)
add_subdirectory(module12_microservices_concepts)
add_subdirectory(module13_interview)
add_subdirectory(module14_reproduce)

# 原 tests/course 手撕题（GoogleTest）
if(EXISTS "${CMAKE_CURRENT_SOURCE_DIR}/handwrite/CMakeLists.txt")
    add_subdirectory(handwrite)
endif()
