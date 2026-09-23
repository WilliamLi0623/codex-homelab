package modelrouter

import "testing"

func validRoutes() RouteConfig {
	return RouteConfig{
		OpenAI: RouteSettings{Provider: "openai", Model: "gpt-6-luna", WireAPI: WireAPIResponses, ReasoningEffort: "high", BaseURL: "https://api.openai.com/v1", SecretName: "openai-model-gateway", SecretKey: "api-key"},
		Spark:  RouteSettings{Provider: "cch", Model: "muse-spark-1.3-contributor", WireAPI: WireAPIResponses, ReasoningEffort: "xhigh", BaseURL: "https://cch-jp.zenkexi.com/v1", SecretName: "cch-model-gateway", SecretKey: "api-key"},
		GLM:    RouteSettings{Provider: "cch", Model: "glm-5.3-flash", WireAPI: WireAPIChatCompletions, ReasoningEffort: "max", BaseURL: "https://cch-jp.zenkexi.com/v1", SecretName: "cch-model-gateway", SecretKey: "api-key"},
	}
}

func TestResolveFixedRoleRoutes(t *testing.T) {
	cases := []struct {
		name     string
		mode     Mode
		role     Role
		provider string
		model    string
		wireAPI  WireAPI
		effort   string
	}{
		{"normal orchestrator", ModeNormal, RoleOrchestrator, "openai", "gpt-6-luna", WireAPIResponses, "high"},
		{"normal worker", ModeNormal, RoleWorker, "openai", "gpt-6-luna", WireAPIResponses, "high"},
		{"fallback orchestrator", ModeQuotaFallback, RoleOrchestrator, "cch", "muse-spark-1.3-contributor", WireAPIResponses, "xhigh"},
		{"fallback worker", ModeQuotaFallback, RoleWorker, "cch", "glm-5.3-flash", WireAPIChatCompletions, "max"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.mode, tc.role, validRoutes())
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got.Provider != tc.provider || got.Model != tc.model || got.WireAPI != tc.wireAPI || got.ReasoningEffort != tc.effort {
				t.Fatalf("Resolve() = %+v; want provider/model/wire/effort %q/%q/%q/%q", got, tc.provider, tc.model, tc.wireAPI, tc.effort)
			}
			if got.BaseURL == "" || got.SecretName == "" || got.SecretKey == "" {
				t.Fatalf("Resolve() lost endpoint or secret reference: %+v", got)
			}
			if got.Mode != tc.mode || got.Role != tc.role {
				t.Fatalf("Resolve() identity = %s/%s; want %s/%s", got.Mode, got.Role, tc.mode, tc.role)
			}
		})
	}
}

func TestResolveRejectsUnknownModeOrRole(t *testing.T) {
	for _, tc := range []struct {
		mode Mode
		role Role
	}{
		{Mode("unknown"), RoleWorker},
		{ModeNormal, Role("other")},
	} {
		if _, err := Resolve(tc.mode, tc.role, validRoutes()); err == nil {
			t.Fatalf("Resolve(%q, %q) unexpectedly succeeded", tc.mode, tc.role)
		}
	}
}

func TestResolveRejectsInvalidRouteConfiguration(t *testing.T) {
	cases := []struct {
		name string
		mode Mode
		role Role
		edit func(*RouteConfig)
	}{
		{"wrong GLM transport", ModeQuotaFallback, RoleWorker, func(c *RouteConfig) { c.GLM.WireAPI = WireAPIResponses }},
		{"wrong Spark transport", ModeQuotaFallback, RoleOrchestrator, func(c *RouteConfig) { c.Spark.WireAPI = WireAPIChatCompletions }},
		{"wrong reasoning effort", ModeQuotaFallback, RoleWorker, func(c *RouteConfig) { c.GLM.ReasoningEffort = "xhigh" }},
		{"wrong model", ModeQuotaFallback, RoleOrchestrator, func(c *RouteConfig) { c.Spark.Model = "unknown-model" }},
		{"wrong provider", ModeNormal, RoleWorker, func(c *RouteConfig) { c.OpenAI.Provider = "cch" }},
		{"invalid endpoint", ModeQuotaFallback, RoleWorker, func(c *RouteConfig) { c.GLM.BaseURL = "not a URL" }},
		{"missing endpoint", ModeNormal, RoleWorker, func(c *RouteConfig) { c.OpenAI.BaseURL = "" }},
		{"missing secret reference", ModeQuotaFallback, RoleOrchestrator, func(c *RouteConfig) { c.Spark.SecretName = "" }},
		{"secret-like secret name", ModeQuotaFallback, RoleWorker, func(c *RouteConfig) { c.GLM.SecretName = "Bearer abcdef0123456789" }},
		{"invalid dotted secret name", ModeQuotaFallback, RoleWorker, func(c *RouteConfig) { c.GLM.SecretName = "cch..model" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := validRoutes()
			tc.edit(&config)
			if _, err := Resolve(tc.mode, tc.role, config); err == nil {
				t.Fatal("Resolve() unexpectedly accepted invalid route configuration")
			}
		})
	}
}

func TestResolvedRouteContainsOnlySecretReference(t *testing.T) {
	got, err := Resolve(ModeQuotaFallback, RoleWorker, validRoutes())
	if err != nil {
		t.Fatal(err)
	}
	if got.SecretName != "cch-model-gateway" || got.SecretKey != "api-key" {
		t.Fatalf("secret reference = %q/%q", got.SecretName, got.SecretKey)
	}
}

func TestValidateResolvedRouteChecksFrozenPolicyAndGeneration(t *testing.T) {
	route, err := Resolve(ModeQuotaFallback, RoleWorker, validRoutes())
	if err != nil {
		t.Fatal(err)
	}
	route.Generation = 3
	if err := ValidateResolvedRoute(route); err != nil {
		t.Fatalf("ValidateResolvedRoute(valid) error = %v", err)
	}
	invalid := route
	invalid.Model = "muse-spark-1.3-contributor"
	if err := ValidateResolvedRoute(invalid); err == nil {
		t.Fatal("ValidateResolvedRoute accepted a worker route with the wrong model")
	}
	invalid = route
	invalid.Generation = 0
	if err := ValidateResolvedRoute(invalid); err == nil {
		t.Fatal("ValidateResolvedRoute accepted a zero generation")
	}
}

func TestValidateResolvedRouteAgainstConfigBindsEndpointAndSecretReference(t *testing.T) {
	config := validRoutes()
	route, err := Resolve(ModeQuotaFallback, RoleWorker, config)
	if err != nil {
		t.Fatal(err)
	}
	route.Generation = 2
	if err := ValidateResolvedRouteAgainstConfig(route, config); err != nil {
		t.Fatalf("valid configured route rejected: %v", err)
	}

	for name, mutate := range map[string]func(*ResolvedRoute){
		"endpoint":    func(r *ResolvedRoute) { r.BaseURL = "https://attacker.example/v1" },
		"secret name": func(r *ResolvedRoute) { r.SecretName = "attacker-secret" },
		"secret key":  func(r *ResolvedRoute) { r.SecretKey = "other-key" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := route
			mutate(&invalid)
			if err := ValidateResolvedRouteAgainstConfig(invalid, config); err == nil {
				t.Fatal("accepted route snapshot that differs from deployment configuration")
			}
		})
	}
}
