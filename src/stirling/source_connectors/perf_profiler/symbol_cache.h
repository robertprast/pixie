/*
 * Copyright 2018- The Pixie Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

#pragma once

#include <functional>
#include <string>
#include <utility>

#include <absl/container/flat_hash_map.h>

#include "src/stirling/source_connectors/perf_profiler/types.h"

namespace px {
namespace stirling {

class SymbolCache {
 public:
  explicit SymbolCache(SymbolizerFn symbolizer_fn) : symbolizer_fn_(symbolizer_fn) {}

  struct LookupResult {
    std::string_view symbol;
    bool hit;
  };

  LookupResult Lookup(const uintptr_t addr);

  size_t active_entries() { return cache_.size(); }
  size_t total_entries() { return cache_.size() + prev_cache_.size(); }

  void CreateNewGeneration() {
    prev_cache_ = std::move(cache_);
    cache_.clear();
  }

 private:
  /**
   * Symbol wraps a std::string for the sole and express purpose of
   * enabling <some map>::try_emplace() to *not* call a potentially expensive symbolizer_fn
   * if the key already exists in that map. If we provide the symbol directly to try_emplace,
   * then get_addr_symbol() is called (costing extra work).
   */
  class Symbol {
   public:
    Symbol(SymbolizerFn symbolizer_fn, const uintptr_t addr);

    // A move constructor that takes consumes a string.
    explicit Symbol(std::string&& symbol_str) : symbol_(std::move(symbol_str)) {}

    std::string symbol_;
  };

  SymbolizerFn symbolizer_fn_;
  absl::flat_hash_map<uintptr_t, Symbol> cache_;
  absl::flat_hash_map<uintptr_t, Symbol> prev_cache_;
};

}  // namespace stirling
}  // namespace px
