package templates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	CatalogSchemaVersion  = 1
	TemplateSchemaVersion = 2
	MaxCatalogBytes       = 256 << 10
	MaxOfficialBytes      = 256 << 10
	MaxAdapterBytes       = 128 << 10
	OfficialCatalogBase   = "https://raw.githubusercontent.com/Nikolai-Ahlhelm/GameNode/main/templates/"
)

var (
	ErrCatalogUnavailable   = errors.New("official template catalog is unavailable")
	ErrUnsupportedSchema    = errors.New("unsupported template schema")
	identifierPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	versionPattern          = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
	officialVariablePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
)

type CatalogManifest struct {
	SchemaVersion int            `json:"schema_version"`
	Templates     []CatalogEntry `json:"templates"`
}

type CatalogEntry struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	Description           string   `json:"description"`
	Category              string   `json:"category"`
	Version               string   `json:"version"`
	TemplateSchemaVersion int      `json:"template_schema_version"`
	Platforms             []string `json:"platforms"`
	Installer             string   `json:"installer"`
	File                  string   `json:"file"`
	Tags                  []string `json:"tags"`
	Icon                  string   `json:"icon,omitempty"`
	MinimumGameNode       string   `json:"minimum_gamenode_version,omitempty"`
}

type CatalogStatus struct {
	Source           string    `json:"source"`
	FetchedAt        time.Time `json:"fetched_at,omitempty"`
	Cached           bool      `json:"cached"`
	Offline          bool      `json:"offline"`
	LastError        string    `json:"last_error,omitempty"`
	InvalidTemplates int       `json:"invalid_templates"`
}

type CatalogResult struct {
	SchemaVersion int           `json:"schema_version"`
	Templates     []Template    `json:"templates"`
	Status        CatalogStatus `json:"status"`
}

type CatalogSource interface {
	FetchCatalog(context.Context) ([]byte, error)
	FetchTemplate(context.Context, string) ([]byte, error)
}

type HTTPSource struct {
	base   *url.URL
	client *http.Client
}

func NewOfficialHTTPSource() *HTTPSource {
	base, _ := url.Parse(OfficialCatalogBase)
	client := &http.Client{Timeout: 8 * time.Second}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 || request.URL.Scheme != "https" || !strings.EqualFold(request.URL.Host, base.Host) {
			return errors.New("catalog redirect rejected")
		}
		return nil
	}
	return &HTTPSource{base: base, client: client}
}

func NewHTTPSource(baseURL string, client *http.Client) (*HTTPSource, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("catalog source must be an HTTPS base URL")
	}
	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 || request.URL.Scheme != "https" || !strings.EqualFold(request.URL.Host, base.Host) {
			return errors.New("catalog redirect rejected")
		}
		return nil
	}
	return &HTTPSource{base: base, client: &copyClient}, nil
}

func (s *HTTPSource) FetchCatalog(ctx context.Context) ([]byte, error) {
	return s.fetch(ctx, "catalog.json", MaxCatalogBytes)
}

func (s *HTTPSource) FetchTemplate(ctx context.Context, name string) ([]byte, error) {
	if err := validateRelativeFile(name); err != nil {
		return nil, err
	}
	return s.fetch(ctx, name, MaxOfficialBytes)
}

func (s *HTTPSource) fetch(ctx context.Context, relative string, limit int64) ([]byte, error) {
	target := *s.base
	target.Path = strings.TrimSuffix(s.base.Path, "/") + "/" + relative
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog HTTP status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("catalog response exceeds size limit")
	}
	return data, nil
}

type CatalogManager struct {
	source         CatalogSource
	cacheDirectory string
	currentVersion string
	now            func() time.Time
	refreshMu      sync.Mutex
	mu             sync.RWMutex
	items          map[string]Template
	status         CatalogStatus
	log            *slog.Logger
}

func NewCatalogManager(source CatalogSource, dataDirectory, currentVersion string) *CatalogManager {
	m := &CatalogManager{source: source, cacheDirectory: filepath.Join(filepath.Clean(dataDirectory), "templates", "cache"), currentVersion: currentVersion, now: func() time.Time { return time.Now().UTC() }, items: map[string]Template{}, status: CatalogStatus{Source: "none"}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if items, fetched, err := m.readCache(); err == nil {
		m.items = items
		m.status = CatalogStatus{Source: "cache", FetchedAt: fetched, Cached: true}
	}
	return m
}

func (m *CatalogManager) SetLogger(log *slog.Logger) {
	if log == nil {
		return
	}
	m.mu.Lock()
	m.log = log
	items, source := len(m.items), m.status.Source
	m.mu.Unlock()
	m.log.Info("official game catalog initialized", "module", "Templates.Catalog", "source", source, "templates", items)
}

func (m *CatalogManager) List() CatalogResult {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Template, 0, len(m.items))
	for _, item := range m.items {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return CatalogResult{SchemaVersion: CatalogSchemaVersion, Templates: items, Status: m.status}
}

func (m *CatalogManager) Get(id string) (Template, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	item, ok := m.items[id]
	return item, ok
}

func (m *CatalogManager) Refresh(ctx context.Context) (CatalogResult, error) {
	m.log.Info("official game catalog refresh started", "module", "Templates.Catalog")
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	manifestData, err := m.source.FetchCatalog(ctx)
	if err != nil {
		m.log.Error("official game catalog manifest download failed", "module", "Templates.Catalog", "error", err)
		return m.failed(err)
	}
	manifest, err := decodeCatalog(manifestData)
	if err != nil {
		m.log.Error("official game catalog manifest validation failed", "module", "Templates.Catalog", "error", err)
		return m.failed(err)
	}
	items := make(map[string]Template, len(manifest.Templates))
	validEntries := make([]CatalogEntry, 0, len(manifest.Templates))
	invalid := 0
	for _, entry := range manifest.Templates {
		m.log.Debug("official game template download started", "module", "Templates.Catalog", "template_id", entry.ID, "version", entry.Version)
		data, fetchErr := m.source.FetchTemplate(ctx, entry.File)
		if fetchErr != nil {
			m.log.Warn("official game template download failed", "module", "Templates.Catalog", "template_id", entry.ID, "error", fetchErr)
			invalid++
			continue
		}
		template, decodeErr := decodeOfficial(data, entry, m.currentVersion)
		if decodeErr != nil {
			m.log.Warn("official game template validation failed", "module", "Templates.Catalog", "template_id", entry.ID, "error", decodeErr)
			invalid++
			continue
		}
		template.ResolvedAdapters = m.fetchAdapters(ctx, entry.File, template)
		if len(template.ResolvedAdapters) < adapterReferenceCount(template) {
			if previous, ok := m.Get(template.ID); ok {
				template.ResolvedAdapters = retainCompatibleAdapters(template, previous.ResolvedAdapters, template.ResolvedAdapters)
			}
		}
		items[template.ID] = template
		m.log.Debug("official game template loaded", "module", "Templates.Catalog", "template_id", entry.ID, "version", entry.Version, "adapters", len(template.ResolvedAdapters))
		validEntries = append(validEntries, entry)
	}
	if len(manifest.Templates) > 0 && len(items) == 0 {
		return m.failed(errors.New("catalog contains no valid templates"))
	}
	manifest.Templates = validEntries
	fetched := m.now()
	if err = m.writeCache(manifest, items, fetched); err != nil {
		m.log.Error("official game catalog cache update failed", "module", "Templates.Catalog", "error", err)
		return m.failed(err)
	}
	m.mu.Lock()
	m.items = items
	m.status = CatalogStatus{Source: "remote", FetchedAt: fetched, InvalidTemplates: invalid}
	result := m.listLocked()
	m.mu.Unlock()
	m.log.Info("official game catalog refresh completed", "module", "Templates.Catalog", "templates", len(items), "invalid_templates", invalid)
	return result, nil
}

func adapterReferenceCount(template Template) int {
	if template.Configuration == nil {
		return 0
	}
	return len(template.Configuration.Adapters)
}

func retainCompatibleAdapters(template Template, previous, fetched []ConfigAdapterDefinition) []ConfigAdapterDefinition {
	seen := map[string]bool{}
	for _, adapter := range fetched {
		seen[adapter.ID] = true
	}
	for _, reference := range template.Configuration.Adapters {
		if seen[reference.ID] {
			continue
		}
		for _, adapter := range previous {
			if adapter.ID != reference.ID {
				continue
			}
			data, _ := json.Marshal(adapter)
			if validated, err := decodeConfigAdapter(data, reference, template); err == nil {
				fetched = append(fetched, validated)
				seen[reference.ID] = true
			}
		}
	}
	return fetched
}

func (m *CatalogManager) fetchAdapters(ctx context.Context, templateFile string, template Template) []ConfigAdapterDefinition {
	if template.Configuration == nil {
		return nil
	}
	result := make([]ConfigAdapterDefinition, 0, len(template.Configuration.Adapters))
	for _, reference := range template.Configuration.Adapters {
		relative := path.Join(path.Dir(templateFile), reference.File)
		data, err := m.source.FetchTemplate(ctx, relative)
		if err != nil || len(data) > MaxAdapterBytes {
			m.log.Warn("official game configuration adapter download failed", "module", "Templates.Catalog", "template_id", template.ID, "adapter_id", reference.ID, "error", err)
			continue
		}
		adapter, err := decodeConfigAdapter(data, reference, template)
		if err == nil {
			result = append(result, adapter)
		} else {
			m.log.Warn("official game configuration adapter validation failed", "module", "Templates.Catalog", "template_id", template.ID, "adapter_id", reference.ID, "error", err)
		}
	}
	return result
}

func (m *CatalogManager) failed(err error) (CatalogResult, error) {
	m.mu.Lock()
	m.status.Offline = true
	m.status.LastError = "Official catalog refresh failed"
	if len(m.items) > 0 {
		m.status.Source = "cache"
		m.status.Cached = true
	}
	result := m.listLocked()
	m.mu.Unlock()
	return result, fmt.Errorf("%w: %v", ErrCatalogUnavailable, err)
}

func (m *CatalogManager) listLocked() CatalogResult {
	items := make([]Template, 0, len(m.items))
	for _, item := range m.items {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return CatalogResult{SchemaVersion: CatalogSchemaVersion, Templates: items, Status: m.status}
}

func decodeCatalog(data []byte) (CatalogManifest, error) {
	if len(data) == 0 || len(data) > MaxCatalogBytes {
		return CatalogManifest{}, errors.New("catalog exceeds size limit")
	}
	var manifest CatalogManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return CatalogManifest{}, errors.New("catalog JSON is invalid")
	}
	if manifest.SchemaVersion != CatalogSchemaVersion {
		return CatalogManifest{}, ErrUnsupportedSchema
	}
	seen := map[string]bool{}
	for _, entry := range manifest.Templates {
		if !identifierPattern.MatchString(entry.ID) || !identifierPattern.MatchString(entry.Category) || strings.TrimSpace(entry.Name) == "" || !versionPattern.MatchString(entry.Version) || (entry.MinimumGameNode != "" && !versionPattern.MatchString(entry.MinimumGameNode)) || !supportedTemplateSchema(entry.TemplateSchemaVersion) || validateRelativeFile(entry.File) != nil || seen[entry.ID] || len(entry.Platforms) == 0 {
			return CatalogManifest{}, errors.New("catalog entry is invalid")
		}
		for _, platform := range entry.Platforms {
			if platform != "windows" && platform != "linux" {
				return CatalogManifest{}, errors.New("catalog platform is unsupported")
			}
		}
		if entry.Installer != InstallerExisting && entry.Installer != InstallerExistingFiles && entry.Installer != InstallerSteamCMD {
			return CatalogManifest{}, errors.New("catalog installer is unsupported")
		}
		seen[entry.ID] = true
	}
	return manifest, nil
}

func decodeOfficial(data []byte, entry CatalogEntry, currentVersion string) (Template, error) {
	if len(data) == 0 || len(data) > MaxOfficialBytes {
		return Template{}, errors.New("template exceeds size limit")
	}
	var template Template
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&template); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Template{}, validationError(CodeSchemaInvalid, "template JSON is invalid")
	}
	if !supportedTemplateSchema(template.SchemaVersion) || template.SchemaVersion != entry.TemplateSchemaVersion {
		return Template{}, fmt.Errorf("%w: %w", ErrUnsupportedSchema, validationError(CodeUnsupportedVersion, "template schema version is unsupported"))
	}
	if template.SchemaVersion >= 2 && !v2VariableShapesComplete(data) {
		return Template{}, validationError(CodeSchemaInvalid, "template variable schema fields are missing")
	}
	if template.ID != entry.ID || template.Name != entry.Name || template.Description != entry.Description || template.Version != entry.Version || template.Category != entry.Category || template.Installer.Type != entry.Installer || template.SourceType != SourceOfficial || !template.ReadOnly {
		return Template{}, errors.New("template does not match catalog metadata")
	}
	if template.SchemaVersion >= 2 && !equalStrings(template.Tags, entry.Tags) {
		return Template{}, errors.New("template tags do not match catalog metadata")
	}
	template.SourceType = SourceOfficial
	template.SourceIdentifier = entry.ID
	template.SourceFormatVersion = strconv.Itoa(template.SchemaVersion)
	template.ReadOnly = true
	template.Platforms = append([]string(nil), entry.Platforms...)
	template.SourceMetadata.Tags = append([]string(nil), entry.Tags...)
	template.Tags = append([]string(nil), entry.Tags...)
	template.Icon = entry.Icon
	template.MinimumGameNode = entry.MinimumGameNode
	if err := validateOfficial(template); err != nil {
		var validation *ValidationError
		if errors.As(err, &validation) {
			return Template{}, err
		}
		return Template{}, fmt.Errorf("%w: %s", validationError(CodeSchemaInvalid, "template semantic validation failed"), err.Error())
	}
	if template.MinimumGameNode != "" && !versionAtLeast(currentVersion, template.MinimumGameNode) {
		template.Compatibility.Status = Unsupported
		template.Compatibility.Findings = append(template.Compatibility.Findings, Finding{SeverityError, "requirements", "GAMENODE_VERSION_UNSUPPORTED", "This template requires a newer GameNode version."})
	}
	return template, nil
}

func v2VariableShapesComplete(data []byte) bool {
	var raw struct {
		Variables []map[string]json.RawMessage `json:"variables"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return false
	}
	for _, variable := range raw.Variables {
		if _, ok := variable["validation"]; !ok {
			return false
		}
	}
	return true
}

func validateOfficial(template Template) error {
	if !identifierPattern.MatchString(template.ID) || strings.TrimSpace(template.Description) == "" || !versionPattern.MatchString(template.Version) || len(template.Variables) > MaxVariables || len(template.Compatibility.Findings) > MaxFindings {
		return errors.New("official template metadata is invalid")
	}
	if template.Compatibility.Status != Compatible && template.Compatibility.Status != PartiallyCompatible && template.Compatibility.Status != Unsupported {
		return errors.New("official template compatibility is invalid")
	}
	if len(template.SourceMetadata.DockerImages) != 0 || template.SourceMetadata.OriginalHash != "" {
		return errors.New("official templates cannot define container images or hashes")
	}
	known := map[string]bool{}
	definitions := map[string]TemplateVariable{}
	for _, variable := range template.Variables {
		if !officialVariablePattern.MatchString(variable.Key) || known[variable.Key] {
			return validationError(CodeInvalidVariable, "official template variable is invalid")
		}
		if err := validateVariableDefinition(variable); err != nil {
			return err
		}
		known[variable.Key] = true
		definitions[variable.Key] = variable
	}
	if template.Installer.Type == InstallerExisting {
		if template.Launch == nil || len(template.PlatformLaunches) != 0 {
			return validationError(CodeInvalidPlatformLaunch, "legacy template launch shape is invalid")
		}
		if err := validateOfficialLaunch(*template.Launch, known); err != nil {
			return err
		}
		if err := validateLaunchSensitive(*template.Launch, definitions); err != nil {
			return err
		}
		if template.Launch.Resolver != "neoforge" || template.Launch.Executable != "java" {
			return errors.New("legacy existing installer requires the native NeoForge resolver")
		}
	} else if template.Installer.Type == InstallerExistingFiles {
		if template.Installer.SteamCMD != nil || (template.Launch == nil) == (len(template.PlatformLaunches) == 0) {
			return validationError(CodeUnsupportedInstaller, "existing-files installer launch shape is invalid")
		}
		if template.Launch != nil {
			if err := validateOfficialLaunch(*template.Launch, known); err != nil {
				return err
			}
			if template.Launch.Resolver != "neoforge" && template.Launch.Resolver != "java" {
				return validationError(CodeInvalidPlatformLaunch, "existing-files resolver launch is invalid")
			}
			if err := validateLaunchSensitive(*template.Launch, definitions); err != nil {
				return err
			}
		} else {
			for _, platform := range template.Platforms {
				launch, ok := template.PlatformLaunches[platform]
				if !ok || launch.Resolver != "" {
					return validationError(CodeInvalidPlatformLaunch, "existing-files platform launch is invalid")
				}
				if err := validateOfficialLaunch(launch, known); err != nil {
					return err
				}
				if err := validateLaunchSensitive(launch, definitions); err != nil {
					return err
				}
			}
		}
	} else if template.Installer.Type == InstallerSteamCMD {
		plan := template.Installer.SteamCMD
		if plan == nil || plan.AppID <= 0 || plan.LoginMode != "anonymous" || plan.InstallTarget != "server_root" || plan.Platform != "native" || plan.UsernameVariable != "" || plan.PasswordVariable != "" || plan.AuthVariable != "" || plan.PlatformVariable != "" || plan.BetaPasswordVariable != "" || template.Launch != nil || len(template.PlatformLaunches) == 0 {
			return errors.New("SteamCMD installer is invalid")
		}
		if plan.BetaBranchVariable != "" {
			variable, ok := definitions[plan.BetaBranchVariable]
			if !ok || !variable.UserEditable || variable.Sensitive || (variable.Type != "string" && variable.Type != "enum") {
				return errors.New("SteamCMD beta branch variable is invalid")
			}
		}
		declared := map[string]bool{}
		for _, platform := range template.Platforms {
			launch, ok := template.PlatformLaunches[platform]
			if !ok || declared[platform] {
				return validationError(CodeInvalidPlatformLaunch, "SteamCMD platform launch is missing or duplicated")
			}
			if err := validateOfficialLaunch(launch, known); err != nil {
				return err
			}
			if err := validateLaunchSensitive(launch, definitions); err != nil {
				return err
			}
			declared[platform] = true
		}
		for platform := range template.PlatformLaunches {
			if !declared[platform] || (platform != "windows" && platform != "linux") {
				return errors.New("SteamCMD platform launch is undeclared")
			}
		}
	} else {
		return validationError(CodeUnsupportedInstaller, "official installer is unsupported")
	}
	if template.SchemaVersion >= 2 && len(template.ExpectedFiles) == 0 {
		return validationError(CodeExpectedFileInvalid, "schema v2 templates must declare expected files")
	}
	if err := validateExpectedFiles(template.ExpectedFiles, known, template.Platforms); err != nil {
		return err
	}
	if err := validateConfigFiles(template.ConfigFiles, known); err != nil {
		return err
	}
	if err := validateRequirements(template.Requirements); err != nil {
		return err
	}
	if template.Help != nil && (len(template.Help.Summary) > 1024 || len(template.Help.Notes) > 32) {
		return validationError(CodeSchemaInvalid, "template help metadata is invalid")
	}
	if err := validateOfficialPorts(template.Ports, definitions); err != nil {
		return err
	}
	if err := validateConfigurationReferences(template.Configuration); err != nil {
		return err
	}
	return nil
}

func validateLaunchSensitive(launch LaunchDefinition, definitions map[string]TemplateVariable) error {
	if sensitivePlaceholder(launch.Executable, definitions) || sensitivePlaceholder(launch.WorkingDirectory, definitions) {
		return validationError(CodeInvalidPlatformLaunch, "sensitive variables are not permitted in launch paths")
	}
	for _, argument := range launch.Arguments {
		if sensitivePlaceholder(argument, definitions) {
			return validationError(CodeInvalidPlatformLaunch, "sensitive variables are not permitted in launch arguments")
		}
	}
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validateConfigurationReferences(configuration *ConfigurationDefinition) error {
	if configuration == nil {
		return nil
	}
	if len(configuration.Adapters) == 0 || len(configuration.Adapters) > 8 {
		return errors.New("official template configuration is invalid")
	}
	seen := map[string]bool{}
	for _, reference := range configuration.Adapters {
		if !identifierPattern.MatchString(reference.ID) || (reference.SchemaVersion != 1 && reference.SchemaVersion != AdapterSchemaVersion) || seen[reference.ID] || path.Base(reference.File) != reference.File || validateRelativeFile(reference.File) != nil {
			return errors.New("official template configuration reference is invalid")
		}
		seen[reference.ID] = true
	}
	return nil
}

func decodeConfigAdapter(data []byte, reference ConfigAdapterReference, template Template) (ConfigAdapterDefinition, error) {
	if len(data) == 0 || len(data) > MaxAdapterBytes {
		return ConfigAdapterDefinition{}, errors.New("configuration adapter exceeds size limit")
	}
	var adapter ConfigAdapterDefinition
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&adapter); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return ConfigAdapterDefinition{}, errors.New("configuration adapter JSON is invalid")
	}
	if (adapter.SchemaVersion != 1 && adapter.SchemaVersion != AdapterSchemaVersion) || adapter.SchemaVersion != reference.SchemaVersion || adapter.ID != reference.ID || !versionPattern.MatchString(adapter.Version) || len(adapter.Fields) == 0 || len(adapter.Fields) > 128 {
		return ConfigAdapterDefinition{}, errors.New("configuration adapter metadata is invalid")
	}
	tupleFormat := adapter.Format == FormatSectionTuple
	managedLaunch := adapter.Format == FormatManagedLaunch
	if managedLaunch {
		// A managed-launch adapter owns no file. It must not declare a target,
		// a seeded initialization, or the post-start file lifecycle.
		if adapter.SchemaVersion < AdapterSchemaVersion || adapter.Target != "" || adapter.PostStartOnly || adapter.Initialization != nil {
			return ConfigAdapterDefinition{}, errors.New("configuration adapter launch shape is invalid")
		}
	} else {
		extension := strings.ToLower(path.Ext(adapter.Target))
		standardFormat := adapter.Format == FormatXMLProperties || adapter.Format == FormatINIKeyValues
		if (!standardFormat && !tupleFormat) || (adapter.Format == FormatXMLProperties && extension != ".xml") || ((adapter.Format == FormatINIKeyValues || tupleFormat) && extension != ".ini") || validateRelativeConfigTarget(adapter.Target) != nil {
			return ConfigAdapterDefinition{}, errors.New("configuration adapter target is unsafe")
		}
	}
	propertyPattern := regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
	sectionPattern := regexp.MustCompile(`^[A-Za-z0-9_./-]{1,160}$`)
	if tupleFormat {
		if !sectionPattern.MatchString(adapter.Section) || !propertyPattern.MatchString(adapter.ContainerProperty) {
			return ConfigAdapterDefinition{}, errors.New("configuration adapter container is invalid")
		}
	} else if adapter.Section != "" || adapter.ContainerProperty != "" {
		return ConfigAdapterDefinition{}, errors.New("configuration adapter container is invalid")
	}
	if adapter.Initialization != nil {
		if adapter.PostStartOnly || adapter.Initialization.Mode != "seed-from-file" || validateRelativeConfigTarget(adapter.Initialization.Source) != nil {
			return ConfigAdapterDefinition{}, errors.New("configuration adapter initialization is invalid")
		}
	}
	if adapter.PostStartOnly && adapter.Format != FormatINIKeyValues {
		return ConfigAdapterDefinition{}, errors.New("configuration adapter lifecycle is invalid")
	}
	variables := map[string]TemplateVariable{}
	for _, variable := range template.Variables {
		variables[variable.Key] = variable
	}
	properties, keys, targets := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, field := range adapter.Fields {
		variable, ok := variables[field.Key]
		if keys[field.Key] || !officialVariablePattern.MatchString(field.Key) || strings.TrimSpace(field.Label) == "" || len(field.Label) > 80 || len(field.Section) > 80 || validateAdapterField(field) != nil {
			return ConfigAdapterDefinition{}, errors.New("configuration adapter field is invalid")
		}
		if managedLaunch {
			if field.Property != "" || ValidateAdapterBinding(field) != nil {
				return ConfigAdapterDefinition{}, errors.New("configuration adapter binding is invalid")
			}
			// Reject a second field claiming the same argument or environment
			// name, and reject a setting that the base launch already supplies.
			if targets[bindingTarget(*field.Binding)] || launchReferencesKey(template, field.Key) {
				return ConfigAdapterDefinition{}, errors.New("configuration adapter binding is ambiguous")
			}
			targets[bindingTarget(*field.Binding)] = true
		} else if field.Binding != nil || properties[field.Property] || !propertyPattern.MatchString(field.Property) {
			return ConfigAdapterDefinition{}, errors.New("configuration adapter field is invalid")
		}
		if ok {
			if field.Type != variable.Type || field.Required != variable.Required || field.Nullable != variable.Nullable || field.Sensitive != variable.Sensitive || validateValue(TemplateVariable{Type: field.Type, Required: field.Required, Nullable: field.Nullable, Validation: field.Validation}, variable.DefaultValue) != nil {
				return ConfigAdapterDefinition{}, errors.New("configuration adapter field is invalid")
			}
		} else if !adapter.PostStartOnly {
			return ConfigAdapterDefinition{}, errors.New("configuration adapter field has no provisioning variable")
		}
		keys[field.Key], properties[field.Property] = true, true
	}
	return adapter, nil
}

var launchArgumentPattern = regexp.MustCompile(`^-{1,2}[A-Za-z][A-Za-z0-9-]{0,31}$`)

// BindingTarget identifies the technical slot a binding writes to. Two fields
// must never claim the same argument or environment name, whatever their
// binding types are.
func BindingTarget(binding ConfigAdapterBinding) string { return bindingTarget(binding) }

func bindingTarget(binding ConfigAdapterBinding) string {
	if binding.LaunchBinding() {
		return "argument " + binding.Argument
	}
	return "environment " + binding.Name
}

// ValidateAdapterBinding enforces the closed binding whitelist. Argument and
// environment names come only from reviewed adapter data, and each binding type
// is restricted to the field types it can represent safely. Both the catalog
// decoder and the persisted per-server snapshot are checked with this function.
func ValidateAdapterBinding(field ConfigAdapterField) error {
	binding := field.Binding
	if binding == nil {
		return errors.New("configuration adapter field has no binding")
	}
	switch binding.Type {
	case BindingLaunchValue, BindingLaunchFlag, BindingLaunchSecret:
		if !launchArgumentPattern.MatchString(binding.Argument) || binding.Name != "" {
			return errors.New("launch binding argument is invalid")
		}
	case BindingEnvironmentValue, BindingEnvironmentSecret:
		if !officialVariablePattern.MatchString(binding.Name) || binding.Argument != "" {
			return errors.New("environment binding name is invalid")
		}
	default:
		return errors.New("configuration adapter binding type is unsupported")
	}
	// A secret may reach argv or the environment only through the explicit
	// secret binding types, and only from a sensitive secret field.
	if binding.SecretBinding() != (field.Type == "secret") || binding.SecretBinding() != field.Sensitive {
		return errors.New("configuration adapter secret binding is invalid")
	}
	if binding.Type == BindingLaunchFlag && field.Type != "boolean" {
		return errors.New("launch flag binding requires a boolean field")
	}
	if binding.TrueValue != "" || binding.FalseValue != "" {
		if binding.Type != BindingLaunchValue || field.Type != "boolean" || binding.TrueValue == "" || binding.FalseValue == "" {
			return errors.New("boolean value mapping is invalid")
		}
		for _, mapped := range []string{binding.TrueValue, binding.FalseValue} {
			if len(mapped) > 64 || strings.ContainsAny(mapped, "\x00\r\n") {
				return errors.New("boolean value mapping is unsafe")
			}
		}
	}
	return nil
}

// launchReferencesKey reports whether a base launch already expands the given
// variable. A managed setting must have exactly one source of truth.
func launchReferencesKey(template Template, key string) bool {
	launches := make([]LaunchDefinition, 0, len(template.PlatformLaunches)+1)
	if template.Launch != nil {
		launches = append(launches, *template.Launch)
	}
	for _, launch := range template.PlatformLaunches {
		launches = append(launches, launch)
	}
	for _, launch := range launches {
		values := append([]string{launch.Executable, launch.WorkingDirectory}, launch.Arguments...)
		for _, value := range launch.Environment {
			values = append(values, value)
		}
		for _, value := range values {
			if strings.Contains(value, "{{"+key+"}}") || strings.Contains(value, "${"+key+"}") {
				return true
			}
		}
	}
	return false
}

func validateAdapterField(field ConfigAdapterField) error {
	if field.Required && field.Nullable {
		return errors.New("configuration adapter field cannot be required and nullable")
	}
	switch field.Type {
	case "string", "secret":
		if field.Validation.Min != nil || field.Validation.Max != nil || len(field.Validation.Allowed) != 0 {
			return errors.New("configuration adapter string validation is invalid")
		}
	case "integer", "number":
		if field.Validation.MinLength != nil || field.Validation.MaxLength != nil || len(field.Validation.Allowed) != 0 || (field.Validation.Min != nil && field.Validation.Max != nil && *field.Validation.Min > *field.Validation.Max) {
			return errors.New("configuration adapter numeric validation is invalid")
		}
	case "boolean":
		if field.Validation.Min != nil || field.Validation.Max != nil || field.Validation.MinLength != nil || field.Validation.MaxLength != nil || len(field.Validation.Allowed) != 0 {
			return errors.New("configuration adapter boolean validation is invalid")
		}
	case "enum":
		if len(field.Validation.Allowed) == 0 || field.Validation.Min != nil || field.Validation.Max != nil || field.Validation.MinLength != nil || field.Validation.MaxLength != nil {
			return errors.New("configuration adapter enum validation is invalid")
		}
	default:
		return errors.New("configuration adapter field type is invalid")
	}
	if field.Sensitive != (field.Type == "secret") {
		return errors.New("configuration adapter sensitivity is invalid")
	}
	if field.Validation.MinLength != nil && field.Validation.MaxLength != nil && *field.Validation.MinLength > *field.Validation.MaxLength {
		return errors.New("configuration adapter length validation is invalid")
	}
	return nil
}

func validateRelativeConfigTarget(value string) error {
	if strings.TrimSpace(value) != value || value == "" || len(value) > 240 || strings.Contains(value, `\`) || path.Clean(value) != value || strings.HasPrefix(value, "/") || filepath.IsAbs(value) {
		return errors.New("configuration target is unsafe")
	}
	segments := strings.Split(value, "/")
	if len(segments) > 8 {
		return errors.New("configuration target is too deep")
	}
	segmentPattern := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	for _, segment := range segments {
		if segment == "." || segment == ".." || !segmentPattern.MatchString(segment) {
			return errors.New("configuration target is unsafe")
		}
	}
	return nil
}

func validateOfficialLaunch(launch LaunchDefinition, known map[string]bool) error {
	if launch.WorkingRoot != "server_root" || strings.TrimSpace(launch.Executable) == "" || absoluteExecutable(launch.Executable) || forbiddenExecutables[strings.ToLower(filepath.Base(launch.Executable))] || len(launch.Arguments) > 128 {
		if forbiddenExecutables[strings.ToLower(filepath.Base(launch.Executable))] {
			return validationError(CodeShellSemanticsForbidden, "shell and command interpreters are forbidden")
		}
		return validationError(CodeInvalidPlatformLaunch, "official template launch definition is unsafe")
	}
	if err := validateTemplatePath(launch.Executable, known); err != nil && launch.Resolver == "" {
		return validationError(CodeInvalidPath, "launch executable path is unsafe")
	}
	if launch.WorkingDirectory != "" {
		if err := validateTemplatePath(launch.WorkingDirectory, known); err != nil {
			return errors.New("official template working directory is unsafe")
		}
	}
	if launch.StopMethod != "" && launch.StopMethod != "terminate" && launch.StopMethod != "stdin_command" {
		return errors.New("official template stop method is unsupported")
	}
	if launch.StopMethod == "stdin_command" && !safeStopCommand(launch.StopCommand) {
		return errors.New("official template stop command is unsafe")
	}
	if launch.StopMethod != "stdin_command" && launch.StopCommand != "" {
		return errors.New("official template stop command requires stdin")
	}
	if launch.StopTimeout < 0 || launch.StopTimeout > 300 {
		return errors.New("official template stop timeout is invalid")
	}
	values := append([]string{launch.Executable}, launch.Arguments...)
	for index, value := range values {
		javaClasspath := strings.Contains(value, ";") && index > 1 && (values[index-1] == "-cp" || values[index-1] == "-classpath") && safeWindowsJavaClasspath(value)
		if (index == 0 && unsafeLaunchPath(value)) || strings.ContainsAny(value, "\x00\r\n`") || strings.Contains(value, "$(") || (index > 0 && strings.Contains(value, ";") && (values[index-1] == "-cp" || values[index-1] == "-classpath") && !javaClasspath) {
			return errors.New("official template contains unsafe launch data")
		}
		if _, err := Expand(value, map[string]string{}, known); err != nil {
			return errors.New("official template contains invalid placeholders")
		}
	}
	if err := validateEnvironment(launch.Environment, known); err != nil {
		return err
	}
	return nil
}

// safeWindowsJavaClasspath permits only relative, non-empty path entries. The
// semicolon remains part of one argv element passed directly to Java; GameNode
// never interprets it as command syntax.
func safeWindowsJavaClasspath(value string) bool {
	entries := strings.Split(value, ";")
	if len(entries) < 2 || len(entries) > 32 {
		return false
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry) != entry || entry == "" || strings.ContainsAny(entry, "\x00\r\n&|><`") || strings.Contains(entry, "$(") {
			return false
		}
		if _, err := ExpandRelativePath(entry, map[string]string{}, map[string]bool{}); err != nil {
			return false
		}
	}
	return true
}

func validateOfficialPorts(items []TemplatePort, definitions map[string]TemplateVariable) error {
	if len(items) > 32 {
		return errors.New("official template defines too many ports")
	}
	seen := map[string]bool{}
	for _, item := range items {
		if strings.TrimSpace(item.Name) == "" || len(item.Name) > 64 || len(item.Purpose) > 256 || (item.Protocol != "tcp" && item.Protocol != "udp") || (item.Variable == "" && (item.Port < 1 || item.Port > 65535)) || (item.Variable != "" && item.Port != 0) {
			return errors.New("official template port is invalid")
		}
		if item.Variable != "" {
			variable, ok := definitions[item.Variable]
			if !ok || variable.Type != "integer" || !variable.UserEditable || variable.Sensitive || item.Offset < 0 || item.Offset > 100 {
				return errors.New("official template port variable is invalid")
			}
		}
		key := item.Protocol + ":" + item.Variable + ":" + strconv.Itoa(item.Port) + ":" + strconv.Itoa(item.Offset)
		if seen[key] {
			return errors.New("official template port is duplicated")
		}
		seen[key] = true
	}
	return nil
}

func validateRelativeFile(name string) error {
	if strings.TrimSpace(name) != name || name == "" || strings.Contains(name, "\\") {
		return errors.New("template file must be a relative slash path")
	}
	parsed, err := url.Parse(name)
	clean := filepath.ToSlash(filepath.Clean(name))
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || strings.HasPrefix(name, "/") || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != name || !strings.HasSuffix(strings.ToLower(name), ".json") {
		return errors.New("template file path is unsafe")
	}
	return nil
}

type cacheMetadata struct {
	FetchedAt time.Time `json:"fetched_at"`
}

func (m *CatalogManager) writeCache(manifest CatalogManifest, items map[string]Template, fetched time.Time) error {
	if err := os.MkdirAll(m.cacheDirectory, 0700); err != nil {
		return err
	}
	for _, entry := range manifest.Templates {
		item := items[entry.ID]
		data, _ := json.MarshalIndent(item, "", "  ")
		if err := atomicWrite(filepath.Join(m.cacheDirectory, "templates", filepath.FromSlash(entry.File)), data); err != nil {
			return err
		}
		if item.Configuration != nil {
			for _, reference := range item.Configuration.Adapters {
				for _, adapter := range item.ResolvedAdapters {
					if adapter.ID != reference.ID {
						continue
					}
					adapterData, _ := json.MarshalIndent(adapter, "", "  ")
					adapterPath := path.Join(path.Dir(entry.File), reference.File)
					if err := atomicWrite(filepath.Join(m.cacheDirectory, "templates", filepath.FromSlash(adapterPath)), adapterData); err != nil {
						return err
					}
				}
			}
		}
	}
	manifestData, _ := json.MarshalIndent(manifest, "", "  ")
	metadataData, _ := json.Marshal(cacheMetadata{FetchedAt: fetched})
	if err := atomicWrite(filepath.Join(m.cacheDirectory, "catalog.json"), manifestData); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(m.cacheDirectory, "metadata.json"), metadataData)
}

func (m *CatalogManager) readCache() (map[string]Template, time.Time, error) {
	manifestData, err := os.ReadFile(filepath.Join(m.cacheDirectory, "catalog.json"))
	if err != nil {
		return nil, time.Time{}, err
	}
	manifest, err := decodeCatalog(manifestData)
	if err != nil {
		return nil, time.Time{}, err
	}
	items := map[string]Template{}
	for _, entry := range manifest.Templates {
		data, readErr := os.ReadFile(filepath.Join(m.cacheDirectory, "templates", filepath.FromSlash(entry.File)))
		if readErr != nil {
			return nil, time.Time{}, readErr
		}
		item, decodeErr := decodeOfficial(data, entry, m.currentVersion)
		if decodeErr != nil {
			return nil, time.Time{}, decodeErr
		}
		if item.Configuration != nil {
			for _, reference := range item.Configuration.Adapters {
				adapterPath := path.Join(path.Dir(entry.File), reference.File)
				adapterData, adapterErr := os.ReadFile(filepath.Join(m.cacheDirectory, "templates", filepath.FromSlash(adapterPath)))
				if adapterErr != nil {
					continue
				}
				adapter, adapterErr := decodeConfigAdapter(adapterData, reference, item)
				if adapterErr == nil {
					item.ResolvedAdapters = append(item.ResolvedAdapters, adapter)
				}
			}
		}
		items[item.ID] = item
	}
	var metadata cacheMetadata
	data, err := os.ReadFile(filepath.Join(m.cacheDirectory, "metadata.json"))
	if err != nil || json.Unmarshal(data, &metadata) != nil || metadata.FetchedAt.IsZero() {
		return nil, time.Time{}, errors.New("catalog cache metadata is invalid")
	}
	return items, metadata.FetchedAt, nil
}

func atomicWrite(name string, data []byte) error {
	directory := filepath.Dir(name)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".catalog-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(data)
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
	// Windows cannot rename over an existing file. Keep a recoverable previous
	// file while replacing it so a failed replacement never destroys last-good data.
	backup := name + ".previous"
	_ = os.Remove(backup)
	if err = os.Rename(name, backup); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err = os.Rename(temporaryName, name); err != nil {
		_ = os.Rename(backup, name)
		return err
	}
	_ = os.Remove(backup)
	return nil
}

func versionAtLeast(current, minimum string) bool {
	if current == "" || current == "dev" || strings.Contains(current, "devel") {
		return true
	}
	parse := func(value string) [3]int {
		value = strings.TrimPrefix(value, "v")
		if suffix := strings.IndexAny(value, "-+"); suffix >= 0 {
			value = value[:suffix]
		}
		parts := strings.Split(value, ".")
		var out [3]int
		for i := 0; i < len(parts) && i < 3; i++ {
			out[i], _ = strconv.Atoi(parts[i])
		}
		return out
	}
	a, b := parse(current), parse(minimum)
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return true
}
