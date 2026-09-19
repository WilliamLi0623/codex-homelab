package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const defaultEndpoint = "http://127.0.0.1:8080"

type task struct {
	ID             string `json:"id"`
	Repository     string `json:"repository"`
	BaseRef        string `json:"base_ref"`
	Objective      string `json:"objective"`
	ExecutionClass string `json:"execution_class"`
	State          string `json:"state"`
}

type attempt struct {
	ID           string `json:"id"`
	Number       int    `json:"number"`
	ModelProfile string `json:"model_profile"`
	State        string `json:"state"`
}

type event struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	CreatedAt string `json:"created_at"`
}

type options struct {
	values      map[string]string
	positionals []string
}

func main() {
	if err := run(os.Args[1:], os.Stdout, http.DefaultClient); err != nil {
		log.Fatal(err)
	}
}

func run(arguments []string, output io.Writer, client *http.Client) error {
	if len(arguments) == 0 {
		return fmt.Errorf("usage: codexctl <doctor|status|run|list|show|send|cancel|retry|reconcile|logs> [options]")
	}
	command := arguments[0]
	parsed, err := parseOptions(arguments[1:])
	if err != nil {
		return err
	}
	endpoint := parsed.values["endpoint"]
	if endpoint == "" {
		endpoint = defaultEndpoint
	}

	switch command {
	case "doctor":
		if err := validateOptions(command, parsed, 0, "endpoint"); err != nil {
			return err
		}
		return doctor(output, client, endpoint)
	case "status":
		if err := validateOptions(command, parsed, 0, "endpoint"); err != nil {
			return err
		}
		return controllerStatus(output, client, endpoint)
	case "run":
		if err := validateOptions(command, parsed, 0, "endpoint", "repository", "base-ref", "objective", "profile", "idempotency-key"); err != nil {
			return err
		}
		return runTask(output, client, endpoint, parsed)
	case "list":
		if err := validateOptions(command, parsed, 0, "endpoint"); err != nil {
			return err
		}
		return listTasks(output, client, endpoint)
	case "show":
		if err := validateOptions(command, parsed, 1, "endpoint"); err != nil {
			return err
		}
		return showTask(output, client, endpoint, requiredTaskID(parsed.positionals))
	case "send":
		if err := validateOptions(command, parsed, 1, "endpoint", "body"); err != nil {
			return err
		}
		return sendMessage(output, client, endpoint, requiredTaskID(parsed.positionals), parsed.values["body"])
	case "cancel":
		if err := validateOptions(command, parsed, 1, "endpoint"); err != nil {
			return err
		}
		return updateTask(output, client, endpoint, requiredTaskID(parsed.positionals), "cancel")
	case "retry":
		if err := validateOptions(command, parsed, 1, "endpoint"); err != nil {
			return err
		}
		return retryTask(output, client, endpoint, requiredTaskID(parsed.positionals))
	case "reconcile":
		if err := validateOptions(command, parsed, 2, "endpoint", "outcome"); err != nil {
			return err
		}
		return reconcileAttempt(output, client, endpoint, parsed.positionals[0], parsed.positionals[1], parsed.values["outcome"])
	case "logs":
		if err := validateOptions(command, parsed, 1, "endpoint"); err != nil {
			return err
		}
		return taskLogs(output, client, endpoint, requiredTaskID(parsed.positionals))
	case "nodes", "capacity", "providers":
		return fmt.Errorf("command %q is unsupported: Controller endpoint is not implemented", command)
	default:
		return fmt.Errorf("unsupported command %q", command)
	}
}

func parseOptions(arguments []string) (options, error) {
	result := options{values: make(map[string]string)}
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if !strings.HasPrefix(argument, "--") {
			result.positionals = append(result.positionals, argument)
			continue
		}
		name := strings.TrimPrefix(argument, "--")
		if index+1 >= len(arguments) || strings.HasPrefix(arguments[index+1], "--") {
			return options{}, fmt.Errorf("--%s requires a value", name)
		}
		index++
		result.values[name] = arguments[index]
	}
	return result, nil
}

func validateOptions(command string, parsed options, positionalCount int, allowedNames ...string) error {
	allowed := make(map[string]bool, len(allowedNames))
	for _, name := range allowedNames {
		allowed[name] = true
	}
	for name := range parsed.values {
		if !allowed[name] {
			return fmt.Errorf("%s: unknown option --%s", command, name)
		}
	}
	if len(parsed.positionals) != positionalCount {
		if positionalCount == 1 {
			return fmt.Errorf("%s requires exactly one task ID", command)
		}
		if positionalCount == 2 {
			return fmt.Errorf("%s requires a task ID and attempt ID", command)
		}
		return fmt.Errorf("%s does not accept positional arguments", command)
	}
	return nil
}

func requiredTaskID(positionals []string) string {
	if len(positionals) == 1 && strings.TrimSpace(positionals[0]) != "" {
		return positionals[0]
	}
	return ""
}

func doctor(output io.Writer, client *http.Client, endpoint string) error {
	var health struct {
		Status string `json:"status"`
	}
	if err := requestJSON(client, http.MethodGet, endpoint, "/v1/health", nil, &health); err != nil {
		return err
	}
	if health.Status != "ok" {
		return fmt.Errorf("controller health status is %q", health.Status)
	}
	_, err := fmt.Fprintln(output, "controller: ok")
	return err
}

func controllerStatus(output io.Writer, client *http.Client, endpoint string) error {
	var status struct {
		Controller string `json:"controller"`
		Tasks      int    `json:"tasks"`
	}
	if err := requestJSON(client, http.MethodGet, endpoint, "/v1/status", nil, &status); err != nil {
		return err
	}
	_, err := fmt.Fprintf(output, "controller: %s\ntasks: %d\n", status.Controller, status.Tasks)
	return err
}

func runTask(output io.Writer, client *http.Client, endpoint string, parsed options) error {
	required := []string{"repository", "objective", "idempotency-key"}
	for _, name := range required {
		if strings.TrimSpace(parsed.values[name]) == "" {
			return fmt.Errorf("--%s is required", name)
		}
	}
	baseRef := parsed.values["base-ref"]
	if baseRef == "" {
		baseRef = "main"
	}
	input := map[string]string{
		"repository": parsed.values["repository"], "base_ref": baseRef,
		"objective": parsed.values["objective"], "profile": parsed.values["profile"],
		"idempotency_key": parsed.values["idempotency-key"],
	}
	var response struct {
		Task task `json:"task"`
	}
	if err := requestJSON(client, http.MethodPost, endpoint, "/v1/tasks", input, &response); err != nil {
		return err
	}
	return printTask(output, response.Task)
}

func listTasks(output io.Writer, client *http.Client, endpoint string) error {
	var response struct {
		Tasks []task `json:"tasks"`
	}
	if err := requestJSON(client, http.MethodGet, endpoint, "/v1/tasks", nil, &response); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, "ID\tSTATE\tEXECUTION CLASS\tREPOSITORY\tBASE REF"); err != nil {
		return err
	}
	for _, item := range response.Tasks {
		if _, err := fmt.Fprintf(output, "%s\t%s\t%s\t%s\t%s\n", item.ID, item.State, item.ExecutionClass, item.Repository, item.BaseRef); err != nil {
			return err
		}
	}
	return nil
}

func showTask(output io.Writer, client *http.Client, endpoint, taskID string) error {
	if taskID == "" {
		return fmt.Errorf("show requires exactly one task ID")
	}
	var response struct {
		Task task `json:"task"`
	}
	if err := requestJSON(client, http.MethodGet, endpoint, taskPath(taskID), nil, &response); err != nil {
		return err
	}
	return printTask(output, response.Task)
}

func sendMessage(output io.Writer, client *http.Client, endpoint, taskID, body string) error {
	if taskID == "" {
		return fmt.Errorf("send requires exactly one task ID")
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("--body is required")
	}
	var response struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	}
	if err := requestJSON(client, http.MethodPost, endpoint, taskPath(taskID)+"/messages", map[string]string{"body": body}, &response); err != nil {
		return err
	}
	_, err := fmt.Fprintf(output, "message: %s\nrole: %s\n", response.ID, response.Role)
	return err
}

func updateTask(output io.Writer, client *http.Client, endpoint, taskID, action string) error {
	if taskID == "" {
		return fmt.Errorf("%s requires exactly one task ID", action)
	}
	var response struct {
		Task task `json:"task"`
	}
	if err := requestJSON(client, http.MethodPost, endpoint, taskPath(taskID)+"/"+action, nil, &response); err != nil {
		return err
	}
	return printTask(output, response.Task)
}

func retryTask(output io.Writer, client *http.Client, endpoint, taskID string) error {
	if taskID == "" {
		return fmt.Errorf("retry requires exactly one task ID")
	}
	var response struct {
		Task    task    `json:"task"`
		Attempt attempt `json:"attempt"`
	}
	if err := requestJSON(client, http.MethodPost, endpoint, taskPath(taskID)+"/retry", nil, &response); err != nil {
		return err
	}
	if err := printTask(output, response.Task); err != nil {
		return err
	}
	return printAttempt(output, response.Attempt)
}

func reconcileAttempt(output io.Writer, client *http.Client, endpoint, taskID, attemptID, outcome string) error {
	if strings.TrimSpace(outcome) == "" {
		return fmt.Errorf("--outcome is required")
	}
	path := taskPath(taskID) + "/attempts/" + url.PathEscape(attemptID) + "/reconcile"
	var response attempt
	if err := requestJSON(client, http.MethodPost, endpoint, path, map[string]string{"outcome": outcome}, &response); err != nil {
		return err
	}
	return printAttempt(output, response)
}

func taskLogs(output io.Writer, client *http.Client, endpoint, taskID string) error {
	if taskID == "" {
		return fmt.Errorf("logs requires exactly one task ID")
	}
	var response struct {
		Events []event `json:"events"`
	}
	if err := requestJSON(client, http.MethodGet, endpoint, taskPath(taskID)+"/events", nil, &response); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, "CREATED AT\tTYPE\tID"); err != nil {
		return err
	}
	for _, item := range response.Events {
		if _, err := fmt.Fprintf(output, "%s\t%s\t%s\n", item.CreatedAt, item.Type, item.ID); err != nil {
			return err
		}
	}
	return nil
}

func taskPath(taskID string) string { return "/v1/tasks/" + url.PathEscape(taskID) }

func printTask(output io.Writer, item task) error {
	_, err := fmt.Fprintf(output, "id: %s\nstate: %s\nexecution class: %s\nrepository: %s\nbase ref: %s\nobjective: %s\n", item.ID, item.State, item.ExecutionClass, item.Repository, item.BaseRef, item.Objective)
	return err
}

func printAttempt(output io.Writer, item attempt) error {
	_, err := fmt.Fprintf(output, "attempt: %s\nattempt number: %d\nmodel profile: %s\nattempt state: %s\n", item.ID, item.Number, item.ModelProfile, item.State)
	return err
}

func requestJSON(client *http.Client, method, endpoint, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, strings.TrimRight(endpoint, "/")+path, body)
	if err != nil {
		return fmt.Errorf("build controller request: %w", err)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("contact controller: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&failure)
		if failure.Error == "" {
			failure.Error = "request failed"
		}
		return fmt.Errorf("controller returned %s: %s", response.Status, failure.Error)
	}
	if output == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output); err != nil {
		return fmt.Errorf("decode controller response: %w", err)
	}
	return nil
}
