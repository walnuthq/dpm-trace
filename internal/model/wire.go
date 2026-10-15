package model

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Protobuf wire types this reader understands. Groups (3, 4) are deprecated
// and never appear in Ledger API messages.
const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

// wireField is one field of an encoded protobuf message. A varint or fixed
// field carries its value in Number; a length-delimited one in Bytes.
type wireField struct {
	Num    int
	Type   int
	Number uint64
	Bytes  []byte
}

var errTruncated = errors.New("truncated protobuf message")

// wireFields splits an encoded message into its fields, in wire order.
//
// The module is standard library only, so this reads the wire format
// directly: it knows field numbers and wire types, and the decoder that calls
// it knows the schema.
func wireFields(data []byte) ([]wireField, error) {
	var fields []wireField
	for len(data) > 0 {
		key, n := binary.Uvarint(data)
		if n <= 0 {
			return nil, errTruncated
		}
		data = data[n:]
		field := wireField{Num: int(key >> 3), Type: int(key & 7)}
		if field.Num == 0 {
			return nil, fmt.Errorf("invalid protobuf field number 0")
		}
		switch field.Type {
		case wireVarint:
			value, n := binary.Uvarint(data)
			if n <= 0 {
				return nil, errTruncated
			}
			field.Number = value
			data = data[n:]
		case wireFixed64:
			if len(data) < 8 {
				return nil, errTruncated
			}
			field.Number = binary.LittleEndian.Uint64(data)
			data = data[8:]
		case wireFixed32:
			if len(data) < 4 {
				return nil, errTruncated
			}
			field.Number = uint64(binary.LittleEndian.Uint32(data))
			data = data[4:]
		case wireBytes:
			length, n := binary.Uvarint(data)
			if n <= 0 || length > uint64(len(data)-n) {
				return nil, errTruncated
			}
			field.Bytes = data[n : n+int(length)]
			data = data[n+int(length):]
		default:
			return nil, fmt.Errorf("unsupported protobuf wire type %d", field.Type)
		}
		fields = append(fields, field)
	}
	return fields, nil
}

// zigzag decodes a sint64.
func zigzag(value uint64) int64 {
	return int64(value>>1) ^ -int64(value&1)
}
