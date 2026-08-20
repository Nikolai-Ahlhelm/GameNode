package gameconfig

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gamenode/internal/servers"
	"gamenode/internal/templates"
)

const MaxConfigBytes = 2 << 20

const sectionTupleFormat = "section-tuple-key-values"

var (
	ErrUnavailable  = errors.New("managed game configuration is unavailable")
	ErrInvalidValue = errors.New("configuration value is invalid")
	ErrUnsafeTarget = errors.New("configuration target is unsafe")
	ErrInitialize   = errors.New("configuration initialization failed")
	ErrParse        = errors.New("configuration parse failed")
	ErrApply        = errors.New("configuration apply failed")
	propertyName    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
	sectionName     = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,160}$`)
)

type ServerSource interface {
	Get(context.Context, string) (servers.Record, error)
}

type Service struct {
	db       *sql.DB
	servers  ServerSource
	updateMu sync.Mutex
}

func New(db *sql.DB, serverSource ServerSource) *Service {
	return &Service{db: db, servers: serverSource}
}

type FieldView struct {
	Key         string               `json:"key"`
	Label       string               `json:"label"`
	Description string               `json:"description,omitempty"`
	Section     string               `json:"section,omitempty"`
	Type        string               `json:"type"`
	Value       string               `json:"value,omitempty"`
	Configured  bool                 `json:"configured"`
	Required    bool                 `json:"required"`
	Nullable    bool                 `json:"nullable"`
	Sensitive   bool                 `json:"sensitive"`
	Validation  templates.Validation `json:"validation"`
}

type AdapterView struct {
	ID              string      `json:"id"`
	Version         string      `json:"version"`
	Format          string      `json:"format"`
	Target          string      `json:"target"`
	RestartRequired bool        `json:"restart_required"`
	Ready           bool        `json:"ready"`
	StatusMessage   string      `json:"status_message,omitempty"`
	Fields          []FieldView `json:"fields"`
}

type Result struct {
	Available bool          `json:"available"`
	Adapters  []AdapterView `json:"adapters"`
}

type snapshot struct {
	definition templates.ConfigAdapterDefinition
}

func (s *Service) Get(ctx context.Context, serverID string) (Result, error) {
	record, err := s.servers.Get(ctx, serverID)
	if err != nil {
		return Result{}, err
	}
	definitions, err := s.definitions(ctx, serverID)
	if err != nil {
		return Result{}, err
	}
	result := Result{Available: len(definitions) > 0, Adapters: []AdapterView{}}
	for _, definition := range definitions {
		var values map[string]string
		var err error
		pending := false
		if ManagedLaunch(definition) {
			// Managed launch settings are owned by GameNode, so they are always
			// readable and never wait for the game to generate a file.
			values, err = s.storedValues(ctx, serverID, definition.ID)
			if err != nil {
				return Result{}, err
			}
		} else {
			values, err = Read(record.Server.WorkingDirectory, definition)
			pending = definition.PostStartOnly && errors.Is(err, os.ErrNotExist)
			if err != nil && !pending {
				return Result{}, err
			}
		}
		view := AdapterView{ID: definition.ID, Version: definition.Version, Format: definition.Format, Target: definition.Target, RestartRequired: definition.RestartRequired, Ready: !pending, Fields: make([]FieldView, 0, len(definition.Fields))}
		if pending {
			view.StatusMessage = "Start this server once so the game can generate its configuration file."
			values = map[string]string{}
		}
		for _, field := range definition.Fields {
			value := values[field.Key]
			item := FieldView{Key: field.Key, Label: field.Label, Description: field.Description, Section: field.Section, Type: field.Type, Configured: value != "", Required: field.Required, Nullable: field.Nullable, Sensitive: field.Sensitive, Validation: field.Validation}
			if !field.Sensitive {
				item.Value = value
			}
			view.Fields = append(view.Fields, item)
		}
		result.Adapters = append(result.Adapters, view)
	}
	return result, nil
}

func (s *Service) Update(ctx context.Context, serverID, adapterID string, values map[string]string) (Result, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	record, err := s.servers.Get(ctx, serverID)
	if err != nil {
		return Result{}, err
	}
	definitions, err := s.definitions(ctx, serverID)
	if err != nil {
		return Result{}, err
	}
	for _, definition := range definitions {
		if definition.ID != adapterID {
			continue
		}
		if ManagedLaunch(definition) {
			if err = s.applyManagedValues(ctx, serverID, definition, values); err != nil {
				return Result{}, err
			}
		} else if err = Apply(record.Server.WorkingDirectory, definition, values); err != nil {
			if definition.PostStartOnly && errors.Is(err, os.ErrNotExist) {
				return Result{}, ErrUnavailable
			}
			return Result{}, err
		}
		_, err = s.db.ExecContext(ctx, `UPDATE server_config_adapters SET updated_at=? WHERE server_id=? AND adapter_id=?`, time.Now().UTC().Format(time.RFC3339Nano), serverID, adapterID)
		if err != nil {
			return Result{}, err
		}
		return s.Get(ctx, serverID)
	}
	return Result{}, ErrUnavailable
}

func (s *Service) definitions(ctx context.Context, serverID string) ([]templates.ConfigAdapterDefinition, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT definition_json FROM server_config_adapters WHERE server_id=? ORDER BY adapter_id`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []templates.ConfigAdapterDefinition
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var definition templates.ConfigAdapterDefinition
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&definition); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return nil, errors.New("stored configuration adapter is invalid")
		}
		if err = ValidateDefinition(definition); err != nil {
			return nil, err
		}
		result = append(result, definition)
	}
	return result, rows.Err()
}

func ValidateDefinition(definition templates.ConfigAdapterDefinition) error {
	if (definition.SchemaVersion != 1 && definition.SchemaVersion != templates.AdapterSchemaVersion) || definition.ID == "" || definition.Version == "" || len(definition.Fields) == 0 || len(definition.Fields) > 128 {
		return ErrUnsafeTarget
	}
	tupleFormat := definition.Format == sectionTupleFormat
	if ManagedLaunch(definition) {
		// A managed-launch adapter stores its values in GameNode and owns no
		// game file, so every file-specific descriptor must stay empty.
		if definition.SchemaVersion < templates.AdapterSchemaVersion || definition.Target != "" || definition.Initialization != nil || definition.PostStartOnly {
			return ErrUnsafeTarget
		}
	} else {
		standardFormat := definition.Format == templates.FormatXMLProperties || definition.Format == templates.FormatINIKeyValues || definition.Format == templates.FormatJSONKeyValues || definition.Format == templates.FormatINISectionKeyValues
		if !safeDefinitionTarget(definition.Format, definition.Target) || (!standardFormat && !tupleFormat) || (definition.PostStartOnly && definition.Format != templates.FormatINIKeyValues && definition.Format != templates.FormatJSONKeyValues && definition.Format != templates.FormatINISectionKeyValues) {
			return ErrUnsafeTarget
		}
		if definition.Initialization != nil {
			if definition.PostStartOnly || definition.Initialization.Mode != "seed-from-file" || !safeDefinitionPath(definition.Initialization.Source) {
				return ErrUnsafeTarget
			}
		}
	}
	if tupleFormat {
		if !sectionName.MatchString(definition.Section) || !propertyName.MatchString(definition.ContainerProperty) {
			return ErrInvalidValue
		}
	} else if definition.Format == templates.FormatINISectionKeyValues {
		if !sectionName.MatchString(definition.Section) || definition.ContainerProperty != "" {
			return ErrInvalidValue
		}
	} else if definition.Section != "" || definition.ContainerProperty != "" {
		return ErrInvalidValue
	}
	keys, properties, bindings := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, field := range definition.Fields {
		if field.Key == "" || keys[field.Key] || field.Label == "" {
			return ErrInvalidValue
		}
		switch field.Type {
		case "string", "integer", "number", "boolean", "enum", "secret":
		default:
			return ErrInvalidValue
		}
		if ManagedLaunch(definition) {
			if field.Property != "" || templates.ValidateAdapterBinding(field) != nil {
				return ErrInvalidValue
			}
			target := templates.BindingTarget(*field.Binding)
			if bindings[target] {
				return ErrInvalidValue
			}
			bindings[target] = true
		} else if field.Binding != nil || properties[field.Property] || !propertyName.MatchString(field.Property) {
			return ErrInvalidValue
		}
		keys[field.Key], properties[field.Property] = true, true
	}
	return nil
}

// ManagedLaunch reports whether the adapter binds its values to the native
// launch instead of editing a game-owned configuration file.
func ManagedLaunch(definition templates.ConfigAdapterDefinition) bool {
	return definition.Format == templates.FormatManagedLaunch
}

func safeDefinitionTarget(format, target string) bool {
	extension := strings.ToLower(filepath.Ext(target))
	if (format != "xml-properties" && format != "ini-key-values" && format != templates.FormatJSONKeyValues && format != templates.FormatINISectionKeyValues && format != sectionTupleFormat) || (format == "xml-properties" && extension != ".xml") || ((format == "ini-key-values" || format == templates.FormatINISectionKeyValues || format == sectionTupleFormat) && extension != ".ini") || (format == templates.FormatJSONKeyValues && extension != ".json") || !safeDefinitionPath(target) {
		return false
	}
	return true
}

func safeDefinitionPath(target string) bool {
	if target == "" || len(target) > 240 || strings.Contains(target, `\`) || filepath.IsAbs(target) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(target)))
	if clean != target || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return false
	}
	segments := strings.Split(clean, "/")
	if len(segments) > 8 {
		return false
	}
	segment := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	for _, value := range segments {
		if !segment.MatchString(value) {
			return false
		}
	}
	return true
}

func Apply(root string, definition templates.ConfigAdapterDefinition, values map[string]string) error {
	return applyWithWriters(root, definition, values, atomicWrite, atomicCreate)
}

func applyWithWriter(root string, definition templates.ConfigAdapterDefinition, values map[string]string, write func(string, []byte) error) error {
	return applyWithWriters(root, definition, values, write, write)
}

func applyWithWriters(root string, definition templates.ConfigAdapterDefinition, values map[string]string, write, create func(string, []byte) error) error {
	if err := ValidateDefinition(definition); err != nil {
		return err
	}
	allowed := map[string]templates.ConfigAdapterField{}
	for _, field := range definition.Fields {
		allowed[field.Key] = field
	}
	replacements := map[string]string{}
	for key, value := range values {
		field, ok := allowed[key]
		if !ok {
			return fmt.Errorf("%w: unknown field", ErrInvalidValue)
		}
		if err := validateValue(field, value); err != nil {
			return err
		}
		if (definition.Format == "ini-key-values" || definition.Format == templates.FormatINISectionKeyValues || definition.Format == sectionTupleFormat || definition.Format == templates.FormatJSONKeyValues) && strings.ContainsAny(value, "\r\n") {
			return ErrInvalidValue
		}
		replacements[field.Property] = value
	}
	if len(replacements) == 0 {
		return nil
	}
	target, err := safeTarget(root, definition.Target)
	if err != nil {
		return err
	}
	data, err := readBounded(target)
	targetExisted := err == nil
	if err != nil && definition.Initialization != nil && errors.Is(err, os.ErrNotExist) {
		source, sourceErr := safeTarget(root, definition.Initialization.Source)
		if sourceErr != nil {
			return fmt.Errorf("%w: %v", ErrInitialize, sourceErr)
		}
		data, sourceErr = readBounded(source)
		if sourceErr != nil {
			return fmt.Errorf("%w: seed is unavailable", ErrInitialize)
		}
		err = nil
	}
	if err != nil {
		return err
	}
	updated, _, err := transformForDefinition(definition, data, replacements)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrParse, err)
	}
	if err = ensureSafeParent(root, target); err != nil {
		return fmt.Errorf("%w: target parent is unsafe", ErrInitialize)
	}
	if targetExisted {
		backupRelative := filepath.ToSlash(filepath.Join(".gamenode-backups", filepath.FromSlash(definition.Target+".previous")))
		backup, backupErr := safeTarget(root, backupRelative)
		if backupErr != nil || ensureSafeParent(root, backup) != nil {
			return fmt.Errorf("%w: backup path is unsafe", ErrApply)
		}
		if err = write(backup, data); err != nil {
			return fmt.Errorf("%w: backup could not be written", ErrApply)
		}
	}
	commit := write
	if !targetExisted {
		commit = create
	}
	if err = commit(target, updated); err != nil {
		return fmt.Errorf("%w: target could not be written", ErrApply)
	}
	return nil
}

func Read(root string, definition templates.ConfigAdapterDefinition) (map[string]string, error) {
	if err := ValidateDefinition(definition); err != nil {
		return nil, err
	}
	target, err := safeTarget(root, definition.Target)
	if err != nil {
		return nil, err
	}
	data, err := readBounded(target)
	if err != nil {
		return nil, err
	}
	_, found, err := transformForDefinition(definition, data, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParse, err)
	}
	result := map[string]string{}
	for _, field := range definition.Fields {
		value, ok := found[field.Property]
		if !ok {
			return nil, fmt.Errorf("%w: managed property is missing", ErrParse)
		}
		if err = validateValue(field, value); err != nil {
			return nil, fmt.Errorf("%w: managed property type is invalid", ErrParse)
		}
		result[field.Key] = value
	}
	return result, nil
}

func properties(definition templates.ConfigAdapterDefinition) map[string]bool {
	result := map[string]bool{}
	for _, field := range definition.Fields {
		result[field.Property] = true
	}
	return result
}

func transformForDefinition(definition templates.ConfigAdapterDefinition, data []byte, replacements map[string]string) ([]byte, map[string]string, error) {
	wanted := properties(definition)
	switch definition.Format {
	case "xml-properties":
		return transformXML(data, replacements, wanted)
	case "ini-key-values":
		return transformINI(data, replacements, wanted)
	case templates.FormatJSONKeyValues:
		return transformJSON(data, replacements, wanted)
	case templates.FormatINISectionKeyValues:
		return transformINISection(data, replacements, wanted, definition.Section)
	case sectionTupleFormat:
		return transformSectionTuple(data, replacements, definition.Fields, definition.Section, definition.ContainerProperty)
	default:
		return nil, nil, ErrUnsafeTarget
	}
}

func transformXML(data []byte, replacements map[string]string, wanted map[string]bool) ([]byte, map[string]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	var output bytes.Buffer
	encoder := xml.NewEncoder(&output)
	found := map[string]string{}
	depth, tokens := 0, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, errors.New("configuration XML is invalid")
		}
		tokens++
		if tokens > 100000 {
			return nil, nil, errors.New("configuration XML is too complex")
		}
		switch value := token.(type) {
		case xml.StartElement:
			depth++
			if depth > 32 {
				return nil, nil, errors.New("configuration XML is too deep")
			}
			if value.Name.Space == "" && value.Name.Local == "property" {
				name, valueIndex := "", -1
				for index := range value.Attr {
					attribute := &value.Attr[index]
					if attribute.Name.Space != "" {
						continue
					}
					if attribute.Name.Local == "name" {
						name = attribute.Value
					}
					if attribute.Name.Local == "value" {
						valueIndex = index
					}
				}
				if wanted[name] {
					if _, duplicate := found[name]; duplicate {
						return nil, nil, errors.New("configuration property is duplicated")
					}
					if valueIndex < 0 {
						return nil, nil, errors.New("configuration property has no value")
					}
					found[name] = value.Attr[valueIndex].Value
					if replacement, ok := replacements[name]; ok {
						value.Attr[valueIndex].Value = replacement
					}
				}
			}
			token = value
		case xml.EndElement:
			depth--
		case xml.ProcInst:
			if tokens != 1 || value.Target != "xml" || len(value.Inst) > 128 || strings.ContainsAny(string(value.Inst), "<>\x00\r\n") {
				return nil, nil, errors.New("configuration XML processing instructions are not supported")
			}
		case xml.Directive:
			return nil, nil, errors.New("configuration XML directives are not supported")
		}
		if err = encoder.EncodeToken(token); err != nil {
			return nil, nil, err
		}
	}
	if depth != 0 {
		return nil, nil, errors.New("configuration XML is unbalanced")
	}
	for name := range wanted {
		if _, ok := found[name]; !ok {
			return nil, nil, errors.New("configuration property is missing")
		}
	}
	if err := encoder.Flush(); err != nil {
		return nil, nil, err
	}
	return output.Bytes(), found, nil
}

// transformINI handles the flat key=value format used by Project Zomboid.
// It preserves comments, ordering, unknown keys, and the source line endings;
// sections and malformed lines fail closed instead of being guessed.
// transformJSON edits only declared top-level scalar properties. It deliberately
// does not implement JSONPath, nested traversal, array editing, or arbitrary
// expressions. Re-encoding the bounded document keeps unknown properties while
// normalizing whitespace; callers still commit through the normal atomic writer.
func transformJSON(data []byte, replacements map[string]string, wanted map[string]bool) ([]byte, map[string]string, error) {
	if len(data) > MaxConfigBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, nil, errors.New("configuration JSON is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var document map[string]json.RawMessage
	if err := decoder.Decode(&document); err != nil || document == nil {
		return nil, nil, errors.New("configuration JSON must be an object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, nil, errors.New("configuration JSON has trailing data")
	}
	found := map[string]string{}
	for key := range wanted {
		raw, ok := document[key]
		if !ok {
			return nil, nil, errors.New("configuration JSON property is missing")
		}
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || trimmed[0] == '{' || trimmed[0] == '[' {
			return nil, nil, errors.New("configuration JSON managed properties must be scalar")
		}
		var value any
		valueDecoder := json.NewDecoder(bytes.NewReader(trimmed))
		valueDecoder.UseNumber()
		if err := valueDecoder.Decode(&value); err != nil {
			return nil, nil, errors.New("configuration JSON property is invalid")
		}
		if err := valueDecoder.Decode(&extra); err != io.EOF {
			return nil, nil, errors.New("configuration JSON property is invalid")
		}
		switch typed := value.(type) {
		case string:
			found[key] = typed
		case json.Number:
			found[key] = typed.String()
		case bool:
			found[key] = strconv.FormatBool(typed)
		case nil:
			found[key] = ""
		default:
			return nil, nil, errors.New("configuration JSON managed property is not scalar")
		}
		if replacement, ok := replacements[key]; ok {
			var encoded []byte
			switch value.(type) {
			case string, nil:
				encoded, _ = json.Marshal(replacement)
			case json.Number:
				if _, err := strconv.ParseFloat(replacement, 64); err != nil || !json.Valid([]byte(replacement)) {
					return nil, nil, ErrInvalidValue
				}
				encoded = []byte(replacement)
			case bool:
				if replacement != "true" && replacement != "false" && replacement != "1" && replacement != "0" {
					return nil, nil, ErrInvalidValue
				}
				if replacement == "1" {
					replacement = "true"
				}
				if replacement == "0" {
					replacement = "false"
				}
				encoded = []byte(replacement)
			default:
				return nil, nil, ErrInvalidValue
			}
			document[key] = encoded
			found[key] = replacement
		}
	}
	updated, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, nil, errors.New("configuration JSON could not be encoded")
	}
	updated = append(updated, '\n')
	return updated, found, nil
}

// transformINISection edits declared scalar key/value properties in exactly
// one INI section. Other sections and unknown keys are preserved verbatim.
func transformINISection(data []byte, replacements map[string]string, wanted map[string]bool, section string) ([]byte, map[string]string, error) {
	if len(data) > MaxConfigBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, nil, errors.New("configuration INI is not valid UTF-8")
	}
	text := string(data)
	hasBOM := strings.HasPrefix(text, "\ufeff")
	if hasBOM {
		text = strings.TrimPrefix(text, "\ufeff")
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 100000 {
		return nil, nil, errors.New("configuration INI is too complex")
	}
	found := map[string]string{}
	current := ""
	foundSection := false
	for index, line := range lines {
		hasCR := strings.HasSuffix(line, "\r")
		raw := strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			if len(trimmed) < 3 || !strings.HasSuffix(trimmed, "]") {
				return nil, nil, errors.New("configuration INI section is invalid")
			}
			current = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			if !sectionName.MatchString(current) {
				return nil, nil, errors.New("configuration INI section is invalid")
			}
			if current == section {
				foundSection = true
			}
			continue
		}
		if current != section {
			continue
		}
		equals := strings.IndexByte(raw, '=')
		if equals <= 0 {
			return nil, nil, errors.New("configuration INI line is invalid")
		}
		key := strings.TrimSpace(raw[:equals])
		if !propertyName.MatchString(key) {
			return nil, nil, errors.New("configuration INI key is invalid")
		}
		if !wanted[key] {
			continue
		}
		if _, duplicate := found[key]; duplicate {
			return nil, nil, errors.New("configuration property is duplicated")
		}
		found[key] = raw[equals+1:]
		if replacement, ok := replacements[key]; ok {
			line = raw[:equals+1] + replacement
			if hasCR {
				line += "\r"
			}
			lines[index] = line
			found[key] = replacement
		}
	}
	if !foundSection {
		return nil, nil, errors.New("configuration INI section is missing")
	}
	for name := range wanted {
		if _, ok := found[name]; !ok {
			return nil, nil, errors.New("configuration property is missing")
		}
	}
	result := strings.Join(lines, "\n")
	if hasBOM {
		result = "\ufeff" + result
	}
	return []byte(result), found, nil
}

func transformINI(data []byte, replacements map[string]string, wanted map[string]bool) ([]byte, map[string]string, error) {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, nil, errors.New("configuration INI is not valid UTF-8")
	}
	text := string(data)
	hasBOM := strings.HasPrefix(text, "\ufeff")
	if hasBOM {
		text = strings.TrimPrefix(text, "\ufeff")
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 100000 {
		return nil, nil, errors.New("configuration INI is too complex")
	}
	found := map[string]string{}
	for index, line := range lines {
		hasCR := strings.HasSuffix(line, "\r")
		raw := strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			return nil, nil, errors.New("configuration INI sections are not supported")
		}
		equals := strings.IndexByte(raw, '=')
		if equals <= 0 {
			return nil, nil, errors.New("configuration INI line is invalid")
		}
		key := strings.TrimSpace(raw[:equals])
		if !propertyName.MatchString(key) {
			return nil, nil, errors.New("configuration INI key is invalid")
		}
		if !wanted[key] {
			continue
		}
		if _, duplicate := found[key]; duplicate {
			return nil, nil, errors.New("configuration property is duplicated")
		}
		found[key] = raw[equals+1:]
		if replacement, ok := replacements[key]; ok {
			line = raw[:equals+1] + replacement
			if hasCR {
				line += "\r"
			}
			lines[index] = line
		}
	}
	for name := range wanted {
		if _, ok := found[name]; !ok {
			return nil, nil, errors.New("configuration property is missing")
		}
	}
	result := strings.Join(lines, "\n")
	if hasBOM {
		result = "\ufeff" + result
	}
	return []byte(result), found, nil
}

func validateValue(field templates.ConfigAdapterField, value string) error {
	if value == "" {
		if field.Nullable || !field.Required {
			return nil
		}
		return ErrInvalidValue
	}
	if strings.ContainsRune(value, 0) || len(value) > 16<<10 {
		return ErrInvalidValue
	}
	switch field.Type {
	case "integer":
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return ErrInvalidValue
		}
		if field.Validation.Min != nil && float64(number) < *field.Validation.Min {
			return ErrInvalidValue
		}
		if field.Validation.Max != nil && float64(number) > *field.Validation.Max {
			return ErrInvalidValue
		}
	case "number":
		number, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return ErrInvalidValue
		}
		if field.Validation.Min != nil && number < *field.Validation.Min {
			return ErrInvalidValue
		}
		if field.Validation.Max != nil && number > *field.Validation.Max {
			return ErrInvalidValue
		}
	case "boolean":
		if value != "true" && value != "false" && value != "1" && value != "0" {
			return ErrInvalidValue
		}
	case "enum":
		valid := false
		for _, allowed := range field.Validation.Allowed {
			if value == allowed {
				valid = true
			}
		}
		if !valid {
			return ErrInvalidValue
		}
	}
	if field.Validation.MinLength != nil && len(value) < *field.Validation.MinLength {
		return ErrInvalidValue
	}
	if field.Validation.MaxLength != nil && len(value) > *field.Validation.MaxLength {
		return ErrInvalidValue
	}
	return nil
}

func safeTarget(root, name string) (string, error) {
	cleanRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", ErrUnsafeTarget
	}
	target := filepath.Join(cleanRoot, name)
	relative, err := filepath.Rel(cleanRoot, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", ErrUnsafeTarget
	}
	if resolvedRoot, resolveErr := filepath.EvalSymlinks(cleanRoot); resolveErr == nil {
		if resolvedParent, parentErr := filepath.EvalSymlinks(filepath.Dir(target)); parentErr == nil {
			relative, err = filepath.Rel(resolvedRoot, resolvedParent)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
				return "", ErrUnsafeTarget
			}
		}
		if resolvedTarget, resolveErr := filepath.EvalSymlinks(target); resolveErr == nil {
			relative, err = filepath.Rel(resolvedRoot, resolvedTarget)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
				return "", ErrUnsafeTarget
			}
		}
	}
	return target, nil
}

// ensureSafeParent creates missing target directories one component at a time.
// Existing symlinks and reparse points are resolved at every step and must stay
// below the canonical server root before creation continues.
func ensureSafeParent(root, target string) error {
	cleanRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return ErrUnsafeTarget
	}
	resolvedRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		return ErrUnsafeTarget
	}
	parent := filepath.Dir(target)
	relative, err := filepath.Rel(cleanRoot, parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return ErrUnsafeTarget
	}
	current := cleanRoot
	if relative == "." {
		return nil
	}
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if err = os.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return ErrUnsafeTarget
			}
		} else if statErr != nil || !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return ErrUnsafeTarget
		}
		resolved, resolveErr := filepath.EvalSymlinks(current)
		if resolveErr != nil || !pathWithin(resolvedRoot, resolved) {
			return ErrUnsafeTarget
		}
		resolvedInfo, statErr := os.Stat(resolved)
		if statErr != nil || !resolvedInfo.IsDir() {
			return ErrUnsafeTarget
		}
	}
	return nil
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func readBounded(name string) ([]byte, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxConfigBytes {
		return nil, errors.New("configuration file exceeds size limit")
	}
	return data, nil
}

func atomicWrite(name string, data []byte) error {
	directory := filepath.Dir(name)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".gamenode-config-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(temporaryName, name); err == nil {
		return nil
	}
	previous := name + ".replace"
	_ = os.Remove(previous)
	if err = os.Rename(name, previous); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err = os.Rename(temporaryName, name); err != nil {
		_ = os.Rename(previous, name)
		return err
	}
	_ = os.Remove(previous)
	return nil
}

// atomicCreate publishes a fully written same-directory temporary file without
// replacing a target that appeared concurrently after the missing-file check.
func atomicCreate(name string, data []byte) error {
	directory := filepath.Dir(name)
	temporary, err := os.CreateTemp(directory, ".gamenode-config-create-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Link(temporaryName, name)
}
