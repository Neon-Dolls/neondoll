package dollcard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// UnknownFields retains extension fields that this Core does not understand.
// Values are opaque JSON and must never be interpreted as authority or trusted
// instructions merely because they are present in a DollCard.
type UnknownFields map[string]json.RawMessage

func decodeExtensibleObject(data []byte, known any, knownNames ...string) (UnknownFields, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, known); err != nil {
		return nil, err
	}
	for _, name := range knownNames {
		delete(fields, name)
	}
	unknown := make(UnknownFields, len(fields))
	for name, value := range fields {
		unknown[name] = append(json.RawMessage(nil), value...)
	}
	return unknown, nil
}

func marshalExtensibleObject(known any, unknown UnknownFields, knownNames ...string) ([]byte, error) {
	if err := validateUnknownFields(unknown, knownNames...); err != nil {
		return nil, err
	}
	data, err := json.Marshal(known)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for name, value := range unknown {
		fields[name] = append(json.RawMessage(nil), value...)
	}
	return json.Marshal(fields)
}

func validateUnknownFields(unknown UnknownFields, knownNames ...string) error {
	reserved := make(map[string]bool, len(knownNames))
	for _, name := range knownNames {
		reserved[name] = true
	}
	for name, value := range unknown {
		if name == "" || reserved[name] || !json.Valid(value) || rejectDuplicateJSONKeys(value) != nil {
			return ErrInvalidContractIndex
		}
	}
	return nil
}

// rejectDuplicateJSONKeys rejects ambiguity at every object nesting level.
// encoding/json otherwise accepts duplicate keys and silently keeps one value.
func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder); err != nil {
		return ErrInvalidContractIndex
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrInvalidContractIndex
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return fmt.Errorf("duplicate or invalid JSON object key")
			}
			seen[key] = true
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("invalid JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
	return nil
}
