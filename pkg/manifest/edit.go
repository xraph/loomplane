package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"slices"

	"gopkg.in/yaml.v3"
)

// Edits change a manifest's YAML in place, through its node tree, so the
// comments and order someone wrote survive: a save from a parsed
// manifest would drop them.

// WithResource adds a resource to a manifest's YAML. It refuses a name a
// service or resource already has.
func WithResource(doc []byte, name string, r Resource) ([]byte, error) {
	root, file, err := mapping(doc)
	if err != nil {
		return nil, err
	}
	for _, section := range []string{"services", "resources", "gateways"} {
		if m := child(root, section); m != nil && child(m, name) != nil {
			return nil, fmt.Errorf("%w: something is already called %s", ErrInvalid, name)
		}
	}
	resources := child(root, "resources")
	if resources == nil {
		resources = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = append(root.Content, scalar("resources"), resources)
	}
	value := &yaml.Node{Kind: yaml.MappingNode}
	value.Content = append(value.Content, scalar("kind"), scalar(r.Kind))
	if r.Provider != "" {
		value.Content = append(value.Content, scalar("provider"), scalar(r.Provider))
	}
	if r.Version != "" {
		value.Content = append(value.Content, scalar("version"), scalar(r.Version))
	}
	resources.Content = append(resources.Content, scalar(name), value)
	return encode(file)
}

// WithIgnored adds a resource kind to what a manifest's YAML ignores.
func WithIgnored(doc []byte, kind string) ([]byte, error) {
	root, file, err := mapping(doc)
	if err != nil {
		return nil, err
	}
	ignore := child(root, "ignore")
	if ignore == nil {
		ignore = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = append(root.Content, scalar("ignore"), ignore)
	}
	kinds := child(ignore, "resources")
	if kinds == nil {
		kinds = &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		ignore.Content = append(ignore.Content, scalar("resources"), kinds)
	}
	if kinds.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%w: ignore.resources isn't a list", ErrInvalid)
	}
	if slices.ContainsFunc(kinds.Content, func(n *yaml.Node) bool { return n.Value == kind }) {
		return doc, nil
	}
	kinds.Content = append(kinds.Content, scalar(kind))
	return encode(file)
}

// mapping parses doc and returns its top-level mapping and the document.
func mapping(doc []byte) (root, file *yaml.Node, err error) {
	file = &yaml.Node{}
	if err := yaml.Unmarshal(doc, file); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if file.Kind != yaml.DocumentNode || len(file.Content) != 1 || file.Content[0].Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("%w: the manifest isn't a mapping", ErrInvalid)
	}
	return file.Content[0], file, nil
}

// child is the value under key in a mapping, or nil.
func child(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

func encode(file *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(file); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if _, err := Parse(out); err != nil {
		return nil, errors.Join(errors.New("the edit made a manifest that doesn't parse"), err)
	}
	return out, nil
}
