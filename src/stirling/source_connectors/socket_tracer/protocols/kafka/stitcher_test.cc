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

#include <gmock/gmock.h>
#include <gtest/gtest.h>

#include "src/stirling/source_connectors/socket_tracer/protocols/kafka/stitcher.h"
#include "src/stirling/source_connectors/socket_tracer/protocols/kafka/test_data.h"
#include "src/stirling/source_connectors/socket_tracer/protocols/kafka/types.h"

namespace px {
namespace stirling {
namespace protocols {
namespace kafka {

TEST(KafkaStitcherTest, BasicMatching) {
  std::deque<Packet> req_packets;
  std::deque<Packet> resp_packets;
  RecordsWithErrorCount<Record> result;

  result = StitchFrames(&req_packets, &resp_packets);
  EXPECT_TRUE(resp_packets.empty());
  EXPECT_TRUE(req_packets.empty());
  EXPECT_EQ(result.error_count, 0);
  EXPECT_EQ(result.records.size(), 0);

  req_packets.push_back(testdata::kProduceReqPacket);

  result = StitchFrames(&req_packets, &resp_packets);
  EXPECT_TRUE(resp_packets.empty());
  EXPECT_EQ(req_packets.size(), 1);
  EXPECT_EQ(result.error_count, 0);
  EXPECT_EQ(result.records.size(), 0);

  resp_packets.push_back(testdata::kProduceRespPacket);

  result = StitchFrames(&req_packets, &resp_packets);
  EXPECT_TRUE(resp_packets.empty());
  EXPECT_EQ(req_packets.size(), 0);
  EXPECT_EQ(result.error_count, 0);
  EXPECT_EQ(result.records.size(), 1);
}

}  // namespace kafka
}  // namespace protocols
}  // namespace stirling
}  // namespace px
