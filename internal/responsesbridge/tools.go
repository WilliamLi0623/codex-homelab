package responsesbridge

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type ChatTool struct {
	Type     string       `json:"type"`
	Function ChatFunction `json:"function"`
}

type ChatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

func convertTools(tools []ResponseTool) ([]ChatTool, error) {
	converted := make([]ChatTool, 0, len(tools))
	for _, tool := range tools {
		if tool.Type != "function" {
			return nil, fmt.Errorf("unsupported Responses tool type %q", tool.Type)
		}
		name, description, parameters := tool.Name, tool.Description, tool.Parameters
		if tool.Function != nil {
			name, description, parameters = tool.Function.Name, tool.Function.Description, tool.Function.Parameters
		}
		if name == "" {
			return nil, fmt.Errorf("function tool name is required")
		}
		if len(bytes.TrimSpace(parameters)) == 0 {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		converted = append(converted, ChatTool{Type: "function", Function: ChatFunction{
			Name: name, Description: description, Parameters: parameters,
		}})
	}
	return converted, nil
}
