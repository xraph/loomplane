package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Edits change a manifest's YAML in place, through its node tree, so the
// comments and order someone wrote survive: a save from a parsed
// manifest would drop them.

// Placement changes where an existing service, resource or gateway runs.
// A nil field leaves its value alone; an empty string restores inheritance.
type Placement struct {
	Section     string  `json:"section"`
	Name        string  `json:"name"`
	Environment string  `json:"environment,omitempty"`
	Provider    *string `json:"provider,omitempty"`
	Region      *string `json:"region,omitempty"`
	CloudTarget *string `json:"cloud_target,omitempty"`
	// CloudTags replaces the tag list; an empty list restores inheritance.
	// Clear cloud_target in the same edit when switching to tags.
	CloudTags  *[]string `json:"cloud_tags,omitempty"`
	Target     *string   `json:"target,omitempty"`
	HealthPath *string   `json:"health_path,omitempty"`
	Endpoint   *Endpoint `json:"endpoint,omitempty"`
	Build      *Build    `json:"build,omitempty"`
	Enabled    *bool     `json:"enabled,omitempty"`
}

// WithPlacement edits only placement fields, preserving source, needs and
// configuration for every other environment.
func WithPlacement(doc []byte, p Placement) ([]byte, error) {
	if _, err := Parse(doc); err != nil {
		return nil, err
	}
	if !slices.Contains([]string{"services", "resources", "gateways"}, p.Section) ||
		(p.CloudTarget == nil && p.CloudTags == nil && p.Provider == nil && p.Region == nil && p.Enabled == nil && p.Target == nil && p.HealthPath == nil && p.Endpoint == nil && p.Build == nil) {
		return nil, fmt.Errorf("%w: name a service, resource or gateway and its placement changes", ErrInvalid)
	}
	if p.Section != "services" && (p.CloudTarget != nil || p.CloudTags != nil || p.Target != nil || p.HealthPath != nil || p.Endpoint != nil || p.Build != nil) {
		return nil, fmt.Errorf("%w: cloud_target, cloud_tags, target, endpoint, health_path and build edits need a service", ErrInvalid)
	}
	if p.Environment == "" && p.Section != "services" && p.Enabled != nil {
		return nil, fmt.Errorf("%w: enabled on %s needs an environment", ErrInvalid, p.Section)
	}
	if p.Environment != "" && !envRE.MatchString(p.Environment) {
		return nil, fmt.Errorf("%w: %s isn't an environment name", ErrInvalid, p.Environment)
	}
	_, file, err := mapping(doc)
	if err != nil {
		return nil, err
	}
	budget := MaxSize
	file, err = detachedPlacementNode(file, 0, &budget)
	if err != nil {
		return nil, err
	}
	root := file.Content[0]
	section := child(root, p.Section)
	if section == nil {
		return nil, fmt.Errorf("%w: %s doesn't exist", ErrInvalid, p.Section)
	}
	if err := placementMapping(section); err != nil {
		return nil, err
	}
	target := child(section, p.Name)
	if target == nil {
		return nil, fmt.Errorf("%w: %s.%s doesn't exist", ErrInvalid, p.Section, p.Name)
	}
	if err := placementMapping(target); err != nil {
		return nil, err
	}
	if p.Environment != "" {
		envs := child(target, "environments")
		if envs == nil {
			envs = &yaml.Node{Kind: yaml.MappingNode}
			target.Content = append(target.Content, scalar("environments"), envs)
		}
		if err := placementMapping(envs); err != nil {
			return nil, err
		}
		target = child(envs, p.Environment)
		if target == nil {
			target = &yaml.Node{Kind: yaml.MappingNode}
			envs.Content = append(envs.Content, scalar(p.Environment), target)
		}
		if err := placementMapping(target); err != nil {
			return nil, err
		}
	}
	for _, field := range []struct {
		key   string
		value *string
	}{{"cloud_target", p.CloudTarget}, {"provider", p.Provider}, {"region", p.Region}, {"target", p.Target}, {"health_path", p.HealthPath}} {
		if field.value != nil {
			setPlacementField(target, field.key, scalar(*field.value), *field.value == "")
		}
	}
	if p.CloudTags != nil {
		value := &yaml.Node{}
		if err := value.Encode(*p.CloudTags); err != nil {
			return nil, err
		}
		value.Style = yaml.FlowStyle
		setPlacementField(target, "cloud_tags", value, len(*p.CloudTags) == 0)
	}
	if p.Endpoint != nil {
		value := &yaml.Node{}
		if err := value.Encode(p.Endpoint); err != nil {
			return nil, err
		}
		setPlacementField(target, "endpoint", value, *p.Endpoint == (Endpoint{}))
	}
	if p.Build != nil {
		value := &yaml.Node{}
		if err := value.Encode(p.Build); err != nil {
			return nil, err
		}
		setPlacementField(target, "build", value, *p.Build == (Build{}))
	}
	if p.Enabled != nil {
		value := &yaml.Node{}
		if err := value.Encode(*p.Enabled); err != nil {
			return nil, err
		}
		setPlacementField(target, "enabled", value, false)
	}
	return encode(file)
}

// Detach aliases before editing so shared settings in another environment
// keep their original values. Empty YAML mappings often parse as null.
func placementMapping(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode {
		budget := MaxSize
		clone, err := detachedPlacementNode(n, 0, &budget)
		if err != nil {
			return err
		}
		*n = *clone
	}
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		n.Kind, n.Tag, n.Value, n.Content = yaml.MappingNode, "!!map", "", nil
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("%w: placement settings must be a mapping", ErrInvalid)
	}
	return nil
}

func detachedPlacementNode(n *yaml.Node, depth int, budget *int) (*yaml.Node, error) {
	if n == nil || depth > 64 || *budget <= 0 {
		return nil, fmt.Errorf("%w: placement alias is too deeply nested", ErrInvalid)
	}
	*budget--
	if n.Kind == yaml.AliasNode {
		clone, err := detachedPlacementNode(n.Alias, depth+1, budget)
		if err != nil {
			return nil, err
		}
		if n.HeadComment != "" {
			clone.HeadComment = n.HeadComment
		}
		if n.LineComment != "" {
			clone.LineComment = n.LineComment
		}
		if n.FootComment != "" {
			clone.FootComment = n.FootComment
		}
		return clone, nil
	}
	clone := *n
	clone.Anchor, clone.Alias = "", nil
	clone.Content = make([]*yaml.Node, 0, len(n.Content))
	for _, child := range n.Content {
		c, err := detachedPlacementNode(child, depth+1, budget)
		if err != nil {
			return nil, err
		}
		clone.Content = append(clone.Content, c)
	}
	if clone.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(clone.Content); i += 2 {
			if clone.Content[i].Tag == "!!merge" {
				retainPlacementComments(&clone, clone.Content[i], clone.Content[i+1])
			}
		}
		clone.Content = explicitPlacementFields(clone.Content)
	}
	return &clone, nil
}

// YAML merge sequences give earlier mappings precedence, while explicit
// fields always win. Materialize them so clearing a field really clears it.
func explicitPlacementFields(content []*yaml.Node) []*yaml.Node {
	seen := map[string]bool{}
	for i := 0; i+1 < len(content); i += 2 {
		if content[i].Tag != "!!merge" {
			seen[content[i].Value] = true
		}
	}
	out := make([]*yaml.Node, 0, len(content))
	for i := 0; i+1 < len(content); i += 2 {
		key, value := content[i], content[i+1]
		if key.Tag != "!!merge" {
			out = append(out, key, value)
			continue
		}
		mappings := []*yaml.Node{value}
		if value.Kind == yaml.SequenceNode {
			mappings = value.Content
		}
		for _, mapping := range mappings {
			for j := 0; j+1 < len(mapping.Content); j += 2 {
				k, v := mapping.Content[j], mapping.Content[j+1]
				if seen[k.Value] {
					continue
				}
				seen[k.Value] = true
				out = append(out, k, v)
			}
		}
	}
	return out
}

func setPlacementField(target *yaml.Node, key string, value *yaml.Node, remove bool) {
	for i := 0; i+1 < len(target.Content); i += 2 {
		if target.Content[i].Value != key {
			continue
		}
		if remove {
			retainPlacementComments(target, target.Content[i], target.Content[i+1])
			target.Content = append(target.Content[:i], target.Content[i+2:]...)
		} else {
			value.HeadComment, value.LineComment, value.FootComment = target.Content[i+1].HeadComment, target.Content[i+1].LineComment, target.Content[i+1].FootComment
			target.Content[i+1] = value
		}
		return
	}
	if !remove {
		target.Content = append(target.Content, scalar(key), value)
	}
}

func retainPlacementComments(target *yaml.Node, nodes ...*yaml.Node) {
	comments := make([]string, 0, 1+3*len(nodes))
	comments = append(comments, target.HeadComment)
	for _, n := range nodes {
		comments = append(comments, n.HeadComment, n.LineComment, n.FootComment)
	}
	target.HeadComment = strings.TrimSpace(strings.Join(comments, "\n"))
}

// WithResource adds a resource to a manifest's YAML. It refuses a name a
// service or resource already has.
func WithResource(doc []byte, name string, r Resource) ([]byte, error) {
	root, file, err := mapping(doc)
	if err != nil {
		return nil, err
	}
	for _, section := range []string{"services", "resources", "gateways", "links"} {
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
	if r.Target != "" {
		value.Content = append(value.Content, scalar("target"), scalar(r.Target))
	}
	if r.Version != "" {
		value.Content = append(value.Content, scalar("version"), scalar(r.Version))
	}
	if len(r.Uses) > 0 {
		uses := &yaml.Node{Kind: yaml.MappingNode}
		for _, dep := range sortedKeys(r.Uses) {
			uses.Content = append(uses.Content, scalar(dep), useNode(r.Uses[dep]))
		}
		value.Content = append(value.Content, scalar("uses"), uses)
	}
	resources.Content = append(resources.Content, scalar(name), value)
	return encode(file)
}

// useNode is a dependency choice as YAML. A choice with nothing set is an
// empty mapping, which reads back as a new dependency.
func useNode(u Use) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode}
	if u.Resource != "" {
		n.Content = append(n.Content, scalar("resource"), scalar(u.Resource))
	}
	if u.External != "" {
		n.Content = append(n.Content, scalar("external"), scalar(u.External))
	}
	if u.Enabled != nil {
		n.Content = append(n.Content, scalar("enabled"), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(*u.Enabled)})
	}
	if len(n.Content) == 0 {
		n.Style = yaml.FlowStyle
	}
	return n
}

// WithNeed makes a service need something, and sets the options it passes
// to it. The need is added once, after the ones already listed. Options
// replace the need's options as a whole; none leaves what is there alone,
// so linking again without options doesn't wipe them. Unlink and link
// again to clear them.
func WithNeed(doc []byte, service, need string, options map[string]string) ([]byte, error) {
	root, file, err := mapping(doc)
	if err != nil {
		return nil, err
	}
	svc, err := editTarget(root, "services", service)
	if err != nil {
		return nil, err
	}
	var problems Problems
	checkNeedOptions(func(path, format string, args ...any) {
		problems = append(problems, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
	}, "services."+service+".need_options."+need, options)
	if len(problems) > 0 {
		return nil, problems
	}
	changed := false
	needs := child(svc, "needs")
	switch {
	case needs == nil:
		needs = &yaml.Node{Kind: yaml.SequenceNode}
		svc.Content = append(svc.Content, scalar("needs"), needs)
	case needs.Kind == yaml.ScalarNode && needs.Tag == "!!null":
		needs.Kind, needs.Tag, needs.Value = yaml.SequenceNode, "!!seq", ""
	case needs.Kind != yaml.SequenceNode:
		return nil, fmt.Errorf("%w: services.%s.needs isn't a list", ErrInvalid, service)
	}
	if !slices.ContainsFunc(needs.Content, func(n *yaml.Node) bool { return n.Value == need }) {
		needs.Content = append(needs.Content, scalar(need))
		changed = true
	}
	if len(options) > 0 {
		all, err := editMapping(svc, "need_options", true)
		if err != nil {
			return nil, fmt.Errorf("%w: services.%s.%w", ErrInvalid, service, err)
		}
		value := &yaml.Node{Kind: yaml.MappingNode}
		for _, name := range sortedKeys(options) {
			value.Content = append(value.Content, scalar(name), scalar(options[name]))
		}
		setChild(all, need, value)
		changed = true
	}
	if !changed {
		return doc, nil
	}
	return encode(file)
}

// WithoutNeed stops a service needing something, and drops the options it
// passed to it. A need_options or needs left empty goes too. A service that
// doesn't need it is an error: the edit would do nothing.
func WithoutNeed(doc []byte, service, need string) ([]byte, error) {
	root, file, err := mapping(doc)
	if err != nil {
		return nil, err
	}
	svc, err := editTarget(root, "services", service)
	if err != nil {
		return nil, err
	}
	needs := child(svc, "needs")
	if needs == nil || needs.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%w: %s doesn't need %s", ErrInvalid, service, need)
	}
	at := slices.IndexFunc(needs.Content, func(n *yaml.Node) bool { return n.Value == need })
	if at < 0 {
		return nil, fmt.Errorf("%w: %s doesn't need %s", ErrInvalid, service, need)
	}
	needs.Content = slices.Delete(needs.Content, at, at+1)
	if len(needs.Content) == 0 {
		removeChild(svc, "needs")
	}
	if all := child(svc, "need_options"); all != nil && all.Kind == yaml.MappingNode {
		removeChild(all, need)
		if len(all.Content) == 0 {
			removeChild(svc, "need_options")
		}
	}
	return encode(file)
}

// WithUse sets where one dependency of a catalog resource comes from,
// replacing that dependency's choice and leaving the others. A choice with
// nothing set asks for a new dependency.
func WithUse(doc []byte, resource, dependency string, u Use) ([]byte, error) {
	root, file, err := mapping(doc)
	if err != nil {
		return nil, err
	}
	if !nameRE.MatchString(dependency) {
		return nil, fmt.Errorf("%w: %q isn't a dependency name", ErrInvalid, dependency)
	}
	res, err := editTarget(root, "resources", resource)
	if err != nil {
		return nil, err
	}
	uses, err := editMapping(res, "uses", true)
	if err != nil {
		return nil, fmt.Errorf("%w: resources.%s.%w", ErrInvalid, resource, err)
	}
	setChild(uses, dependency, useNode(u))
	return encode(file)
}

// editTarget is the mapping for a named service or resource in a section.
func editTarget(root *yaml.Node, section, name string) (*yaml.Node, error) {
	target := child(child(root, section), name)
	if target == nil {
		return nil, fmt.Errorf("%w: %s.%s doesn't exist", ErrInvalid, section, name)
	}
	if target.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: %s.%s isn't a mapping the edit can change", ErrInvalid, section, name)
	}
	return target, nil
}

// editMapping is the mapping under key, made if it's missing or empty.
func editMapping(m *yaml.Node, key string, create bool) (*yaml.Node, error) {
	n := child(m, key)
	switch {
	case n == nil && create:
		n = &yaml.Node{Kind: yaml.MappingNode}
		m.Content = append(m.Content, scalar(key), n)
	case n != nil && n.Kind == yaml.ScalarNode && n.Tag == "!!null":
		n.Kind, n.Tag, n.Value, n.Style = yaml.MappingNode, "!!map", "", 0
	case n == nil || n.Kind != yaml.MappingNode:
		return nil, fmt.Errorf("%s isn't a mapping the edit can change", key)
	}
	return n, nil
}

// setChild sets key in a mapping, in place if it's there, keeping the
// comments on the old value, and at the end if it isn't.
func setChild(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			old := m.Content[i+1]
			value.HeadComment, value.LineComment, value.FootComment = old.HeadComment, old.LineComment, old.FootComment
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content, scalar(key), value)
}

func removeChild(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = slices.Delete(m.Content, i, i+2)
			return
		}
	}
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
