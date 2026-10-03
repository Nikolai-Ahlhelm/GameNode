package gameconfig

import (
	"bytes"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"gamenode/internal/templates"
)

// propertiesFormat edits a Java .properties file such as Minecraft's
// server.properties. Unlike ini-key-values it tolerates a missing target (the
// file is created) and missing keys (they are appended), because the game adds
// and renames keys between versions and itself defaults any absent key.
const propertiesFormat = templates.FormatPropertiesKeyValues

// propertiesKey is the property-name shape for this format (lowercase, digits,
// dots, hyphens), e.g. "level-seed" or "query.port".
var propertiesKey = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,63}$`)

const propertiesHeader = "# Minecraft server properties\n# Managed by GameNode; unmanaged keys are preserved.\n"

func propertyNameFor(format string) *regexp.Regexp {
	if format == propertiesFormat {
		return propertiesKey
	}
	return propertyName
}

// normalizePropertiesBoolean writes the Java spelling. Template variables carry
// booleans as 1/0, which java.lang.Boolean.parseBoolean would read as false.
func normalizePropertiesBoolean(value string) string {
	switch strings.ToLower(value) {
	case "1", "true":
		return "true"
	case "0", "false":
		return "false"
	}
	return value
}

func transformProperties(data []byte, replacements map[string]string, wanted map[string]bool) ([]byte, map[string]string, error) {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, nil, errors.New("configuration properties file is not valid UTF-8")
	}
	text := string(data)
	hasBOM := strings.HasPrefix(text, "\ufeff")
	text = strings.TrimPrefix(text, "\ufeff")
	newline := "\n"
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) > 100000 {
		return nil, nil, errors.New("configuration properties file is too complex")
	}
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	found := map[string]string{}
	for index, raw := range lines {
		trimmed := strings.TrimLeft(raw, " \t\f")
		if trimmed == "" || trimmed[0] == '#' || trimmed[0] == '!' {
			continue
		}
		equals := strings.IndexByte(raw, '=')
		if equals <= 0 {
			continue
		}
		key := strings.TrimSpace(raw[:equals])
		if !wanted[key] {
			continue
		}
		if _, duplicate := found[key]; duplicate {
			return nil, nil, errors.New("configuration property is duplicated")
		}
		found[key] = unescapeProperty(strings.TrimLeft(raw[equals+1:], " \t\f"))
		if replacement, ok := replacements[key]; ok {
			lines[index] = raw[:equals+1] + escapeProperty(replacement)
			found[key] = replacement
		}
	}
	missing := make([]string, 0, len(replacements))
	for key := range replacements {
		if _, ok := found[key]; !ok {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	for _, key := range missing {
		lines = append(lines, key+"="+escapeProperty(replacements[key]))
		found[key] = replacements[key]
	}
	result := strings.Join(lines, newline)
	if len(lines) > 0 {
		result += newline
	}
	if hasBOM {
		result = "\ufeff" + result
	}
	return []byte(result), found, nil
}

// escapeProperty encodes a value for java.util.Properties: backslash and colon
// are escaped (matching vanilla output), non-ASCII becomes \uXXXX so the file
// loads identically whether the game reads it as Latin-1 or UTF-8, and a
// leading space is preserved.
func escapeProperty(value string) string {
	var builder strings.Builder
	for index, character := range value {
		switch {
		case character == '\\':
			builder.WriteString(`\\`)
		case character == ':':
			builder.WriteString(`\:`)
		case character == ' ' && index == 0:
			builder.WriteString(`\ `)
		case character < 0x20 || character > 0x7e:
			if character > 0xFFFF {
				high, low := utf16.EncodeRune(character)
				builder.WriteString(unicodeEscape(high) + unicodeEscape(low))
			} else {
				builder.WriteString(unicodeEscape(character))
			}
		default:
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

func unicodeEscape(character rune) string {
	const digits = "0123456789abcdef"
	return `\u` + string([]byte{digits[character>>12&15], digits[character>>8&15], digits[character>>4&15], digits[character&15]})
}

func unescapeProperty(value string) string {
	if !strings.ContainsRune(value, '\\') {
		return value
	}
	runes := []rune(value)
	var builder strings.Builder
	for index := 0; index < len(runes); index++ {
		character := runes[index]
		if character != '\\' || index+1 >= len(runes) {
			builder.WriteRune(character)
			continue
		}
		index++
		switch next := runes[index]; next {
		case 't':
			builder.WriteRune('\t')
		case 'n':
			builder.WriteRune('\n')
		case 'r':
			builder.WriteRune('\r')
		case 'f':
			builder.WriteRune('\f')
		case 'u':
			if index+4 < len(runes) {
				if code, ok := parseHex4(runes[index+1 : index+5]); ok {
					index += 4
					if utf16.IsSurrogate(rune(code)) && index+6 < len(runes) && runes[index+1] == '\\' && runes[index+2] == 'u' {
						if low, lowOK := parseHex4(runes[index+3 : index+7]); lowOK {
							if combined := utf16.DecodeRune(rune(code), rune(low)); combined != utf8.RuneError {
								builder.WriteRune(combined)
								index += 6
								continue
							}
						}
					}
					builder.WriteRune(rune(code))
					continue
				}
			}
			builder.WriteRune('u')
		default:
			builder.WriteRune(next)
		}
	}
	return builder.String()
}

func parseHex4(digits []rune) (int, bool) {
	code := 0
	for _, digit := range digits {
		switch {
		case digit >= '0' && digit <= '9':
			code = code<<4 | int(digit-'0')
		case digit >= 'a' && digit <= 'f':
			code = code<<4 | int(digit-'a'+10)
		case digit >= 'A' && digit <= 'F':
			code = code<<4 | int(digit-'A'+10)
		default:
			return 0, false
		}
	}
	return code, true
}
