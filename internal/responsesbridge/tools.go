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
	Strict      *bool           `json:"strict,omitempty"`
}

func convertTools(tools []ResponseTool) ([]ChatTool, *ToolNameMap, error) {
	converted := make([]ChatTool, 0, len(tools))
	nameMap := newToolNameMap()
	for _, tool := range tools {
		switch tool.Type {
		case "function":
			name, description, parameters, strict := tool.Name, tool.Description, tool.Parameters, tool.Strict
			if tool.Function != nil {
				name, description, parameters, strict = tool.Function.Name, tool.Function.Description, tool.Function.Parameters, tool.Function.Strict
			}
			if tool.DeferLoading {
				return nil, nil, fmt.Errorf("deferred function tools are unsupported")
			}
			chatName, err := nameMap.add(name, "", name)
			if err != nil {
				return nil, nil, err
			}
			converted = append(converted, chatFunctionTool(chatName, description, parameters, strict))
		case "namespace":
			if tool.Name == "" || len(tool.NamespaceTools) == 0 {
				return nil, nil, fmt.Errorf("namespace tool requires a name and at least one nested tool")
			}
			for _, nested := range tool.NamespaceTools {
				if nested.Type != "function" {
					return nil, nil, fmt.Errorf("unsupported namespace tool type %q", nested.Type)
				}
				name, description, parameters, strict := nested.Name, nested.Description, nested.Parameters, nested.Strict
				if nested.Function != nil {
					name, description, parameters, strict = nested.Function.Name, nested.Function.Description, nested.Function.Parameters, nested.Function.Strict
				}
				if nested.DeferLoading {
					return nil, nil, fmt.Errorf("deferred namespace tools are unsupported")
				}
				if name == "" {
					return nil, nil, fmt.Errorf("namespace function tool name is required")
				}
				chatName, err := nameMap.add(name, tool.Name, name)
				if err != nil {
					return nil, nil, err
				}
				if len(bytes.TrimSpace(parameters)) == 0 {
					parameters = nested.InputSchema
				}
				if len(bytes.TrimSpace(parameters)) == 0 {
					parameters = json.RawMessage(`{"type":"object","properties":{}}`)
				}
				converted = append(converted, chatFunctionTool(chatName, description, parameters, strict))
			}
		default:
			return nil, nil, fmt.Errorf("unsupported Responses tool type %q", tool.Type)
		}
	}
	return converted, nameMap, nil
}

func chatFunctionTool(name, description string, parameters json.RawMessage, strict *bool) ChatTool {
	if len(bytes.TrimSpace(parameters)) == 0 {
		parameters = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return ChatTool{Type: "function", Function: ChatFunction{
		Name: name, Description: description, Parameters: parameters, Strict: strict,
	}}
}
