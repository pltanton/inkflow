package frontmatter

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

func UpdateTags(content string, tags []string) string {
	uniq := uniqueTags(tags)
	if len(uniq) == 0 {
		return content
	}
	front, body, ok := splitFrontmatter(content)
	if !ok {
		return renderWithFrontmatter(content, uniq)
	}
	doc, err := parseFrontmatter(front)
	if err != nil {
		return renderWithFrontmatter(content, uniq)
	}
	replaceTags(doc, uniq)
	rendered, err := yaml.Marshal(doc)
	if err != nil {
		return renderWithFrontmatter(content, uniq)
	}
	rendered = bytes.TrimSpace(rendered)
	return "---\n" + string(rendered) + "\n---\n" + body
}

func splitFrontmatter(content string) (front string, body string, ok bool) {
	if !strings.HasPrefix(content, "---\n") {
		return "", content, false
	}
	rest := content[len("---\n"):]
	for i := 0; i < len(rest); {
		j := strings.IndexByte(rest[i:], '\n')
		if j < 0 {
			return "", content, false
		}
		line := strings.TrimRight(rest[i:i+j], "\r")
		if line == "---" || line == "..." {
			end := i + j + 1
			return rest[:i], rest[end:], true
		}
		i += j + 1
	}
	return "", content, false
}

func parseFrontmatter(front string) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(front), &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("empty frontmatter")
	}
	return doc.Content[0], nil
}

func replaceTags(doc *yaml.Node, tags []string) {
	if doc.Kind != yaml.MappingNode {
		doc.Kind = yaml.MappingNode
		doc.Tag = "!!map"
		doc.Style = 0
		doc.Content = nil
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key := doc.Content[i]
		if key != nil && key.Kind == yaml.ScalarNode && key.Value == "tags" {
			doc.Content[i+1] = tagsNode(tags)
			return
		}
	}
	doc.Content = append(doc.Content, scalarNode("tags"), tagsNode(tags))
}

func tagsNode(tags []string) *yaml.Node {
	n := &yaml.Node{
		Kind:    yaml.SequenceNode,
		Tag:     "!!seq",
		Content: make([]*yaml.Node, 0, len(tags)),
	}
	for _, tag := range tags {
		n.Content = append(n.Content, scalarNode(tag))
	}
	return n
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!str",
		Value: value,
	}
}

func uniqueTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}

func renderWithFrontmatter(body string, tags []string) string {
	doc := &yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
	}
	replaceTags(doc, tags)
	rendered, err := yaml.Marshal(doc)
	if err != nil {
		return body
	}
	rendered = bytes.TrimSpace(rendered)
	if body == "" {
		return "---\n" + string(rendered) + "\n---\n"
	}
	return "---\n" + string(rendered) + "\n---\n\n" + body
}
