package modelrouter

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Mode is the fixed account-quota state used to resolve a role route.
type Mode string

const (
	ModeNormal        Mode = "normal"
	ModeQuotaFallback Mode = "quota_fallback"
)

// Role identifies the kind of work, not a user-selectable model strategy.
type Role string

const (
	RoleOrchestrator Role = "orchestrator"
	RoleWorker       Role = "worker"
)

// WireAPI is the upstream protocol selected for a model route.
type WireAPI string

const (
	WireAPIResponses       WireAPI = "responses"
	WireAPIChatCompletions WireAPI = "chat-completions"
)

// RouteSettings contains non-secret route metadata and a reference to the
// Kubernetes Secret carrying credentials. Secret values never belong here.
type RouteSettings struct {
	Provider        string
	Model           string
	WireAPI         WireAPI
	ReasoningEffort string
	BaseURL         string
	SecretName      string
	SecretKey       string
}

// RouteConfig is deployment configuration, not a routing policy selector.
// Resolve accepts only the approved model/transport/effort tuple for the
// requested fixed mode and role.
type RouteConfig struct {
	OpenAI RouteSettings
	Spark  RouteSettings
	GLM    RouteSettings
}

// ResolvedRoute is a validated immutable snapshot suitable for persisting on
// one attempt. It contains secret references only, never credential values.
type ResolvedRoute struct {
	Mode            Mode
	Generation      int64
	Role            Role
	Provider        string
	Model           string
	WireAPI         WireAPI
	ReasoningEffort string
	BaseURL         string
	SecretName      string
	SecretKey       string
}

// ValidateResolvedRoute rechecks a persisted route snapshot against the fixed
// policy before it crosses the capacity or executor boundary.
func ValidateResolvedRoute(route ResolvedRoute) error {
	if route.Generation < 1 {
		return fmt.Errorf("route generation must be positive")
	}
	settings := RouteSettings{Provider: route.Provider, Model: route.Model, WireAPI: route.WireAPI, ReasoningEffort: route.ReasoningEffort, BaseURL: route.BaseURL, SecretName: route.SecretName, SecretKey: route.SecretKey}
	config := RouteConfig{}
	switch route.Mode {
	case ModeNormal:
		config.OpenAI = settings
	case ModeQuotaFallback:
		switch route.Role {
		case RoleOrchestrator:
			config.Spark = settings
		case RoleWorker:
			config.GLM = settings
		default:
			return fmt.Errorf("unknown routing role %q", route.Role)
		}
	default:
		return fmt.Errorf("unknown routing mode %q", route.Mode)
	}
	_, err := Resolve(route.Mode, route.Role, config)
	return err
}

// ValidateResolvedRouteAgainstConfig verifies both the fixed role policy and
// that mutable deployment-owned endpoint/Secret references still match the
// configuration that created the route snapshot.
func ValidateResolvedRouteAgainstConfig(route ResolvedRoute, config RouteConfig) error {
	if err := ValidateResolvedRoute(route); err != nil {
		return err
	}
	expected, err := Resolve(route.Mode, route.Role, config)
	if err != nil {
		return err
	}
	if route.Provider != expected.Provider || route.Model != expected.Model || route.WireAPI != expected.WireAPI || route.ReasoningEffort != expected.ReasoningEffort || route.BaseURL != expected.BaseURL || route.SecretName != expected.SecretName || route.SecretKey != expected.SecretKey {
		return fmt.Errorf("resolved route does not match configured provider endpoint and Secret reference")
	}
	return nil
}

var (
	secretNameLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)
	secretKeyPattern       = regexp.MustCompile(`^[A-Za-z0-9._-]{1,253}$`)
)

// Resolve returns the one approved route for mode and role. It does not
// perform health-based selection, retries, or provider failover.
func Resolve(mode Mode, role Role, config RouteConfig) (ResolvedRoute, error) {
	var settings RouteSettings
	var expected RouteSettings
	switch mode {
	case ModeNormal:
		if role != RoleOrchestrator && role != RoleWorker {
			return ResolvedRoute{}, fmt.Errorf("unknown routing role %q", role)
		}
		settings = config.OpenAI
		expected = RouteSettings{Provider: "openai", Model: "gpt-6-luna", WireAPI: WireAPIResponses, ReasoningEffort: "high"}
	case ModeQuotaFallback:
		switch role {
		case RoleOrchestrator:
			settings = config.Spark
			expected = RouteSettings{Provider: "cch", Model: "muse-spark-1.3-contributor", WireAPI: WireAPIResponses, ReasoningEffort: "xhigh"}
		case RoleWorker:
			settings = config.GLM
			expected = RouteSettings{Provider: "cch", Model: "glm-5.3-flash", WireAPI: WireAPIChatCompletions, ReasoningEffort: "max"}
		default:
			return ResolvedRoute{}, fmt.Errorf("unknown routing role %q", role)
		}
	default:
		return ResolvedRoute{}, fmt.Errorf("unknown routing mode %q", mode)
	}

	if err := validateSettings(settings, expected); err != nil {
		return ResolvedRoute{}, fmt.Errorf("invalid %s/%s route: %w", mode, role, err)
	}
	return ResolvedRoute{
		Mode: mode, Role: role, Provider: settings.Provider, Model: settings.Model,
		WireAPI: settings.WireAPI, ReasoningEffort: settings.ReasoningEffort,
		BaseURL: settings.BaseURL, SecretName: settings.SecretName, SecretKey: settings.SecretKey,
	}, nil
}

func validateSettings(settings, expected RouteSettings) error {
	if settings.Provider != expected.Provider || settings.Model != expected.Model || settings.WireAPI != expected.WireAPI || settings.ReasoningEffort != expected.ReasoningEffort {
		return fmt.Errorf("provider/model/wire/effort must be %q/%q/%q/%q", expected.Provider, expected.Model, expected.WireAPI, expected.ReasoningEffort)
	}
	u, err := url.Parse(settings.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("base URL must be an https URL without user info, query, or fragment")
	}
	if !validSecretName(settings.SecretName) {
		return fmt.Errorf("secret name is not a valid Kubernetes Secret name")
	}
	if !secretKeyPattern.MatchString(settings.SecretKey) || strings.TrimSpace(settings.SecretKey) != settings.SecretKey {
		return fmt.Errorf("secret key is not a valid Kubernetes Secret key")
	}
	return nil
}

func validSecretName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if !secretNameLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}
