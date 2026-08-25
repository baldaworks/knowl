package okf

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

func splitFrontmatter(content []byte) ([]byte, string, bool) {
	if !bytes.HasPrefix(content, []byte("---\n")) && !bytes.HasPrefix(content, []byte("---\r\n")) {
		return nil, string(content), false
	}

	lineEnd := bytes.IndexByte(content, '\n')
	remaining := content[lineEnd+1:]
	offset := 0
	for len(remaining) > 0 {
		next := bytes.IndexByte(remaining, '\n')
		lineBytes := remaining
		advance := len(remaining)
		if next >= 0 {
			lineBytes = remaining[:next]
			advance = next + 1
		}
		line := strings.TrimSuffix(string(lineBytes), "\r")
		if line == "---" {
			bodyStart := lineEnd + 1 + offset + advance
			return content[lineEnd+1 : lineEnd+1+offset], string(content[bodyStart:]), true
		}
		offset += advance
		if next < 0 {
			break
		}
		remaining = remaining[advance:]
	}

	return nil, "", true
}

func decodeYAMLMap(raw []byte, limits Limits) (map[string]any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}

	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("multiple YAML documents")
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("frontmatter must be a mapping")
	}

	stats := yamlStats{active: make(map[*yaml.Node]bool)}
	if err := stats.visit(root.Content[0], 1, limits); err != nil {
		return nil, err
	}

	budget := limits.MaxNodes
	value, err := yamlValue(root.Content[0], 1, limits.MaxDepth, &budget, make(map[*yaml.Node]bool))
	if err != nil {
		return nil, err
	}
	mapping, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("frontmatter must be a mapping")
	}

	return mapping, nil
}

type yamlStats struct {
	nodes   int
	aliases int
	active  map[*yaml.Node]bool
}

func (s *yamlStats) visit(node *yaml.Node, depth int, limits Limits) error {
	if node == nil || depth > limits.MaxDepth {
		return errors.New("YAML depth limit exceeded")
	}
	if s.active[node] {
		return errors.New("recursive YAML alias")
	}
	s.nodes++
	if s.nodes > limits.MaxNodes {
		return errors.New("YAML node limit exceeded")
	}
	if node.Kind == yaml.AliasNode {
		s.aliases++
		if s.aliases > limits.MaxAliases || node.Alias == nil {
			return errors.New("YAML alias limit exceeded")
		}
	}

	s.active[node] = true
	defer delete(s.active, node)
	for _, child := range node.Content {
		if err := s.visit(child, depth+1, limits); err != nil {
			return err
		}
	}

	return nil
}

func yamlValue(node *yaml.Node, depth, maxDepth int, budget *int, active map[*yaml.Node]bool) (any, error) {
	if node == nil || depth > maxDepth || *budget <= 0 || active[node] {
		return nil, errors.New("YAML expansion limit exceeded")
	}
	*budget--
	active[node] = true
	defer delete(active, node)

	switch node.Kind {
	case yaml.AliasNode:
		return yamlValue(node.Alias, depth+1, maxDepth, budget, active)
	case yaml.MappingNode:
		if len(node.Content)%2 != 0 {
			return nil, errors.New("invalid YAML mapping")
		}
		result := make(map[string]any, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "" || key.Value == "<<" {
				return nil, errors.New("YAML mapping keys must be non-empty strings")
			}
			if _, exists := result[key.Value]; exists {
				return nil, errors.New("duplicate YAML mapping key")
			}
			value, err := yamlValue(node.Content[index+1], depth+1, maxDepth, budget, active)
			if err != nil {
				return nil, err
			}
			result[key.Value] = value
		}
		return result, nil
	case yaml.SequenceNode:
		result := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			value, err := yamlValue(child, depth+1, maxDepth, budget, active)
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		return result, nil
	case yaml.ScalarNode:
		return yamlScalar(node)
	default:
		return nil, errors.New("unsupported YAML node")
	}
}

func yamlScalar(node *yaml.Node) (any, error) {
	switch node.Tag {
	case "!!null":
		return nil, nil
	case "!!str", "!!timestamp":
		return node.Value, nil
	case "!!bool":
		var value bool
		if err := node.Decode(&value); err != nil {
			return nil, err
		}
		return value, nil
	case "!!int":
		var signed int64
		if err := node.Decode(&signed); err == nil {
			return signed, nil
		}
		var unsigned uint64
		if err := node.Decode(&unsigned); err != nil {
			return nil, err
		}
		return unsigned, nil
	case "!!float":
		var value float64
		if err := node.Decode(&value); err != nil {
			return nil, err
		}
		if strconv.FormatFloat(value, 'g', -1, 64) == "NaN" ||
			strings.Contains(strconv.FormatFloat(value, 'g', -1, 64), "Inf") {
			return nil, errors.New("non-finite YAML number")
		}
		return value, nil
	default:
		return nil, errors.New("unsupported YAML scalar tag")
	}
}
