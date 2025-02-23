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

#include "src/stirling/source_connectors/socket_tracer/protocols/kafka/packet_decoder.h"
#include <string>
#include "src/common/base/byte_utils.h"
#include "src/stirling/source_connectors/socket_tracer/protocols/kafka/types.h"

namespace px {
namespace stirling {
namespace protocols {
namespace kafka {

// TODO(chengruizhe): Many of the methods here are shareable with other protocols such as CQL.

template <typename TCharType>
StatusOr<std::basic_string<TCharType>> PacketDecoder::ExtractBytesCore(int32_t len) {
  PL_ASSIGN_OR_RETURN(std::basic_string_view<TCharType> tbuf,
                      binary_decoder_.ExtractString<TCharType>(len));
  return std::basic_string<TCharType>(tbuf);
}

template <uint8_t TMaxLength>
StatusOr<int64_t> PacketDecoder::ExtractUnsignedVarintCore() {
  constexpr uint8_t kFirstBitMask = 0x80;
  constexpr uint8_t kLastSevenBitMask = 0x7f;
  constexpr uint8_t kByteLength = 7;

  int64_t value = 0;
  for (int i = 0; i < TMaxLength; i += kByteLength) {
    PL_ASSIGN_OR_RETURN(uint64_t b, binary_decoder_.ExtractChar());
    if (!(b & kFirstBitMask)) {
      value |= (b << i);
      return value;
    }
    value |= ((b & kLastSevenBitMask) << i);
  }
  return error::Internal("Extract Varint Core failure.");
}

template <uint8_t TMaxLength>
StatusOr<int64_t> PacketDecoder::ExtractVarintCore() {
  PL_ASSIGN_OR_RETURN(int64_t value, ExtractUnsignedVarintCore<TMaxLength>());
  // Casting to uint64_t for logical right shift.
  return (static_cast<uint64_t>(value) >> 1) ^ (-(value & 1));
}

/*
 * Primitive Type Parsers
 */

StatusOr<int8_t> PacketDecoder::ExtractInt8() { return binary_decoder_.ExtractInt<int8_t>(); }

StatusOr<int16_t> PacketDecoder::ExtractInt16() { return binary_decoder_.ExtractInt<int16_t>(); }

StatusOr<int32_t> PacketDecoder::ExtractInt32() { return binary_decoder_.ExtractInt<int32_t>(); }

StatusOr<int64_t> PacketDecoder::ExtractInt64() { return binary_decoder_.ExtractInt<int64_t>(); }

StatusOr<int32_t> PacketDecoder::ExtractUnsignedVarint() {
  constexpr uint8_t kVarintMaxLength = 35;
  return ExtractUnsignedVarintCore<kVarintMaxLength>();
}

StatusOr<int32_t> PacketDecoder::ExtractVarint() {
  constexpr uint8_t kVarintMaxLength = 35;
  return ExtractVarintCore<kVarintMaxLength>();
}

StatusOr<int64_t> PacketDecoder::ExtractVarlong() {
  constexpr uint8_t kVarlongMaxLength = 70;
  return ExtractVarintCore<kVarlongMaxLength>();
}

StatusOr<std::string> PacketDecoder::ExtractString() {
  PL_ASSIGN_OR_RETURN(int16_t len, ExtractInt16());
  return ExtractBytesCore<char>(len);
}

StatusOr<std::string> PacketDecoder::ExtractNullableString() {
  PL_ASSIGN_OR_RETURN(int16_t len, ExtractInt16());
  if (len == -1) {
    return std::string();
  }
  return ExtractBytesCore<char>(len);
}

StatusOr<std::string> PacketDecoder::ExtractCompactString() {
  PL_ASSIGN_OR_RETURN(int32_t len, ExtractUnsignedVarint());
  // length N + 1 is encoded.
  len -= 1;
  if (len < 0) {
    return error::Internal("Compact String has negative length.");
  }
  return ExtractBytesCore<char>(len);
}

StatusOr<std::string> PacketDecoder::ExtractCompactNullableString() {
  PL_ASSIGN_OR_RETURN(int32_t len, ExtractUnsignedVarint());
  // length N + 1 is encoded.
  len -= 1;
  if (len < -1) {
    return error::Internal("Compact Nullable String has negative length.");
  }
  if (len == -1) {
    return std::string();
  }
  return ExtractBytesCore<char>(len);
}

// Only supports Kafka version >= 0.11.0
StatusOr<RecordMessage> PacketDecoder::ExtractRecordMessage() {
  RecordMessage r;
  PL_ASSIGN_OR_RETURN(int32_t length, ExtractVarint());
  PL_RETURN_IF_ERROR(MarkOffset(length));

  PL_ASSIGN_OR_RETURN(int8_t attributes, ExtractInt8());
  PL_ASSIGN_OR_RETURN(int64_t timestamp_delta, ExtractVarlong());
  PL_ASSIGN_OR_RETURN(int32_t offset_delta, ExtractVarint());
  PL_ASSIGN_OR_RETURN(r.key, ExtractBytesZigZag());
  PL_ASSIGN_OR_RETURN(r.value, ExtractBytesZigZag());

  PL_UNUSED(attributes);
  PL_UNUSED(timestamp_delta);
  PL_UNUSED(offset_delta);

  // Discard record headers and jump to the marked offset.
  PL_RETURN_IF_ERROR(JumpToOffset());
  return r;
}

// Only supports Kafka version >= 0.11.0
StatusOr<RecordBatch> PacketDecoder::ExtractRecordBatch() {
  RecordBatch r;
  PL_ASSIGN_OR_RETURN(int64_t base_offset, ExtractInt64());

  PL_ASSIGN_OR_RETURN(int32_t length, ExtractInt32());
  PL_RETURN_IF_ERROR(MarkOffset(length));

  PL_ASSIGN_OR_RETURN(int32_t partition_leader_epoch, ExtractInt32());
  PL_ASSIGN_OR_RETURN(int8_t magic, ExtractInt8());
  // If magic is not v2, then this is potentially an older format.
  if (magic < 2) {
    return error::Internal("Old record batch (message set) format not supported.");
  }
  if (magic > 2) {
    return error::Internal("Unknown magic in ExtractRecordBatch.");
  }

  PL_ASSIGN_OR_RETURN(int32_t crc, ExtractInt32());
  PL_ASSIGN_OR_RETURN(int16_t attributes, ExtractInt16());
  PL_ASSIGN_OR_RETURN(int32_t last_offset_delta, ExtractInt32());
  PL_ASSIGN_OR_RETURN(int64_t first_time_stamp, ExtractInt64());
  PL_ASSIGN_OR_RETURN(int64_t max_time_stamp, ExtractInt64());
  PL_ASSIGN_OR_RETURN(int64_t producer_ID, ExtractInt64());
  PL_ASSIGN_OR_RETURN(int16_t producer_epoch, ExtractInt16());
  PL_ASSIGN_OR_RETURN(int32_t base_sequence, ExtractInt32());

  PL_UNUSED(base_offset);
  PL_UNUSED(partition_leader_epoch);
  PL_UNUSED(crc);
  PL_UNUSED(attributes);
  PL_UNUSED(last_offset_delta);
  PL_UNUSED(first_time_stamp);
  PL_UNUSED(max_time_stamp);
  PL_UNUSED(producer_ID);
  PL_UNUSED(producer_epoch);
  PL_UNUSED(base_sequence);

  PL_ASSIGN_OR_RETURN(r.records, ExtractArray(&PacketDecoder::ExtractRecordMessage));
  PL_RETURN_IF_ERROR(JumpToOffset());
  return r;
}

StatusOr<ProduceReqPartition> PacketDecoder::ExtractProduceReqPartition() {
  ProduceReqPartition r;
  PL_ASSIGN_OR_RETURN(r.index, ExtractInt32());

  // COMPACT_RECORDS is used in api_version >= 9.
  int32_t length = 0;
  // TODO(chengruizhe): Add flexible version support. Flexible version was introduced in Kafka to
  // support more compact datatypes and tagged fields etc.
  if (is_flexible_) {
    PL_ASSIGN_OR_RETURN(length, ExtractUnsignedVarint());
  } else {
    PL_ASSIGN_OR_RETURN(length, ExtractInt32());
  }
  PL_RETURN_IF_ERROR(MarkOffset(length));

  PL_ASSIGN_OR_RETURN(r.record_batch, ExtractRecordBatch());

  PL_RETURN_IF_ERROR(JumpToOffset());
  return r;
}

StatusOr<ProduceReqTopic> PacketDecoder::ExtractProduceReqTopic() {
  ProduceReqTopic r;
  if (is_flexible_) {
    PL_ASSIGN_OR_RETURN(r.name, ExtractCompactString());
    PL_ASSIGN_OR_RETURN(r.partitions,
                        ExtractCompactArray(&PacketDecoder::ExtractProduceReqPartition));
  } else {
    PL_ASSIGN_OR_RETURN(r.name, ExtractString());
    PL_ASSIGN_OR_RETURN(r.partitions, ExtractArray(&PacketDecoder::ExtractProduceReqPartition));
  }
  return r;
}

StatusOr<std::string> PacketDecoder::ExtractBytesZigZag() {
  PL_ASSIGN_OR_RETURN(int32_t len, ExtractVarint());
  if (len < -1) {
    return error::Internal("Not enough bytes in ExtractBytesZigZag.");
  }
  if (len == 0 || len == -1) {
    return std::string();
  }
  return ExtractBytesCore<char>(len);
}

StatusOr<RecordError> PacketDecoder::ExtractRecordError() {
  RecordError r;

  PL_ASSIGN_OR_RETURN(r.batch_index, ExtractInt32());
  if (is_flexible_) {
    PL_ASSIGN_OR_RETURN(r.error_message, ExtractCompactNullableString());
  } else {
    PL_ASSIGN_OR_RETURN(r.error_message, ExtractNullableString());
  }
  return r;
}

StatusOr<ProduceRespPartition> PacketDecoder::ExtractProduceRespPartition() {
  ProduceRespPartition r;

  PL_ASSIGN_OR_RETURN(r.index, ExtractInt32());
  PL_ASSIGN_OR_RETURN(r.error_code, ExtractInt16());
  PL_ASSIGN_OR_RETURN(int64_t base_offset, ExtractInt64());
  if (api_version_ >= 2) {
    PL_ASSIGN_OR_RETURN(int64_t log_append_time_ms, ExtractInt64());
    PL_UNUSED(log_append_time_ms);
  }
  if (api_version_ >= 5) {
    PL_ASSIGN_OR_RETURN(int64_t log_start_offset, ExtractInt64());
    PL_UNUSED(log_start_offset);
  }
  if (is_flexible_) {
    PL_ASSIGN_OR_RETURN(r.record_errors, ExtractCompactArray(&PacketDecoder::ExtractRecordError));
    PL_ASSIGN_OR_RETURN(r.error_message, ExtractCompactNullableString());
  } else if (api_version_ >= 8) {
    PL_ASSIGN_OR_RETURN(r.record_errors, ExtractArray(&PacketDecoder::ExtractRecordError));
    PL_ASSIGN_OR_RETURN(r.error_message, ExtractNullableString());
  }

  PL_UNUSED(base_offset);
  return r;
}

StatusOr<ProduceRespTopic> PacketDecoder::ExtractProduceRespTopic() {
  ProduceRespTopic r;

  if (is_flexible_) {
    PL_ASSIGN_OR_RETURN(r.name, ExtractCompactString());
    PL_ASSIGN_OR_RETURN(r.partitions,
                        ExtractCompactArray(&PacketDecoder::ExtractProduceRespPartition));
  } else {
    PL_ASSIGN_OR_RETURN(r.name, ExtractString());
    PL_ASSIGN_OR_RETURN(r.partitions, ExtractArray(&PacketDecoder::ExtractProduceRespPartition));
  }
  return r;
}

/*
 * Header Parsers
 */

Status PacketDecoder::ExtractReqHeader(Request* req) {
  PL_ASSIGN_OR_RETURN(int16_t api_key, ExtractInt16());
  req->api_key = static_cast<APIKey>(api_key);

  PL_ASSIGN_OR_RETURN(req->api_version, ExtractInt16());
  SetAPIInfo(req->api_key, req->api_version);

  PL_RETURN_IF_ERROR(/* correlation_id */ ExtractInt32());
  PL_ASSIGN_OR_RETURN(req->client_id, ExtractNullableString());
  return Status::OK();
}

Status PacketDecoder::ExtractRespHeader(Response* /*resp*/) {
  PL_RETURN_IF_ERROR(/* correlation_id */ ExtractInt32());

  return Status::OK();
}

/*
 * Message struct Parsers
 */

// Documentation: https://kafka.apache.org/protocol.html#The_Messages_Produce
StatusOr<ProduceReq> PacketDecoder::ExtractProduceReq() {
  ProduceReq r;
  if (api_version_ >= 3) {
    PL_ASSIGN_OR_RETURN(r.transactional_id, ExtractNullableString());
  }

  PL_ASSIGN_OR_RETURN(r.acks, ExtractInt16());
  PL_ASSIGN_OR_RETURN(r.timeout_ms, ExtractInt32());
  if (is_flexible_) {
    PL_ASSIGN_OR_RETURN(r.topics, ExtractCompactArray(&PacketDecoder::ExtractProduceReqTopic));
  } else {
    PL_ASSIGN_OR_RETURN(r.topics, ExtractArray(&PacketDecoder::ExtractProduceReqTopic));
  }
  return r;
}

StatusOr<ProduceResp> PacketDecoder::ExtractProduceResp() {
  ProduceResp r;

  if (is_flexible_) {
    PL_ASSIGN_OR_RETURN(r.topics, ExtractCompactArray(&PacketDecoder::ExtractProduceRespTopic));
  } else {
    PL_ASSIGN_OR_RETURN(r.topics, ExtractArray(&PacketDecoder::ExtractProduceRespTopic));
  }

  if (api_version_ >= 1) {
    PL_ASSIGN_OR_RETURN(r.throttle_time_ms, ExtractInt32());
  }
  return r;
}

}  // namespace kafka
}  // namespace protocols
}  // namespace stirling
}  // namespace px
