package responsesbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type UpstreamError struct {
	Status int
	Class  string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("CC Hub upstream %s (status %d)", e.Class, e.Status)
}

func localStatus(err error) int {
	if e, ok := err.(*UpstreamError); ok {
		return e.Status
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return 504
	}
	return 502
}

func streamErrorClass(err error) string {
	if errors.Is(err, context.Canceled) {
		return "client_cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "upstream_timeout"
	}
	if strings.Contains(err.Error(), "decode upstream SSE") {
		return "malformed_sse"
	}
	if strings.Contains(err.Error(), "completed without output") {
		return "malformed_upstream"
	}
	if strings.Contains(err.Error(), "tool call") {
		return "invalid_tool_call"
	}
	if strings.Contains(err.Error(), "ended before [DONE]") || strings.Contains(err.Error(), "read upstream SSE") {
		return "stream_interrupted"
	}
	return "bridge_stream_error"
}

// streamErrorDiagnostic returns parser-generated details only. It deliberately
// does not include upstream payloads, prompts, tool arguments, or response text.
func streamErrorDiagnostic(err error) string {
	if err == nil {
		return "none"
	}
	detail := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, err.Error())
	if len(detail) > 160 {
		detail = detail[:160]
	}
	return detail
}
