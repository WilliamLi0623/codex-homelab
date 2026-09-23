package responsesbridge

import (
	"crypto/sha256"
	"encoding/base32"
	"fmt"
)

type toolTarget struct {
	Namespace string
	Name      string
}

// ToolNameMap is request-scoped and deterministic. Namespace functions are
// hashed into valid Chat Completions names, then restored on Responses output.
type ToolNameMap struct {
	chatToTarget map[string]toolTarget
	targetToChat map[string]string
}

func newToolNameMap() *ToolNameMap {
	return &ToolNameMap{chatToTarget: map[string]toolTarget{}, targetToChat: map[string]string{}}
}

func (m *ToolNameMap) add(name, namespace, responseName string) (string, error) {
	if name == "" || responseName == "" {
		return "", fmt.Errorf("function tool name is required")
	}
	target := toolTarget{Namespace: namespace, Name: responseName}
	key := targetKey(namespace, responseName)
	if _, exists := m.targetToChat[key]; exists {
		return "", fmt.Errorf("duplicate Responses tool identity")
	}
	chatName := responseName
	if namespace != "" {
		sum := sha256.Sum256([]byte(namespace + "\x00" + responseName))
		chatName = "ns_" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	}
	if previous, exists := m.chatToTarget[chatName]; exists && previous != target {
		return "", fmt.Errorf("Chat function name collision while flattening namespace tools")
	}
	m.chatToTarget[chatName] = target
	m.targetToChat[key] = chatName
	return chatName, nil
}

func (m *ToolNameMap) chatName(namespace, name string) (string, error) {
	if m == nil || name == "" {
		return "", fmt.Errorf("function call name is required")
	}
	chatName, exists := m.targetToChat[targetKey(namespace, name)]
	if !exists {
		return "", fmt.Errorf("function call references an undeclared tool")
	}
	return chatName, nil
}

func (m *ToolNameMap) restoreCall(call AssembledToolCall) (AssembledToolCall, error) {
	if m == nil {
		return AssembledToolCall{}, fmt.Errorf("tool name map is missing")
	}
	target, exists := m.chatToTarget[call.Name]
	if !exists {
		return AssembledToolCall{}, fmt.Errorf("upstream requested an undeclared function tool")
	}
	call.Namespace = target.Namespace
	call.Name = target.Name
	return call, nil
}

func targetKey(namespace, name string) string { return namespace + "\x00" + name }
