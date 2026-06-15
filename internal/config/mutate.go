package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ParseConfigValue validates and converts a supported config key/value pair.
func ParseConfigValue(key, value string) (any, error) {
	key = publicConfigKey(key)
	value = strings.TrimSpace(value)

	switch key {
	case "data_dir", "db_path",
		"embedding.provider", "embedding.model", "embedding.ollama_url",
		"embedding.openai_api_key", "embedding.openai_base_url":
		return value, nil
	case "embedding.dimensions":
		return parsePositiveInt(key, value)
	case "indexing.chunk_size", "indexing.chunk_overlap":
		return parseNonNegativeInt(key, value)
	case "indexing.max_file_size":
		return parsePositiveInt64(key, value)
	case "indexing.ignore_patterns":
		return parseStringList(value)
	case "search.default_mode":
		switch value {
		case "semantic", "keyword", "hybrid":
			return value, nil
		default:
			return nil, fmt.Errorf("invalid search.default_mode value %q: expected semantic, keyword, or hybrid", value)
		}
	case "search.vector_weight", "search.text_weight":
		return parseUnitFloat32(key, value)
	case "server.mcp_enabled":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("invalid server.mcp_enabled value %q: %w", value, err)
		}
		return parsed, nil
	case "vector.veclite.m", "vector.veclite.ef_construction", "vector.veclite.ef_search":
		return parsePositiveInt(key, value)
	default:
		return nil, fmt.Errorf("unknown config key: %s", key)
	}
}

// ApplyConfigValue validates and applies a supported config key/value pair.
func ApplyConfigValue(cfg *Config, key, value string) error {
	parsed, err := ParseConfigValue(key, value)
	if err != nil {
		return err
	}

	key = publicConfigKey(key)
	switch key {
	case "data_dir":
		cfg.DataDir = parsed.(string)
	case "db_path":
		cfg.DBPath = parsed.(string)
	case "embedding.provider":
		cfg.Embedding.Provider = parsed.(string)
	case "embedding.model":
		cfg.Embedding.Model = parsed.(string)
	case "embedding.ollama_url":
		cfg.Embedding.OllamaURL = parsed.(string)
	case "embedding.openai_api_key":
		cfg.Embedding.OpenAIAPIKey = parsed.(string)
	case "embedding.openai_base_url":
		cfg.Embedding.OpenAIBaseURL = parsed.(string)
	case "embedding.dimensions":
		cfg.Embedding.Dimensions = parsed.(int)
	case "indexing.chunk_size":
		cfg.Indexing.ChunkSize = parsed.(int)
	case "indexing.chunk_overlap":
		cfg.Indexing.ChunkOverlap = parsed.(int)
	case "indexing.max_file_size":
		cfg.Indexing.MaxFileSize = parsed.(int64)
	case "indexing.ignore_patterns":
		cfg.Indexing.IgnorePatterns = parsed.([]string)
	case "search.default_mode":
		cfg.Search.DefaultMode = parsed.(string)
	case "search.vector_weight":
		cfg.Search.VectorWeight = parsed.(float32)
	case "search.text_weight":
		cfg.Search.TextWeight = parsed.(float32)
	case "server.mcp_enabled":
		cfg.Server.MCPEnabled = parsed.(bool)
	case "vector.veclite.m":
		cfg.Vector.VecLite.M = parsed.(int)
	case "vector.veclite.ef_construction":
		cfg.Vector.VecLite.EfConstruction = parsed.(int)
	case "vector.veclite.ef_search":
		cfg.Vector.VecLite.EfSearch = parsed.(int)
	}

	cfg.markPresent(key)
	return nil
}

// SetConfigValueInFile updates one config key in a YAML file without
// replacing unrelated keys.
func SetConfigValueInFile(path, key, value string) error {
	parsed, err := ParseConfigValue(key, value)
	if err != nil {
		return err
	}

	doc, err := readYAMLDocument(path)
	if err != nil {
		return err
	}

	if err := setYAMLPath(doc, strings.Split(key, "."), yamlNodeForValue(parsed)); err != nil {
		return err
	}

	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		_ = encoder.Close()
		return fmt.Errorf("failed to encode config: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("failed to encode config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	if err := os.WriteFile(path, out.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	return nil
}

func publicConfigKey(key string) string {
	return strings.TrimPrefix(key, "defaults.")
}

func parsePositiveInt(key, value string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s value %q: %w", key, value, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("invalid %s value %q: must be greater than zero", key, value)
	}
	return parsed, nil
}

func parseNonNegativeInt(key, value string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s value %q: %w", key, value, err)
	}
	if parsed < 0 {
		return 0, fmt.Errorf("invalid %s value %q: must be zero or greater", key, value)
	}
	return parsed, nil
}

func parsePositiveInt64(key, value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s value %q: %w", key, value, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("invalid %s value %q: must be greater than zero", key, value)
	}
	return parsed, nil
}

func parseUnitFloat32(key, value string) (float32, error) {
	parsed, err := strconv.ParseFloat(value, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid %s value %q: %w", key, value, err)
	}
	if parsed < 0 || parsed > 1 {
		return 0, fmt.Errorf("invalid %s value %q: must be between 0 and 1", key, value)
	}
	return float32(parsed), nil
}

func parseStringList(value string) ([]string, error) {
	if value == "" {
		return []string{}, nil
	}

	if strings.HasPrefix(value, "[") {
		var parsed []string
		if err := yaml.Unmarshal([]byte(value), &parsed); err != nil {
			return nil, fmt.Errorf("invalid string list value %q: %w", value, err)
		}
		return parsed, nil
	}

	parts := strings.Split(value, ",")
	parsed := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			parsed = append(parsed, part)
		}
	}
	return parsed, nil
}

func readYAMLDocument(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return emptyYAMLDocument(), nil
		}
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	if len(bytes.TrimSpace(data)) == 0 {
		return emptyYAMLDocument(), nil
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	if len(doc.Content) == 0 {
		return emptyYAMLDocument(), nil
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config root must be a YAML mapping")
	}
	return &doc, nil
}

func emptyYAMLDocument() *yaml.Node {
	return &yaml.Node{
		Kind: yaml.DocumentNode,
		Content: []*yaml.Node{
			{Kind: yaml.MappingNode, Tag: "!!map"},
		},
	}
}

func setYAMLPath(doc *yaml.Node, path []string, value *yaml.Node) error {
	if doc.Kind != yaml.DocumentNode {
		return fmt.Errorf("config document must be a YAML document")
	}
	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("config root must be a YAML mapping")
	}
	return setYAMLMappingPath(doc.Content[0], path, value)
}

func setYAMLMappingPath(mapping *yaml.Node, path []string, value *yaml.Node) error {
	if len(path) == 0 {
		return nil
	}

	key := path[0]
	index := yamlMappingKeyIndex(mapping, key)
	if len(path) == 1 {
		if index >= 0 {
			old := mapping.Content[index+1]
			value.HeadComment = old.HeadComment
			value.LineComment = old.LineComment
			value.FootComment = old.FootComment
			mapping.Content[index+1] = value
			return nil
		}
		mapping.Content = append(mapping.Content, yamlKeyNode(key), value)
		return nil
	}

	var child *yaml.Node
	if index >= 0 {
		child = mapping.Content[index+1]
		if child.Kind != yaml.MappingNode {
			child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			mapping.Content[index+1] = child
		}
	} else {
		child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		mapping.Content = append(mapping.Content, yamlKeyNode(key), child)
	}

	return setYAMLMappingPath(child, path[1:], value)
}

func yamlMappingKeyIndex(mapping *yaml.Node, key string) int {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func yamlKeyNode(key string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
}

func yamlNodeForValue(value any) *yaml.Node {
	switch v := value.(type) {
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
	case int:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(v)}
	case int64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(v, 10)}
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(v)}
	case float32:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(float64(v), 'f', -1, 32)}
	case []string:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range v {
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: item})
		}
		return node
	default:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: fmt.Sprint(value)}
	}
}

func collectConfigPresence(data []byte, prefix string) map[string]bool {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
		return nil
	}

	root := doc.Content[0]
	if prefix != "" {
		for _, part := range strings.Split(prefix, ".") {
			if root.Kind != yaml.MappingNode {
				return nil
			}
			index := yamlMappingKeyIndex(root, part)
			if index < 0 {
				return nil
			}
			root = root.Content[index+1]
		}
	}

	present := make(map[string]bool)
	collectNodePresence(root, "", present)
	if len(present) == 0 {
		return nil
	}
	return present
}

func collectNodePresence(node *yaml.Node, prefix string, present map[string]bool) {
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			collectNodePresence(node.Content[i+1], path, present)
		}
	case yaml.SequenceNode, yaml.ScalarNode:
		if prefix != "" {
			present[prefix] = true
		}
	}
}
