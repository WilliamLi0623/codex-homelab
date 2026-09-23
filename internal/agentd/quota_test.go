package agentd

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestClientReadsAccountRateLimitsThroughReadOnlyRPC(t *testing.T) {
	input := strings.NewReader("{\"id\":1,\"result\":{}}\n{\"id\":2,\"result\":{\"ordinaryUsageAllowed\":true,\"rateLimits\":{}}}\n")
	var output strings.Builder
	client := NewClient(input, &output)
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReadAccountRateLimits(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"method":"account/rateLimits/read"`) {
		t.Fatalf("App Server RPC output = %q; expected read-only account method", output.String())
	}
}

func TestClientRejectsRateLimitsRPCBeforeInitialization(t *testing.T) {
	client := NewClient(strings.NewReader(""), io.Discard)
	if _, err := client.ReadAccountRateLimits(context.Background()); err == nil {
		t.Fatal("ReadAccountRateLimits() succeeded before initialize")
	}
}
