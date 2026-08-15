package gameconfig

import (
	"bytes"
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"gamenode/internal/templates"
)

const (
	maxSectionTupleBytes = 512 << 10
	maxSectionTuplePairs = 512
	maxTupleNesting      = 8
)

type sectionTuplePair struct {
	key      string
	rawValue string
}

// transformSectionTuple edits one configured parenthesized key/value container
// in one configured INI section. It has no game-specific property knowledge.
func transformSectionTuple(data []byte, replacements map[string]string, fields []templates.ConfigAdapterField, section, container string) ([]byte, map[string]string, error) {
	if len(data) > MaxConfigBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, nil, errors.New("section tuple configuration must be bounded UTF-8")
	}
	fieldByProperty := make(map[string]templates.ConfigAdapterField, len(fields))
	for _, field := range fields {
		fieldByProperty[field.Property] = field
	}
	text := string(data)
	hasBOM := strings.HasPrefix(text, "\ufeff")
	text = strings.TrimPrefix(text, "\ufeff")
	lines := strings.Split(text, "\n")
	if len(lines) > 100000 {
		return nil, nil, errors.New("section tuple configuration is too complex")
	}
	targetSection := "[" + section + "]"
	sectionSeen, inSection, containerSeen := false, false, false
	found := map[string]string{}
	for index, line := range lines {
		hasCR := strings.HasSuffix(line, "\r")
		raw := strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inSection = trimmed == targetSection
			if inSection {
				if sectionSeen {
					return nil, nil, errors.New("section tuple target section is duplicated")
				}
				sectionSeen = true
			}
			continue
		}
		if !inSection || trimmed == "" || strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		equals := strings.IndexByte(raw, '=')
		if equals < 0 || strings.TrimSpace(raw[:equals]) != container {
			continue
		}
		if containerSeen {
			return nil, nil, errors.New("section tuple container is duplicated")
		}
		containerSeen = true
		afterEquals := raw[equals+1:]
		leading := len(afterEquals) - len(strings.TrimLeft(afterEquals, " \t"))
		trailing := len(afterEquals) - len(strings.TrimRight(afterEquals, " \t"))
		if leading+trailing > len(afterEquals) {
			return nil, nil, errors.New("section tuple whitespace is invalid")
		}
		tuple := afterEquals[leading : len(afterEquals)-trailing]
		updated, values, err := parseAndUpdateSectionTuple(tuple, replacements, fieldByProperty)
		if err != nil {
			return nil, nil, err
		}
		found = values
		line = raw[:equals+1] + afterEquals[:leading] + updated + afterEquals[len(afterEquals)-trailing:]
		if hasCR {
			line += "\r"
		}
		lines[index] = line
	}
	if !sectionSeen || !containerSeen {
		return nil, nil, errors.New("section tuple target is missing")
	}
	result := strings.Join(lines, "\n")
	if hasBOM {
		result = "\ufeff" + result
	}
	return []byte(result), found, nil
}

func parseAndUpdateSectionTuple(tuple string, replacements map[string]string, fields map[string]templates.ConfigAdapterField) (string, map[string]string, error) {
	if len(tuple) < 2 || len(tuple) > maxSectionTupleBytes || tuple[0] != '(' || tuple[len(tuple)-1] != ')' {
		return "", nil, errors.New("section tuple is invalid")
	}
	parts, err := splitSectionTuplePairs(tuple[1 : len(tuple)-1])
	if err != nil {
		return "", nil, err
	}
	pairs := make([]sectionTuplePair, 0, len(parts))
	mappedSeen, found := map[string]bool{}, map[string]string{}
	for _, part := range parts {
		equals := strings.IndexByte(part, '=')
		if equals <= 0 {
			return "", nil, errors.New("section tuple pair is invalid")
		}
		key, rawValue := strings.TrimSpace(part[:equals]), strings.TrimSpace(part[equals+1:])
		if !propertyName.MatchString(key) || rawValue == "" {
			return "", nil, errors.New("section tuple property is invalid")
		}
		pair := sectionTuplePair{key: key, rawValue: rawValue}
		if field, managed := fields[key]; managed {
			if mappedSeen[key] {
				return "", nil, errors.New("section tuple managed property is duplicated")
			}
			mappedSeen[key] = true
			decoded, decodeErr := decodeSectionTupleValue(field.Type, rawValue)
			if decodeErr != nil {
				return "", nil, decodeErr
			}
			found[key] = decoded
			if replacement, ok := replacements[key]; ok {
				pair.rawValue, decodeErr = encodeSectionTupleValue(field.Type, replacement)
				if decodeErr != nil {
					return "", nil, decodeErr
				}
			}
		}
		pairs = append(pairs, pair)
	}
	for key := range fields {
		if _, ok := found[key]; !ok {
			return "", nil, errors.New("section tuple managed property is missing")
		}
	}
	encoded := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		encoded = append(encoded, pair.key+"="+pair.rawValue)
	}
	return "(" + strings.Join(encoded, ",") + ")", found, nil
}

func splitSectionTuplePairs(inner string) ([]string, error) {
	if inner == "" {
		return nil, errors.New("section tuple is empty")
	}
	parts := []string{}
	start, depth := 0, 0
	quoted, escaped := false, false
	for index := 0; index < len(inner); index++ {
		character := inner[index]
		if quoted {
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
			} else if character == '"' {
				quoted = false
			}
			continue
		}
		switch character {
		case '"':
			quoted = true
		case '(':
			depth++
			if depth > maxTupleNesting {
				return nil, errors.New("section tuple nesting is too deep")
			}
		case ')':
			depth--
			if depth < 0 {
				return nil, errors.New("section tuple nesting is invalid")
			}
		case ',':
			if depth == 0 {
				parts = append(parts, inner[start:index])
				if len(parts) > maxSectionTuplePairs {
					return nil, errors.New("section tuple has too many properties")
				}
				start = index + 1
			}
		case '\r', '\n', 0:
			return nil, errors.New("section tuple contains invalid control data")
		}
	}
	if quoted || escaped || depth != 0 {
		return nil, errors.New("section tuple quoting or nesting is invalid")
	}
	parts = append(parts, inner[start:])
	if len(parts) > maxSectionTuplePairs {
		return nil, errors.New("section tuple has too many properties")
	}
	return parts, nil
}

func decodeSectionTupleValue(typeName, raw string) (string, error) {
	switch typeName {
	case "string", "secret":
		return decodeSectionTupleQuoted(raw)
	case "boolean":
		if strings.EqualFold(raw, "true") {
			return "true", nil
		}
		if strings.EqualFold(raw, "false") {
			return "false", nil
		}
	case "integer":
		if _, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return raw, nil
		}
	case "number":
		if value, err := strconv.ParseFloat(raw, 64); err == nil && !math.IsInf(value, 0) && !math.IsNaN(value) {
			return raw, nil
		}
	case "enum":
		if strings.HasPrefix(raw, `"`) {
			return decodeSectionTupleQuoted(raw)
		}
		if propertyName.MatchString(raw) {
			return raw, nil
		}
	}
	return "", errors.New("section tuple managed property has an invalid value type")
}

func decodeSectionTupleQuoted(raw string) (string, error) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", errors.New("section tuple managed string is not quoted")
	}
	var output strings.Builder
	for index := 1; index < len(raw)-1; index++ {
		character := raw[index]
		if character == '\\' {
			index++
			if index >= len(raw)-1 || (raw[index] != '\\' && raw[index] != '"') {
				return "", errors.New("section tuple managed string escape is invalid")
			}
			character = raw[index]
		} else if character == '"' || character == '\r' || character == '\n' || character == 0 {
			return "", errors.New("section tuple managed string is invalid")
		}
		output.WriteByte(character)
	}
	return output.String(), nil
}

func encodeSectionTupleValue(typeName, value string) (string, error) {
	switch typeName {
	case "string", "secret":
		if strings.ContainsAny(value, "\x00\r\n") {
			return "", ErrInvalidValue
		}
		value = strings.ReplaceAll(value, `\`, `\\`)
		value = strings.ReplaceAll(value, `"`, `\"`)
		return `"` + value + `"`, nil
	case "boolean":
		if value == "true" || value == "1" {
			return "True", nil
		}
		if value == "false" || value == "0" {
			return "False", nil
		}
	case "integer":
		if _, err := strconv.ParseInt(value, 10, 64); err == nil {
			return value, nil
		}
	case "number":
		if parsed, err := strconv.ParseFloat(value, 64); err == nil && !math.IsInf(parsed, 0) && !math.IsNaN(parsed) {
			return value, nil
		}
	case "enum":
		if propertyName.MatchString(value) {
			return value, nil
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return "", ErrInvalidValue
		}
		value = strings.ReplaceAll(value, `\`, `\\`)
		value = strings.ReplaceAll(value, `"`, `\"`)
		return `"` + value + `"`, nil
	}
	return "", ErrInvalidValue
}
