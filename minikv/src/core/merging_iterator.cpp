#include "core/merging_iterator.h"

namespace minikv {
namespace core {

MergingIterator::MergingIterator(std::vector<std::unique_ptr<Iterator>> children)
    : children_(std::move(children)), status_(Status::Ok()) {
    for (auto& c : children_) {
        if (c && !c->status().ok()) {
            status_ = c->status();
            break;
        }
    }
}

void MergingIterator::pushChild(size_t i) {
    if (i >= children_.size() || !children_[i] || !children_[i]->valid()) return;
    HeapItem item;
    item.child_index = i;
    item.key = children_[i]->key().toString();
    heap_.push(std::move(item));
}

void MergingIterator::rebuildHeap() {
    while (!heap_.empty()) heap_.pop();
    current_ = static_cast<size_t>(-1);
    for (size_t i = 0; i < children_.size(); ++i) pushChild(i);
    if (!heap_.empty()) current_ = heap_.top().child_index;
}

bool MergingIterator::valid() const {
    return status_.ok() && current_ < children_.size() && children_[current_] &&
           children_[current_]->valid();
}

void MergingIterator::seekToFirst() {
    for (auto& c : children_) {
        if (c) c->seekToFirst();
    }
    rebuildHeap();
}

void MergingIterator::seek(const Slice& target) {
    for (auto& c : children_) {
        if (c) c->seek(target);
    }
    rebuildHeap();
}

void MergingIterator::next() {
    if (!valid()) return;
    std::string cur = children_[current_]->key().toString();
    while (!heap_.empty() &&
           InternalKeyCompare(Slice(heap_.top().key), Slice(cur)) == 0) {
        size_t i = heap_.top().child_index;
        heap_.pop();
        children_[i]->next();
        pushChild(i);
    }
    current_ = heap_.empty() ? static_cast<size_t>(-1) : heap_.top().child_index;
}

Slice MergingIterator::key() const {
    if (!valid()) return Slice();
    return children_[current_]->key();
}

Slice MergingIterator::value() const {
    if (!valid()) return Slice();
    return children_[current_]->value();
}

Status MergingIterator::status() const {
    if (!status_.ok()) return status_;
    for (auto& c : children_) {
        if (c && !c->status().ok()) return c->status();
    }
    return Status::Ok();
}

}  // namespace core
}  // namespace minikv
