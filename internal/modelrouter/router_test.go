package modelrouter

import (
	"errors"
	"fmt"
	"testing"
)

func TestRouterFullCodingPriority(t *testing.T) {
	r := New(map[string]ProfileState{
		"openai-primary":             {Enabled: true, Healthy: true},
		"muse-spark-1.3-contributor": {Enabled: true, Healthy: true},
		"glm-5.3-flash":              {Enabled: true, Healthy: true},
	})

	for _, want := range []string{"openai-primary", "muse-spark-1.3-contributor", "glm-5.3-flash"} {
		got, err := r.Next(ProfileFullCoding)
		if err != nil || got.ID != want {
			t.Fatalf("Next() = %#v, %v; want %q", got, err, want)
		}
	}
}

func TestRouterWorkerUsesGLMFlash(t *testing.T) {
	r := New(map[string]ProfileState{"glm-5.3-flash": {Enabled: true, Healthy: true}})
	got, err := r.Next(ProfileWorker)
	if err != nil || got.ID != "glm-5.3-flash" {
		t.Fatalf("Next(worker) = %#v, %v; want glm-5.3-flash", got, err)
	}
}

func TestRouterSkipsDisabledAndUnhealthyAndFailsClosed(t *testing.T) {
	r := New(map[string]ProfileState{
		"openai-primary":             {Enabled: false, Healthy: true},
		"muse-spark-1.3-contributor": {Enabled: true, Healthy: false},
		"glm-5.3-flash":              {Enabled: false, Healthy: false},
	})
	_, err := r.Next(ProfileFullCoding)
	var routeErr *RouteError
	if !errors.As(err, &routeErr) || routeErr.Profile != ProfileFullCoding || routeErr.Code != ErrNoAvailableProfile {
		t.Fatalf("error = %v; want stable no-available typed error", err)
	}
}

func TestFailureClassificationAndFailoverPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want FailureClass
		ok   bool
	}{
		{"gateway outage", GatewayError("CC Hub unavailable"), FailureGateway, false},
		{"provider failure", ProviderError("model unavailable"), FailureProvider, true},
		{"quota failure", QuotaError("rate limited"), FailureQuota, true},
		{"model failure", ModelError("model rejected"), FailureModel, true},
		{"validation failure", ValidationError("invalid request"), FailureValidation, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := fmt.Errorf("wrapped: %w", tc.err)
			if got := Classify(wrapped); got != tc.want || MayFailover(wrapped) != tc.ok {
				t.Fatalf("Classify/MayFailover = %v/%v; want %v/%v", got, MayFailover(tc.err), tc.want, tc.ok)
			}
		})
	}
}

func TestValidationFailureDoesNotAdvanceRoute(t *testing.T) {
	r := New(map[string]ProfileState{
		"openai-primary":             {Enabled: true, Healthy: true},
		"muse-spark-1.3-contributor": {Enabled: true, Healthy: true},
	})
	first, err := r.Next(ProfileFullCoding)
	if err != nil || first.ID != "openai-primary" {
		t.Fatalf("first Next() = %#v, %v", first, err)
	}
	if err := r.ReportFailure(ProfileFullCoding, first.ID, ValidationError("bad input")); err != nil {
		t.Fatal(err)
	}
	next, err := r.Next(ProfileFullCoding)
	if err != nil || next.ID != first.ID {
		t.Fatalf("after validation failure Next() = %#v, %v; want same profile", next, err)
	}
}

func TestProviderFailureAdvancesButGatewayFailureDoesNot(t *testing.T) {
	r := New(map[string]ProfileState{
		"openai-primary":             {Enabled: true, Healthy: true},
		"muse-spark-1.3-contributor": {Enabled: true, Healthy: true},
	})
	first, _ := r.Next(ProfileFullCoding)
	if err := r.ReportFailure(ProfileFullCoding, first.ID, GatewayError("transport down")); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Next(ProfileFullCoding)
	if got.ID != first.ID {
		t.Fatalf("gateway failure advanced to %q; want %q", got.ID, first.ID)
	}
	if err := r.ReportFailure(ProfileFullCoding, got.ID, ProviderError("model unavailable")); err != nil {
		t.Fatal(err)
	}
	next, _ := r.Next(ProfileFullCoding)
	if next.ID != "muse-spark-1.3-contributor" {
		t.Fatalf("provider failure selected %q; want muse-spark-1.3-contributor", next.ID)
	}
}

func TestReportFailureIsProfileScopedAndDoesNotRegressOnDuplicateOrOutOfOrderReports(t *testing.T) {
	r := New(map[string]ProfileState{
		"openai-primary":             {Enabled: true, Healthy: true},
		"muse-spark-1.3-contributor": {Enabled: true, Healthy: true},
		"glm-5.3-flash":              {Enabled: true, Healthy: true},
	})
	fullFirst, _ := r.Next(ProfileFullCoding)
	workerFirst, _ := r.Next(ProfileWorker)
	if fullFirst.ID != "openai-primary" || workerFirst.ID != "glm-5.3-flash" {
		t.Fatalf("initial routes = %q/%q", fullFirst.ID, workerFirst.ID)
	}
	if err := r.ReportFailure(ProfileFullCoding, fullFirst.ID, ProviderError("down")); err != nil {
		t.Fatal(err)
	}
	fullSecond, _ := r.Next(ProfileFullCoding)
	if fullSecond.ID != "muse-spark-1.3-contributor" {
		t.Fatalf("full route = %q; want muse", fullSecond.ID)
	}
	if err := r.ReportFailure(ProfileFullCoding, fullFirst.ID, ProviderError("duplicate old report")); err != nil {
		t.Fatal(err)
	}
	if err := r.ReportFailure(ProfileFullCoding, "glm-5.3-flash", ProviderError("out of order")); err != nil {
		t.Fatal(err)
	}
	fullThird, _ := r.Next(ProfileFullCoding)
	if fullThird.ID != "glm-5.3-flash" {
		t.Fatalf("full route regressed/skipped = %q; want glm", fullThird.ID)
	}
	scoped := New(map[string]ProfileState{
		"openai-primary":             {Enabled: true, Healthy: true},
		"muse-spark-1.3-contributor": {Enabled: true, Healthy: true},
		"glm-5.3-flash":              {Enabled: true, Healthy: true},
	})
	_, _ = scoped.Next(ProfileFullCoding)
	_, _ = scoped.Next(ProfileFullCoding)
	if err := scoped.ReportFailure(ProfileFullCoding, "glm-5.3-flash", ProviderError("full only")); err != nil {
		t.Fatal(err)
	}
	workerAgain, err := scoped.Next(ProfileWorker)
	if err != nil || workerAgain.ID != "glm-5.3-flash" {
		t.Fatalf("worker route was affected by full report: %#v, %v", workerAgain, err)
	}
}

func TestUnknownModelReportsStableErrorAndDoesNotAdvance(t *testing.T) {
	r := New(map[string]ProfileState{"openai-primary": {Enabled: true, Healthy: true}})
	for _, tc := range []struct {
		profile Profile
		model   string
	}{{ProfileFullCoding, "not-configured"}, {ProfileWorker, "openai-primary"}} {
		err := r.ReportFailure(tc.profile, tc.model, ProviderError("bad"))
		var routeErr *RouteError
		if !errors.As(err, &routeErr) || routeErr.Code != ErrUnknownModel {
			t.Fatalf("ReportFailure(%q, %q) = %v; want stable unknown-model error", tc.profile, tc.model, err)
		}
	}
	got, err := r.Next(ProfileFullCoding)
	if err != nil || got.ID != "openai-primary" {
		t.Fatalf("unknown report changed route: %#v, %v", got, err)
	}
}

func TestProfilesExhaustWithStableNoAvailableError(t *testing.T) {
	r := New(map[string]ProfileState{"openai-primary": {Enabled: true, Healthy: true}, "muse-spark-1.3-contributor": {Enabled: true, Healthy: true}, "glm-5.3-flash": {Enabled: true, Healthy: true}})
	for i := 0; i < 3; i++ {
		if _, err := r.Next(ProfileFullCoding); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		_, err := r.Next(ProfileFullCoding)
		var routeErr *RouteError
		if !errors.As(err, &routeErr) || routeErr.Code != ErrNoAvailableProfile {
			t.Fatalf("full call %d = %v; want stable no-available", i+4, err)
		}
	}
	worker := New(map[string]ProfileState{"glm-5.3-flash": {Enabled: true, Healthy: true}})
	if _, err := worker.Next(ProfileWorker); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Next(ProfileWorker); err == nil || err.Error() != (&RouteError{Profile: ProfileWorker, Code: ErrNoAvailableProfile}).Error() {
		t.Fatalf("worker second call = %v; want stable no-available", err)
	}
}

func TestReportFailureMissingStateReturnsUnknownModelWithoutAdvancing(t *testing.T) {
	r := New(map[string]ProfileState{"openai-primary": {Enabled: true, Healthy: true}})
	err := r.ReportFailure(ProfileFullCoding, "muse-spark-1.3-contributor", ProviderError("missing state"))
	var routeErr *RouteError
	if !errors.As(err, &routeErr) || routeErr.Code != ErrUnknownModel {
		t.Fatalf("ReportFailure() = %v; want ErrUnknownModel", err)
	}
	got, err := r.Next(ProfileFullCoding)
	if err != nil || got.ID != "openai-primary" {
		t.Fatalf("missing-state report advanced route: %#v, %v", got, err)
	}
}

func TestQuotaAndModelFailuresAdvanceToNextCandidate(t *testing.T) {
	for _, failure := range []error{QuotaError("quota"), ModelError("model")} {
		r := New(map[string]ProfileState{
			"openai-primary":             {Enabled: true, Healthy: true},
			"muse-spark-1.3-contributor": {Enabled: true, Healthy: true},
		})
		first, err := r.Next(ProfileFullCoding)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.ReportFailure(ProfileFullCoding, first.ID, failure); err != nil {
			t.Fatal(err)
		}
		next, err := r.Next(ProfileFullCoding)
		if err != nil || next.ID != "muse-spark-1.3-contributor" {
			t.Fatalf("failure %T selected %#v, %v; want muse", failure, next, err)
		}
	}
}

func TestDisabledAndUnhealthyRemainFailClosedAcrossRepeatedNext(t *testing.T) {
	r := New(map[string]ProfileState{
		"openai-primary":             {Enabled: false, Healthy: true},
		"muse-spark-1.3-contributor": {Enabled: true, Healthy: false},
		"glm-5.3-flash":              {Enabled: false, Healthy: false},
	})
	for i := 0; i < 3; i++ {
		_, err := r.Next(ProfileFullCoding)
		var routeErr *RouteError
		if !errors.As(err, &routeErr) || routeErr.Code != ErrNoAvailableProfile {
			t.Fatalf("Next() call %d = %v; want stable no-available", i+1, err)
		}
	}
}
