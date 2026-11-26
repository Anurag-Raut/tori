package provider

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
)

const agyOAuthSentinelKey = "oauthTokenInfoSentinelKey"

// createUnifiedOAuthToken encodes an Antigravity Desktop/IDE unified OAuth state entry
// for the `antigravityUnifiedStateSync.oauthToken` ItemTable key in state.vscdb.
func createUnifiedOAuthToken(accessToken, refreshToken string, expirySeconds int64) string {
	oauthInfo := encodeAgyOAuthInfo(accessToken, refreshToken, expirySeconds)
	row := protoEncodeString(1, base64.StdEncoding.EncodeToString(oauthInfo))
	entry := append(protoEncodeString(1, agyOAuthSentinelKey), protoEncodeBytes(2, row)...)
	outer := protoEncodeBytes(1, entry)
	return base64.StdEncoding.EncodeToString(outer)
}

func encodeAgyOAuthInfo(accessToken, refreshToken string, expirySeconds int64) []byte {
	out := make([]byte, 0, len(accessToken)+len(refreshToken)+48)
	out = append(out, protoEncodeString(1, accessToken)...)
	out = append(out, protoEncodeString(2, "Bearer")...)
	out = append(out, protoEncodeString(3, refreshToken)...)
	if expirySeconds > 0 {
		ts := append(protoEncodeVarintField(1, uint64(expirySeconds)), protoEncodeVarintField(2, 0)...)
		out = append(out, protoEncodeBytes(4, ts)...)
	}
	return out
}

// extractOAuthFromUnified parses a base64 `antigravityUnifiedStateSync.oauthToken` entry.
func extractOAuthFromUnified(b64 string) (access, refresh string, expiry int64, err error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", "", 0, fmt.Errorf("decode unified oauth base64: %w", err)
	}
	inner, ok := protoGetBytes(raw, 1)
	if !ok {
		return "", "", 0, fmt.Errorf("missing unified oauth outer field 1")
	}
	row, ok := protoGetBytes(inner, 2)
	if !ok {
		return "", "", 0, fmt.Errorf("missing unified oauth inner field 2")
	}
	payloadStr, ok := protoGetString(row, 1)
	if !ok {
		return "", "", 0, fmt.Errorf("missing unified oauth payload")
	}
	oauthInfo, err := base64.StdEncoding.DecodeString(payloadStr)
	if err != nil {
		// Some versions store raw protobuf bytes directly instead of nested base64.
		oauthInfo = []byte(payloadStr)
	}
	access, _ = protoGetString(oauthInfo, 1)
	refresh, _ = protoGetString(oauthInfo, 3)
	if refresh == "" {
		return "", "", 0, fmt.Errorf("unified oauth entry missing refresh token")
	}
	if ts, ok := protoGetBytes(oauthInfo, 4); ok {
		if sec, ok := protoGetVarint(ts, 1); ok {
			expiry = int64(sec)
		}
	}
	return access, refresh, expiry, nil
}

func protoEncodeVarint(v uint64) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], v)
	return buf[:n]
}

func protoEncodeVarintField(fieldNum int, v uint64) []byte {
	tag := uint64(fieldNum<<3 | 0)
	return append(protoEncodeVarint(tag), protoEncodeVarint(v)...)
}

func protoEncodeString(fieldNum int, s string) []byte {
	return protoEncodeBytes(fieldNum, []byte(s))
}

func protoEncodeBytes(fieldNum int, data []byte) []byte {
	tag := uint64(fieldNum<<3 | 2)
	out := protoEncodeVarint(tag)
	out = append(out, protoEncodeVarint(uint64(len(data)))...)
	out = append(out, data...)
	return out
}

func protoGetBytes(data []byte, fieldNum int) ([]byte, bool) {
	offset := 0
	for offset < len(data) {
		tag, n := binary.Uvarint(data[offset:])
		if n <= 0 {
			return nil, false
		}
		offset += n
		num := int(tag >> 3)
		wire := int(tag & 7)
		switch wire {
		case 0:
			_, n2 := binary.Uvarint(data[offset:])
			if n2 <= 0 {
				return nil, false
			}
			offset += n2
		case 1:
			if offset+8 > len(data) {
				return nil, false
			}
			offset += 8
		case 2:
			l, n2 := binary.Uvarint(data[offset:])
			if n2 <= 0 {
				return nil, false
			}
			offset += n2
			end := offset + int(l)
			if end < offset || end > len(data) {
				return nil, false
			}
			if num == fieldNum {
				return data[offset:end], true
			}
			offset = end
		case 5:
			if offset+4 > len(data) {
				return nil, false
			}
			offset += 4
		default:
			return nil, false
		}
	}
	return nil, false
}

func protoGetString(data []byte, fieldNum int) (string, bool) {
	b, ok := protoGetBytes(data, fieldNum)
	if !ok {
		return "", false
	}
	return string(b), true
}

func protoGetVarint(data []byte, fieldNum int) (uint64, bool) {
	offset := 0
	for offset < len(data) {
		tag, n := binary.Uvarint(data[offset:])
		if n <= 0 {
			return 0, false
		}
		offset += n
		num := int(tag >> 3)
		wire := int(tag & 7)
		switch wire {
		case 0:
			v, n2 := binary.Uvarint(data[offset:])
			if n2 <= 0 {
				return 0, false
			}
			offset += n2
			if num == fieldNum {
				return v, true
			}
		case 1:
			if offset+8 > len(data) {
				return 0, false
			}
			offset += 8
		case 2:
			l, n2 := binary.Uvarint(data[offset:])
			if n2 <= 0 {
				return 0, false
			}
			offset += n2 + int(l)
			if offset > len(data) {
				return 0, false
			}
		case 5:
			if offset+4 > len(data) {
				return 0, false
			}
			offset += 4
		default:
			return 0, false
		}
	}
	return 0, false
}
