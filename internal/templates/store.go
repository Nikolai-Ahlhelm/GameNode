package templates

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Create(ctx context.Context, template Template) (Template, error) {
	if template.Name == "" || template.SourceType != "pelican-pterodactyl" {
		return Template{}, errors.New("invalid normalized template")
	}
	id, err := newID()
	if err != nil {
		return Template{}, err
	}
	now := time.Now().UTC()
	template.ID = id
	template.CreatedAt = now
	template.UpdatedAt = now
	metadata, _ := json.Marshal(template.SourceMetadata)
	installer, _ := json.Marshal(template.Installer)
	var launch any
	if template.Launch != nil {
		encoded, _ := json.Marshal(template.Launch)
		launch = string(encoded)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Template{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO game_templates(id,name,description,source_type,source_identifier,source_format_version,source_metadata_json,installer_json,launch_json,compatibility_status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, template.ID, template.Name, template.Description, template.SourceType, template.SourceIdentifier, template.SourceFormatVersion, string(metadata), string(installer), launch, template.Compatibility.Status, stamp(now), stamp(now))
	if err != nil {
		return Template{}, fmt.Errorf("create template: %w", err)
	}
	for i, v := range template.Variables {
		validation, _ := json.Marshal(v.Validation)
		rules, _ := json.Marshal(v.RawRules)
		if _, err = tx.ExecContext(ctx, `INSERT INTO game_template_variables(template_id,position,name,description,variable_key,default_value,user_viewable,user_editable,variable_type,sensitive,required,nullable,validation_json,raw_rules_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, template.ID, i, v.Name, v.Description, v.Key, v.DefaultValue, v.UserViewable, v.UserEditable, v.Type, v.Sensitive, v.Required, v.Nullable, string(validation), string(rules)); err != nil {
			return Template{}, fmt.Errorf("create template variable: %w", err)
		}
	}
	for i, f := range template.Compatibility.Findings {
		if _, err = tx.ExecContext(ctx, `INSERT INTO game_template_findings(template_id,position,severity,component,code,summary) VALUES(?,?,?,?,?,?)`, template.ID, i, f.Severity, f.Component, f.Code, f.Summary); err != nil {
			return Template{}, fmt.Errorf("create compatibility finding: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return Template{}, err
	}
	return template, nil
}

func (s *Store) List(ctx context.Context) ([]Template, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM game_templates ORDER BY name COLLATE NOCASE,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	result := make([]Template, 0, len(ids))
	for _, id := range ids {
		template, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, template)
	}
	return result, nil
}

func (s *Store) Get(ctx context.Context, id string) (Template, error) {
	var result Template
	var metadata, installer string
	var launch sql.NullString
	var created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT id,name,description,source_type,source_identifier,source_format_version,source_metadata_json,installer_json,launch_json,compatibility_status,created_at,updated_at FROM game_templates WHERE id=?`, id).Scan(&result.ID, &result.Name, &result.Description, &result.SourceType, &result.SourceIdentifier, &result.SourceFormatVersion, &metadata, &installer, &launch, &result.Compatibility.Status, &created, &updated)
	if err != nil {
		return Template{}, err
	}
	if json.Unmarshal([]byte(metadata), &result.SourceMetadata) != nil || json.Unmarshal([]byte(installer), &result.Installer) != nil {
		return Template{}, errors.New("stored template data is invalid")
	}
	if launch.Valid {
		var definition LaunchDefinition
		if json.Unmarshal([]byte(launch.String), &definition) != nil {
			return Template{}, errors.New("stored launch data is invalid")
		}
		result.Launch = &definition
	}
	result.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	result.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	variables, err := s.db.QueryContext(ctx, `SELECT name,description,variable_key,default_value,user_viewable,user_editable,variable_type,sensitive,required,nullable,validation_json,raw_rules_json FROM game_template_variables WHERE template_id=? ORDER BY position`, id)
	if err != nil {
		return Template{}, err
	}
	defer variables.Close()
	for variables.Next() {
		var v TemplateVariable
		var validation, rules string
		if err = variables.Scan(&v.Name, &v.Description, &v.Key, &v.DefaultValue, &v.UserViewable, &v.UserEditable, &v.Type, &v.Sensitive, &v.Required, &v.Nullable, &validation, &rules); err != nil {
			return Template{}, err
		}
		if json.Unmarshal([]byte(validation), &v.Validation) != nil || json.Unmarshal([]byte(rules), &v.RawRules) != nil {
			return Template{}, errors.New("stored variable data is invalid")
		}
		result.Variables = append(result.Variables, v)
	}
	if err = variables.Err(); err != nil {
		return Template{}, err
	}
	findings, err := s.db.QueryContext(ctx, `SELECT severity,component,code,summary FROM game_template_findings WHERE template_id=? ORDER BY position`, id)
	if err != nil {
		return Template{}, err
	}
	defer findings.Close()
	for findings.Next() {
		var f Finding
		if err = findings.Scan(&f.Severity, &f.Component, &f.Code, &f.Summary); err != nil {
			return Template{}, err
		}
		result.Compatibility.Findings = append(result.Compatibility.Findings, f)
	}
	return result, findings.Err()
}

func (s *Store) Delete(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM game_templates WHERE id=?`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func stamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

type Service struct{ store *Store }

var ErrInvalidEgg = errors.New("invalid egg")

func NewService(store *Store) *Service { return &Service{store: store} }
func (s *Service) Analyze(data []byte) (Template, error) {
	template, err := AnalyzeEgg(data)
	if err != nil {
		return Template{}, fmt.Errorf("%w: %v", ErrInvalidEgg, err)
	}
	return template, nil
}
func (s *Service) Import(ctx context.Context, data []byte) (Template, error) {
	template, err := AnalyzeEgg(data)
	if err != nil {
		return Template{}, fmt.Errorf("%w: %v", ErrInvalidEgg, err)
	}
	return s.store.Create(ctx, template)
}
func (s *Service) List(ctx context.Context) ([]Template, error)         { return s.store.List(ctx) }
func (s *Service) Get(ctx context.Context, id string) (Template, error) { return s.store.Get(ctx, id) }
func (s *Service) Delete(ctx context.Context, id string) error          { return s.store.Delete(ctx, id) }
