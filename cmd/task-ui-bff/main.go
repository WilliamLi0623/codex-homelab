package main

import (
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const defaultUIListenAddress = "127.0.0.1:8081"
const maxUIRequestBody = 1 << 20

type uiConfig struct {
	ListenAddress          string
	ControllerURL          *url.URL
	OperatorToken          string
	ControllerServiceToken string
}

func main() {
	listen := flag.String("listen", "", "private UI BFF listen address")
	controller := flag.String("controller", "", "Controller URL")
	operatorToken := flag.String("operator-token", "", "operator Bearer token")
	serviceToken := flag.String("controller-token", "", "optional Controller service token")
	flag.Parse()
	config, err := loadUIConfig(func(name string) string { return os.Getenv(name) }, *listen, *controller, *operatorToken, *serviceToken)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: config.ListenAddress, Handler: newUIHandler(config), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 0, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	log.Printf("task UI BFF listening on %s", config.ListenAddress)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func loadUIConfig(getenv func(string) string, flagListen, flagController, flagOperatorToken, flagServiceToken string) (uiConfig, error) {
	listen := strings.TrimSpace(flagListen)
	if listen == "" {
		listen = strings.TrimSpace(getenv("TASK_UI_LISTEN"))
	}
	if listen == "" {
		listen = defaultUIListenAddress
	}
	if err := validatePrivateAddress(listen); err != nil {
		return uiConfig{}, err
	}
	controllerRaw := strings.TrimSpace(flagController)
	if controllerRaw == "" {
		controllerRaw = strings.TrimSpace(getenv("TASK_UI_CONTROLLER_URL"))
	}
	controllerURL, err := url.Parse(controllerRaw)
	if err != nil || (controllerURL.Scheme != "http" && controllerURL.Scheme != "https") || controllerURL.Host == "" {
		return uiConfig{}, fmt.Errorf("TASK_UI_CONTROLLER_URL must be an HTTP(S) URL")
	}
	controllerURL.Path = strings.TrimRight(controllerURL.Path, "/")
	operator := strings.TrimSpace(flagOperatorToken)
	if operator == "" {
		operator = strings.TrimSpace(getenv("TASK_UI_AUTH_TOKEN"))
	}
	if operator == "" {
		return uiConfig{}, fmt.Errorf("TASK_UI_AUTH_TOKEN is required")
	}
	service := strings.TrimSpace(flagServiceToken)
	if service == "" {
		service = strings.TrimSpace(getenv("TASK_UI_CONTROLLER_TOKEN"))
	}
	return uiConfig{ListenAddress: listen, ControllerURL: controllerURL, OperatorToken: operator, ControllerServiceToken: service}, nil
}

func validatePrivateAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" {
		return fmt.Errorf("UI BFF listen address must include a private host and port")
	}
	switch strings.Trim(host, "[]") {
	case "0.0.0.0", "::", "*":
		return fmt.Errorf("UI BFF must use a private listener")
	}
	return nil
}

func newUIHandler(config uiConfig) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !authorizedUIRequest(request, config.OperatorToken) {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			writeUIError(writer, http.StatusUnauthorized, "UI authorization required")
			return
		}
		upstreamPath, ok := allowlistedControllerPath(request.Method, request.URL.Path)
		if !ok {
			writeUIError(writer, http.StatusNotFound, "UI route not found")
			return
		}
		proxyController(writer, request, config, upstreamPath)
	})
}

func authorizedUIRequest(request *http.Request, expected string) bool {
	const prefix = "Bearer "
	value := request.Header.Get("Authorization")
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	provided := strings.TrimSpace(strings.TrimPrefix(value, prefix))
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func allowlistedControllerPath(method, path string) (string, bool) {
	if !strings.HasPrefix(path, "/ui/api") {
		return "", false
	}
	controllerPath := strings.TrimPrefix(path, "/ui/api")
	if controllerPath == "/tasks" && (method == http.MethodGet || method == http.MethodPost) {
		return "/v1/tasks", true
	}
	if !strings.HasPrefix(controllerPath, "/tasks/") {
		return "", false
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(controllerPath, "/tasks/"), "/"), "/")
	if len(parts) == 1 && method == http.MethodGet {
		return "/v1/tasks/" + parts[0], true
	}
	if len(parts) == 2 && parts[1] == "messages" && (method == http.MethodGet || method == http.MethodPost) {
		return "/v1/tasks/" + parts[0] + "/messages", true
	}
	if len(parts) == 2 && parts[1] == "events" && method == http.MethodGet {
		return "/v1/tasks/" + parts[0] + "/events", true
	}
	if len(parts) == 3 && parts[1] == "events" && parts[2] == "stream" && method == http.MethodGet {
		return "/v1/tasks/" + parts[0] + "/events/stream", true
	}
	if len(parts) == 2 && method == http.MethodPost && (parts[1] == "attempts" || parts[1] == "turns" || parts[1] == "cancel" || parts[1] == "retry") {
		return "/v1/tasks/" + parts[0] + "/" + parts[1], true
	}
	return "", false
}

func proxyController(writer http.ResponseWriter, request *http.Request, config uiConfig, upstreamPath string) {
	body := io.Reader(http.NoBody)
	if request.Body != nil {
		body = http.MaxBytesReader(writer, request.Body, maxUIRequestBody)
	}
	upstreamURL := *config.ControllerURL
	upstreamURL.Path = strings.TrimRight(config.ControllerURL.Path, "/") + upstreamPath
	upstreamURL.RawQuery = request.URL.RawQuery
	upstreamRequest, err := http.NewRequestWithContext(request.Context(), request.Method, upstreamURL.String(), body)
	if err != nil {
		writeUIError(writer, http.StatusBadGateway, "Controller request could not be created")
		return
	}
	if contentType := request.Header.Get("Content-Type"); contentType != "" {
		upstreamRequest.Header.Set("Content-Type", contentType)
	}
	if lastEventID := request.Header.Get("Last-Event-ID"); lastEventID != "" {
		upstreamRequest.Header.Set("Last-Event-ID", lastEventID)
	}
	if config.ControllerServiceToken != "" {
		upstreamRequest.Header.Set("Authorization", "Bearer "+config.ControllerServiceToken)
	}
	response, err := http.DefaultClient.Do(upstreamRequest)
	if err != nil {
		writeUIError(writer, http.StatusBadGateway, "Controller is unavailable")
		return
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		writeUIError(writer, response.StatusCode, "Controller operation failed")
		return
	}
	for _, header := range []string{"Content-Type", "Cache-Control", "Connection", "Last-Event-ID"} {
		if value := response.Header.Get(header); value != "" {
			writer.Header().Set(header, value)
		}
	}
	writer.WriteHeader(response.StatusCode)
	_, _ = io.Copy(writer, response.Body)
}

func writeUIError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = fmt.Fprintf(writer, "{\"error\":%q}\n", message)
}
