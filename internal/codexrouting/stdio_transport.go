package codexrouting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/WilliamLi0623/codex-homelab/internal/agentd"
)

// CodexAppServerTransport starts a separate App Server process for each quota
// read. This keeps it isolated from active Codex tasks and lets the caller's
// deadline terminate a stalled child without leaving a wedged process behind.
type CodexAppServerTransport struct {
	Executable  string
	Environment []string
}

func (t CodexAppServerTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if method != accountRateLimitsReadMethod {
		return nil, errors.New("quota app server transport permits only account/rateLimits/read")
	}
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil || string(encoded) != "{}" {
			return nil, errors.New("quota app server transport accepts empty parameters only")
		}
	}
	if t.Executable == "" {
		return nil, errors.New("Codex executable path is required")
	}
	process, err := agentd.StartIsolatedCodexAppServer(ctx, t.Executable, t.Environment)
	if err != nil {
		return nil, fmt.Errorf("start isolated Codex quota process: %w", err)
	}
	defer func() { _ = process.Close() }()
	initCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := process.Client.Initialize(initCtx); err != nil {
		return nil, errors.New("initialize isolated Codex quota process failed")
	}
	return process.Client.ReadAccountRateLimits(ctx)
}
