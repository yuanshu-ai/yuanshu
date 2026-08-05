package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"

	"github.com/gowebpki/jcs"
)

func decodeStrictJSON(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("invalid UTF-8")
	}
	if err := validateJSONSurrogates(raw); err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeUniqueJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	if _, err := jcs.Transform(raw); err != nil {
		return nil, errors.New("JSON is not valid I-JSON")
	}
	return value, nil
}

// DecodeStrictJSON is shared by minor-version protocol validators so duplicate
// member, UTF-8, surrogate, and I-JSON rules cannot drift between v1.0 and
// v1.1. It performs syntax validation only; callers must still apply their
// version-specific schema, target, signature, and replay checks.
func DecodeStrictJSON(raw []byte) (any, error) {
	return decodeStrictJSON(raw)
}

func decodeUniqueJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}

	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("object key is not a string")
			}
			if _, duplicate := object[key]; duplicate {
				return nil, errors.New("duplicate object member")
			}
			value, err := decodeUniqueJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return nil, errors.New("malformed JSON object")
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := decodeUniqueJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return nil, errors.New("malformed JSON array")
		}
		return array, nil
	default:
		return nil, errors.New("unexpected JSON delimiter")
	}
}

func validateJSONSurrogates(raw []byte) error {
	for index := 0; index < len(raw); index++ {
		if raw[index] != '"' {
			continue
		}
		for index++; index < len(raw); index++ {
			switch raw[index] {
			case '"':
				goto stringDone
			case '\\':
				index++
				if index >= len(raw) {
					return errors.New("truncated JSON escape")
				}
				if raw[index] != 'u' {
					continue
				}
				first, err := decodeHexQuad(raw, index+1)
				if err != nil {
					return err
				}
				index += 4
				switch {
				case first >= 0xD800 && first <= 0xDBFF:
					if index+6 >= len(raw) || raw[index+1] != '\\' || raw[index+2] != 'u' {
						return errors.New("unpaired high surrogate")
					}
					second, err := decodeHexQuad(raw, index+3)
					if err != nil || second < 0xDC00 || second > 0xDFFF {
						return errors.New("unpaired high surrogate")
					}
					index += 6
				case first >= 0xDC00 && first <= 0xDFFF:
					return errors.New("unpaired low surrogate")
				}
			case 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
				0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F,
				0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
				0x18, 0x19, 0x1A, 0x1B, 0x1C, 0x1D, 0x1E, 0x1F:
				return errors.New("unescaped control character")
			}
		}
		return errors.New("unterminated JSON string")
	stringDone:
	}
	return nil
}

func decodeHexQuad(raw []byte, start int) (int64, error) {
	if start+4 > len(raw) {
		return 0, errors.New("truncated Unicode escape")
	}
	value, err := strconv.ParseInt(string(raw[start:start+4]), 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid Unicode escape")
	}
	return value, nil
}
