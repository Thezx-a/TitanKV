# 原 tests/course 手撕 GoogleTest，已合并到 practice/handwrite
# 仍可独立构建：cmake -S practice/handwrite -B practice/handwrite/build
#
# 每个测试文件都是独立的、不依赖 minikv 源码的"手撕"实现。
# GoogleTest 通过 FetchContent 拉取（若上层未提供 GTest::gtest_main target）。
# 参考: minikv/cmake/FetchGoogleTest.cmake

cmake_minimum_required(VERSION 3.20)
if(CMAKE_SOURCE_DIR STREQUAL CMAKE_CURRENT_SOURCE_DIR)
    project(titankv_practice_handwrite LANGUAGES CXX)
    set(CMAKE_CXX_STANDARD 17)
    set(CMAKE_CXX_STANDARD_REQUIRED ON)
    enable_testing()
endif()

# 若上层（minikv 作为顶层项目时）已拉取 GTest，则直接复用。
if(NOT TARGET GTest::gtest_main)
    include(FetchContent)
    FetchContent_Declare(
        googletest
        GIT_REPOSITORY https://github.com/google/googletest.git
        GIT_TAG        v1.14.0
    )
    # Windows 下禁用 gtest 自动初始化的子工程，避免覆盖父项目的编译选项。
    set(gtest_force_shared_crt ON CACHE BOOL "" FORCE)
    FetchContent_MakeAvailable(googletest)
endif()

# 线程池 / SPSC 队列测试需要线程库。
find_package(Threads REQUIRED)

# 辅助函数：声明一个 course 测试目标并注册到 CTest。
function(add_course_test target_name source_file ctest_name)
    add_executable(${target_name} ${source_file})
    target_link_libraries(${target_name} PRIVATE GTest::gtest_main Threads::Threads)
    target_compile_features(${target_name} PRIVATE cxx_std_17)
    # GCC 12+ 上启用更严格的告警，但测试代码不应当作 -Werror 阻断构建。
    if(CMAKE_CXX_COMPILER_ID STREQUAL "GNU" OR CMAKE_CXX_COMPILER_ID MATCHES "Clang")
        target_compile_options(${target_name} PRIVATE -Wall -Wextra -Wno-unused-parameter)
    endif()
    # MSVC 默认按系统代码页解析源文件；中文注释为 UTF-8，需显式指定以避免误解析。
    if(MSVC)
        target_compile_options(${target_name} PRIVATE /utf-8 /permissive-)
    endif()
    add_test(NAME ${ctest_name} COMMAND ${target_name})
endfunction()

add_course_test(test_skiplist_handwrite   test_skiplist_handwrite.cpp   course_skiplist)
add_course_test(test_lru_handwrite        test_lru_handwrite.cpp        course_lru)
add_course_test(test_thread_pool_handwrite test_thread_pool_handwrite.cpp course_thread_pool)
add_course_test(test_unique_ptr_handwrite test_unique_ptr_handwrite.cpp course_unique_ptr)
add_course_test(test_spsc_queue_handwrite test_spsc_queue_handwrite.cpp course_spsc_queue)
add_course_test(test_bloom_filter_math    test_bloom_filter_math.cpp    course_bloom_filter_math)
add_course_test(test_murmurhash            test_murmurhash.cpp           course_murmurhash)
