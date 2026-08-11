# 已合并

本目录原 GoogleTest 手撕题已合并到：

`practice/handwrite/`

请使用：
```bash
cmake -S practice -B practice/build && cmake --build practice/build -j
# 或单测目录
cmake -S practice/handwrite -B practice/handwrite/build && cmake --build practice/handwrite/build -j
ctest --test-dir practice/handwrite/build --output-on-failure
```
