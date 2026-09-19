package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, http.DefaultClient); err != nil {
		log.Fatal(err)
	}
}

func run(arguments []string, output io.Writer, client *http.Client) error {
	if len(arguments) == 0 {
		return fmt.Errorf("usage: codexctl doctor --endpoint http://127.0.0.1:8080")
	}
	command := arguments[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	endpoint := flags.String("endpoint", "http://127.0.0.1:8080", "Controller endpoint")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}

	switch command {
	case "doctor":
		return doctor(output, client, *endpoint)
	default:
		return fmt.Errorf("unsupported command %q", command)
	}
}

func doctor(output io.Writer, client *http.Client, endpoint string) error {
	request, err := http.NewRequest(http.MethodGet, strings.TrimRight(endpoint, "/")+"/v1/health", nil)
	if err != nil {
		return fmt.Errorf("build health request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("contact controller: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("controller health returned %s", response.Status)
	}
	var health struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		return fmt.Errorf("decode controller health: %w", err)
	}
	if health.Status != "ok" {
		return fmt.Errorf("controller health status is %q", health.Status)
	}
	_, err = fmt.Fprintln(output, "controller: ok")
	return err
}
