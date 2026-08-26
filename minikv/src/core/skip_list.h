#pragma once
#include <atomic>
#include <mutex>
#include <optional>
#include <random>
#include <shared_mutex>
#include <string>
#include <vector>
#include "core/internal_key.h"
#include "minikv/slice.h"

namespace minikv {
namespace core {

struct SkipNode {
    std::string key;
    std::string value;
    std::vector<SkipNode*> forward;
    SkipNode(std::string k, std::string v, int level)
        : key(std::move(k)), value(std::move(v)), forward(level + 1, nullptr) {}
};

class SkipList {
public:
    static constexpr int kMaxLevel = 32;

    SkipList() : head_(new SkipNode("", "", kMaxLevel)), max_level_(0), mem_usage_(0) {}

    ~SkipList() {
        SkipNode* node = head_;
        while (node) {
            SkipNode* next = node->forward[0];
            delete node;
            node = next;
        }
    }

    void put(const std::string& key, const std::string& value) {
        std::unique_lock<std::shared_mutex> lock(mutex_);
        std::vector<SkipNode*> update(kMaxLevel + 1, nullptr);
        SkipNode* x = head_;
        for (int i = max_level_; i >= 0; --i) {
            while (x->forward[i] &&
                   InternalKeyCompare(Slice(x->forward[i]->key), Slice(key)) < 0)
                x = x->forward[i];
            update[i] = x;
        }
        x = x->forward[0];
        if (x && x->key == key) {
            mem_usage_ -= x->value.size();
            x->value = value;
            mem_usage_ += value.size();
        } else {
            int level = randomLevel();
            if (level > max_level_) {
                for (int i = max_level_ + 1; i <= level; ++i) update[i] = head_;
                max_level_ = level;
            }
            auto* node = new SkipNode(key, value, level);
            for (int i = 0; i <= level; ++i) {
                node->forward[i] = update[i]->forward[i];
                update[i]->forward[i] = node;
            }
            mem_usage_ += sizeof(SkipNode) + key.size() + value.size();
        }
    }

    std::optional<std::string> get(const std::string& key) const {
        std::shared_lock<std::shared_mutex> lock(mutex_);
        SkipNode* x = head_;
        for (int i = max_level_; i >= 0; --i) {
            while (x->forward[i] &&
                   InternalKeyCompare(Slice(x->forward[i]->key), Slice(key)) < 0)
                x = x->forward[i];
        }
        x = x->forward[0];
        if (x && x->key == key) return x->value;
        return std::nullopt;
    }

    // First node with InternalKeyCompare(node->key, target) >= 0, or nullptr.
    // O(log n). Caller must not retain the pointer across a write unlock.
    SkipNode* findGreaterOrEqual(const Slice& target) const {
        std::shared_lock<std::shared_mutex> lock(mutex_);
        return findGreaterOrEqualUnlocked(target);
    }

    void del(const std::string& key) {
        std::unique_lock<std::shared_mutex> lock(mutex_);
        std::vector<SkipNode*> update(kMaxLevel + 1, nullptr);
        SkipNode* x = head_;
        for (int i = max_level_; i >= 0; --i) {
            while (x->forward[i] &&
                   InternalKeyCompare(Slice(x->forward[i]->key), Slice(key)) < 0)
                x = x->forward[i];
            update[i] = x;
        }
        x = x->forward[0];
        if (x && x->key == key) {
            for (int i = 0; i <= max_level_; ++i) {
                if (update[i]->forward[i] != x) break;
                update[i]->forward[i] = x->forward[i];
            }
            mem_usage_ -= sizeof(SkipNode) + x->key.size() + x->value.size();
            delete x;
            while (max_level_ > 0 && head_->forward[max_level_] == nullptr) --max_level_;
        }
    }

    // Full copy — kept for flush / dump. Point lookup and iterators must not
    // use this on the hot path (M9).
    std::vector<std::pair<std::string, std::string>> entries() const {
        std::shared_lock<std::shared_mutex> lock(mutex_);
        std::vector<std::pair<std::string, std::string>> result;
        SkipNode* x = head_->forward[0];
        while (x) {
            result.push_back({x->key, x->value});
            x = x->forward[0];
        }
        return result;
    }

    size_t approximateMemoryUsage() const {
        std::shared_lock<std::shared_mutex> lock(mutex_);
        return mem_usage_;
    }

    bool empty() const {
        std::shared_lock<std::shared_mutex> lock(mutex_);
        return head_->forward[0] == nullptr;
    }

    // Lazy cursor over the list. Each seek/next takes a shared lock briefly.
    class Iterator {
    public:
        explicit Iterator(const SkipList* list) : list_(list) {}

        void seekToFirst() {
            std::shared_lock<std::shared_mutex> lock(list_->mutex_);
            node_ = list_->head_->forward[0];
        }

        void seek(const Slice& target) {
            std::shared_lock<std::shared_mutex> lock(list_->mutex_);
            node_ = list_->findGreaterOrEqualUnlocked(target);
        }

        void next() {
            std::shared_lock<std::shared_mutex> lock(list_->mutex_);
            if (node_) node_ = node_->forward[0];
        }

        bool valid() const { return node_ != nullptr; }

        Slice key() const {
            if (!node_) return Slice();
            return Slice(node_->key);
        }

        Slice value() const {
            if (!node_) return Slice();
            return Slice(node_->value);
        }

    private:
        const SkipList* list_;
        SkipNode* node_ = nullptr;
    };

private:
    friend class Iterator;

    SkipNode* findGreaterOrEqualUnlocked(const Slice& target) const {
        SkipNode* x = head_;
        for (int i = max_level_; i >= 0; --i) {
            while (x->forward[i] &&
                   InternalKeyCompare(Slice(x->forward[i]->key), target) < 0)
                x = x->forward[i];
        }
        return x->forward[0];
    }

    int randomLevel() {
        static thread_local std::mt19937 rng(std::random_device{}());
        static thread_local std::uniform_int_distribution<int> dist(0, 1);
        int level = 0;
        while (dist(rng) && level < kMaxLevel) ++level;
        return level;
    }

    mutable std::shared_mutex mutex_;
    SkipNode* head_;
    int max_level_;
    size_t mem_usage_;
};

}  // namespace core
}  // namespace minikv
