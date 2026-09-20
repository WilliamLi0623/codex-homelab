package modelrouter

import (
	"errors"
	"fmt"
)

type Profile string

const (
	ProfileFullCoding Profile = "full-coding"
	ProfileWorker     Profile = "cheap-worker"
)

type ProfileState struct {
	Enabled bool
	Healthy bool
}

type Model struct{ ID string }

var priority = map[Profile][]string{
	ProfileFullCoding: {"openai-primary", "muse-spark-1.3-contributor", "glm-5.3-flash"},
	ProfileWorker:     {"glm-5.3-flash"},
}

type Router struct {
	states map[string]ProfileState
	next   map[Profile]int
	bad    map[Profile]map[string]bool
}

func New(states map[string]ProfileState) *Router {
	copyStates := make(map[string]ProfileState, len(states))
	for id, state := range states {
		copyStates[id] = state
	}
	return &Router{states: copyStates, next: make(map[Profile]int), bad: make(map[Profile]map[string]bool)}
}

const ErrNoAvailableProfile = "no_available_profile"
const ErrUnknownModel = "unknown_model"

type RouteError struct {
	Profile Profile
	Code    string
}

func (e *RouteError) Error() string {
	return fmt.Sprintf("modelrouter: %s for profile %s", e.Code, e.Profile)
}

func (r *Router) Next(profile Profile) (Model, error) {
	ordered := priority[profile]
	for i := r.next[profile]; i < len(ordered); i++ {
		id := ordered[i]
		if r.available(profile, id) {
			r.next[profile] = i + 1
			return Model{ID: id}, nil
		}
	}
	return Model{}, &RouteError{Profile: profile, Code: ErrNoAvailableProfile}
}

func (r *Router) available(profile Profile, id string) bool {
	state := r.states[id]
	return state.Enabled && state.Healthy && !r.bad[profile][id]
}

func (r *Router) ReportFailure(profile Profile, modelID string, err error) error {
	ordered, ok := priority[profile]
	if !ok {
		return &RouteError{Profile: profile, Code: ErrUnknownModel}
	}
	modelIndex := -1
	for i, id := range ordered {
		if id == modelID {
			modelIndex = i
			break
		}
	}
	if modelIndex < 0 {
		return &RouteError{Profile: profile, Code: ErrUnknownModel}
	}
	if _, ok := r.states[modelID]; !ok {
		return &RouteError{Profile: profile, Code: ErrUnknownModel}
	}
	if err == nil {
		return nil
	}
	if r.next[profile] != modelIndex+1 {
		return nil
	}
	if MayFailover(err) {
		if r.bad[profile] == nil {
			r.bad[profile] = make(map[string]bool)
		}
		r.bad[profile][modelID] = true
	} else {
		if r.next[profile] == modelIndex+1 {
			r.next[profile] = modelIndex
		}
	}
	return nil
}

type FailureClass string

const (
	FailureGateway    FailureClass = "gateway"
	FailureProvider   FailureClass = "provider"
	FailureQuota      FailureClass = "quota"
	FailureModel      FailureClass = "model"
	FailureValidation FailureClass = "validation"
)

type failure struct {
	class   FailureClass
	message string
}

func (e failure) Error() string { return e.message }

func GatewayError(message string) error    { return failure{FailureGateway, message} }
func ProviderError(message string) error   { return failure{FailureProvider, message} }
func QuotaError(message string) error      { return failure{FailureQuota, message} }
func ModelError(message string) error      { return failure{FailureModel, message} }
func ValidationError(message string) error { return failure{FailureValidation, message} }

func Classify(err error) FailureClass {
	var e failure
	if errors.As(err, &e) {
		return e.class
	}
	return FailureValidation
}

func MayFailover(err error) bool {
	switch Classify(err) {
	case FailureProvider, FailureQuota, FailureModel:
		return true
	default:
		return false
	}
}
