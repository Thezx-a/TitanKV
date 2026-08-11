/**
 * ============================================================================
 * Module 00 · 跨平台环境搭建 —— 工具链与 C++ 标准自检
 * ============================================================================
 *
 * 学习目标：
 *   1. 确认本机 g++ / clang / MSVC 能编译 C++17
 *   2. 认识常用预定义宏（__cplusplus、编译器版本）
 *   3. 养成「先验证环境再写业务」的习惯
 *
 * 编译运行（单独本章）：
 *   cd /root/hellocpp/practice/module00_env_check
 *   cmake -B build && cmake --build build -j
 *   ./build/module00_env_check
 *
 * 本文件为独立练习，不依赖 hellocpp 源码。
 */

#include <array>
#include <cstdint>
#include <iostream>
#include <string>
#include <vector>

// ---------------------------------------------------------------------------
// 辅助：把 __cplusplus 宏翻译成人类可读的标准版本
// ---------------------------------------------------------------------------
static const char* cpp_standard_name(long v) {
    // 注意：不同编译器对 __cplusplus 的取值略有差异
    // GCC/Clang 在 -std=c++17 时通常是 201703L
    if (v >= 202002L) return "C++20 或更高";
    if (v >= 201703L) return "C++17";
    if (v >= 201402L) return "C++14";
    if (v >= 201103L) return "C++11";
    return "C++98 / 未知";
}

// ---------------------------------------------------------------------------
// 辅助：探测当前是哪家编译器
// ---------------------------------------------------------------------------
static std::string detect_compiler() {
#if defined(__clang__)
    return "Clang " + std::to_string(__clang_major__) + "." +
           std::to_string(__clang_minor__) + "." +
           std::to_string(__clang_patchlevel__);
#elif defined(__GNUC__)
    return "GCC " + std::to_string(__GNUC__) + "." +
           std::to_string(__GNUC_MINOR__) + "." +
           std::to_string(__GNUC_PATCHLEVEL__);
#elif defined(_MSC_VER)
    return "MSVC _MSC_VER=" + std::to_string(_MSC_VER);
#else
    return "未知编译器";
#endif
}

int main() {
    std::cout << "========================================\n";
    std::cout << " Module 00 · 环境自检\n";
    std::cout << "========================================\n";

    // 1) 打印编译器信息
    std::cout << "[编译器] " << detect_compiler() << "\n";

    // 2) 打印 C++ 标准宏
    //    若这里显示低于 C++17，请检查 CMakeLists.txt 的 CMAKE_CXX_STANDARD
    std::cout << "[标准]   __cplusplus = " << __cplusplus
              << "  =>  " << cpp_standard_name(__cplusplus) << "\n";

    // 3) 做一个最基本的 C++17 特性冒烟：结构化绑定 + 初始化列表
    //    若编译失败，说明工具链未正确开启 C++17
    std::vector<int> nums{1, 2, 3};
    auto [a, b, c] = std::array<int, 3>{nums[0], nums[1], nums[2]};
    std::cout << "[C++17]  结构化绑定结果: " << a << ", " << b << ", " << c << "\n";

    // 4) 字节序粗测（存储引擎里 Endian 很常见）
    const uint16_t probe = 0x0102;
    const auto* bytes = reinterpret_cast<const unsigned char*>(&probe);
    const char* endian = (bytes[0] == 0x02) ? "小端 Little-Endian（x86/常见）"
                                            : "大端 Big-Endian";
    std::cout << "[字节序] " << endian << "\n";

    std::cout << "----------------------------------------\n";
    std::cout << "环境检查通过 ✔  可以开始 Module 01\n";
    return 0;
}
