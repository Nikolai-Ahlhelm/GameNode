package gameconfig

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gamenode/internal/templates"
)

// MaxResolvedArguments and MaxResolvedEnvironment bound the runtime launch that
// managed configuration may produce. The base launch is already bounded by
// servers.Server.Validate; these limits keep the expanded result bounded too.
const (
	MaxResolvedArguments   = 256
	MaxResolvedEnvironment = 128
	MaxManagedValueBytes   = 4096
)

// ErrIncomplete is returned when a required managed setting has no configured
// value. Starting with a partial launch would silently produce a differently
// configured game server, so resolution fails closed instead.
var ErrIncomplete = errors.New("managed game configuration is incomplete")

// storedValues loads the persisted typed values for one adapter. Values are
// returned raw for internal use; callers that build API responses must drop
// sensitive values themselves.
func (s *Service) storedValues(ctx context.Context, serverID, adapterID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT field_key,value FROM server_config_values WHERE server_id=? AND adapter_id=? ORDER BY field_key`, serverID, adapterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var key, value string
		if err = rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		result[key] = value
	}
	return result, rows.Err()
}

// applyManagedValues validates and upserts the supplied subset of fields. Keys
// that were not submitted stay untouched, so an unsent secret is preserved.
func (s *Service) applyManagedValues(ctx context.Context, serverID string, definition templates.ConfigAdapterDefinition, values map[string]string) error {
	fields := map[string]templates.ConfigAdapterField{}
	for _, field := range definition.Fields {
		fields[field.Key] = field
	}
	accepted := make(map[string]templates.ConfigAdapterField, len(values))
	for key, value := range values {
		field, ok := fields[key]
		if !ok {
			return fmt.Errorf("%w: unknown field", ErrInvalidValue)
		}
		if err := validateManagedValue(field, value); err != nil {
			return err
		}
		accepted[key] = field
	}
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for key, field := range accepted {
		value := values[key]
		if value == "" && !field.Required {
			if _, err = transaction.ExecContext(ctx, `DELETE FROM server_config_values WHERE server_id=? AND adapter_id=? AND field_key=?`, serverID, definition.ID, key); err != nil {
				return err
			}
			continue
		}
		if _, err = transaction.ExecContext(ctx, `INSERT INTO server_config_values(server_id,adapter_id,field_key,value,sensitive,created_at,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(server_id,adapter_id,field_key) DO UPDATE SET value=excluded.value, sensitive=excluded.sensitive, updated_at=excluded.updated_at`, serverID, definition.ID, key, value, field.Sensitive, now, now); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

// validateManagedValue applies the declared field validation and the additional
// argv/environment safety rules. A user value must always remain exactly one
// argv element or one environment value.
func validateManagedValue(field templates.ConfigAdapterField, value string) error {
	if err := validateValue(field, value); err != nil {
		return err
	}
	if len(value) > MaxManagedValueBytes {
		return ErrInvalidValue
	}
	if strings.ContainsAny(value, "\x00\r\n") {
		return ErrInvalidValue
	}
	return nil
}

// ResolveLaunch expands the persisted base launch with managed configuration.
// It returns a new argument slice and environment map; nothing is persisted and
// secret values exist only for the lifetime of this call and the child process.
func (s *Service) ResolveLaunch(ctx context.Context, serverID string, arguments []string, environment map[string]string) ([]string, map[string]string, error) {
	definitions, err := s.definitions(ctx, serverID)
	if err != nil {
		return nil, nil, err
	}
	resolvedArguments := make([]string, 0, len(arguments)+8)
	resolvedArguments = append(resolvedArguments, arguments...)
	resolvedEnvironment := make(map[string]string, len(environment)+8)
	for key, value := range environment {
		resolvedEnvironment[key] = value
	}
	for _, definition := range definitions {
		if !ManagedLaunch(definition) {
			continue
		}
		values, valueErr := s.storedValues(ctx, serverID, definition.ID)
		if valueErr != nil {
			return nil, nil, valueErr
		}
		for _, field := range definition.Fields {
			value := values[field.Key]
			if value == "" {
				if field.Required && !field.Nullable {
					// The key name is safe to report; the value is not needed.
					return nil, nil, fmt.Errorf("%w: %s is not configured", ErrIncomplete, field.Key)
				}
				continue
			}
			if err = validateManagedValue(field, value); err != nil {
				return nil, nil, fmt.Errorf("%w: %s is invalid", ErrInvalidValue, field.Key)
			}
			binding := field.Binding
			switch binding.Type {
			case templates.BindingLaunchFlag:
				if managedBoolean(value) {
					resolvedArguments = append(resolvedArguments, binding.Argument)
				}
			case templates.BindingLaunchValue, templates.BindingLaunchSecret:
				// Exactly two argv elements: the reviewed argument name from the
				// adapter and the user value as one single element.
				resolvedArguments = append(resolvedArguments, binding.Argument, managedArgumentValue(field, value))
			case templates.BindingEnvironmentValue, templates.BindingEnvironmentSecret:
				resolvedEnvironment[binding.Name] = value
			default:
				return nil, nil, ErrUnsafeTarget
			}
		}
	}
	if len(resolvedArguments) > MaxResolvedArguments || len(resolvedEnvironment) > MaxResolvedEnvironment {
		return nil, nil, fmt.Errorf("%w: resolved launch is too large", ErrInvalidValue)
	}
	return resolvedArguments, resolvedEnvironment, nil
}

// managedArgumentValue applies the reviewed boolean value mapping when one is
// declared and otherwise passes the stored value through unchanged.
func managedArgumentValue(field templates.ConfigAdapterField, value string) string {
	binding := field.Binding
	if field.Type != "boolean" || binding.TrueValue == "" {
		return value
	}
	if managedBoolean(value) {
		return binding.TrueValue
	}
	return binding.FalseValue
}

func managedBoolean(value string) bool { return value == "true" || value == "1" }

// InitialValues selects the provisioning-time values for a managed-launch
// adapter from already validated template values.
func InitialValues(definition templates.ConfigAdapterDefinition, values map[string]string) ([]ManagedValue, error) {
	if err := ValidateDefinition(definition); err != nil {
		return nil, err
	}
	result := make([]ManagedValue, 0, len(definition.Fields))
	for _, field := range definition.Fields {
		value, ok := values[field.Key]
		if !ok || value == "" {
			continue
		}
		if err := validateManagedValue(field, value); err != nil {
			return nil, err
		}
		result = append(result, ManagedValue{Key: field.Key, Value: value, Sensitive: field.Sensitive})
	}
	return result, nil
}

// ManagedValue is one initial typed value handed to the server store during
// transactional registration.
type ManagedValue struct {
	Key       string
	Value     string
	Sensitive bool
}
