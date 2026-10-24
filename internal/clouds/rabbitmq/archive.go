package rabbitmq

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"io"
	"path"
	"slices"
	"strings"
)

type queue struct {
	Persistent persistentCount `json:"messages_persistent"`
	Name       string          `json:"name"`
	Durable    bool            `json:"durable"`
	AutoDelete bool            `json:"auto_delete"`
	Type       string          `json:"type"`
	Messages   int64           `json:"messages"`
	Arguments  [][]any         `json:"arguments"`
}
type archiveManifest struct{ Queues []queue }

type persistentCount int64

func (n *persistentCount) UnmarshalJSON(data []byte) error {
	// RabbitMQ emits an empty string for this unsupported quorum statistic.
	if string(data) == `""` || string(data) == "null" {
		*n = 0
		return nil
	}
	return json.Unmarshal(data, (*int64)(n))
}

func normalizePersistence(queues []queue) {
	for i := range queues {
		// Quorum queues persist every message. The classic queue's optional
		// messages_persistent statistic is not their durability contract.
		if queues[i].Type == "quorum" {
			queues[i].Persistent = persistentCount(queues[i].Messages)
		}
	}
}

func readArchive(r io.Reader) (archiveManifest, error) {
	var m archiveManifest
	tr := tar.NewReader(r)
	seen := map[string]bool{}
	metadata := map[string]json.RawMessage{}
	files := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return m, err
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == "" || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") || seen[name] || len(name) > 1024 || h.Size < 0 || h.Size > maxArchive || h.Linkname != "" || (h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir) {
			return m, errors.New("unsafe RabbitMQ archive entry")
		}
		seen[name] = true
		if len(seen) > 100000 {
			return m, errors.New("too many RabbitMQ archive entries")
		}
		if name == "mnesia" || strings.HasPrefix(name, "mnesia/") {
			if h.Typeflag == tar.TypeReg {
				files++
			}
			if _, err = io.CopyN(io.Discard, tr, h.Size); err != nil {
				return m, err
			}
			continue
		}
		if !slices.Contains([]string{"profile.json", "queues.json", "vhosts.json", "users.json", "policies.json", "parameters.json", "cluster.json"}, name) || h.Typeflag != tar.TypeReg || h.Size > 1<<20 {
			return m, errors.New("unexpected RabbitMQ archive metadata")
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return m, err
		}
		metadata[name] = data
	}
	if files == 0 || len(metadata) != 7 {
		return m, errors.New("incomplete RabbitMQ message backup")
	}
	var profile struct{ Format, Node, Version string }
	if err := json.Unmarshal(metadata["profile.json"], &profile); err != nil {
		return m, err
	}
	if profile.Format != "rabbitmq-node-v1" || profile.Node != "rabbit@localhost" || profile.Version != "4.3.6" {
		return m, errors.New("unsupported RabbitMQ backup profile")
	}
	var cluster struct {
		DiskNodes    []string `json:"disk_nodes"`
		RunningNodes []string `json:"running_nodes"`
	}
	if err := json.Unmarshal(metadata["cluster.json"], &cluster); err != nil {
		return m, err
	}
	if !slices.Equal(cluster.DiskNodes, []string{"rabbit@localhost"}) || !slices.Equal(cluster.RunningNodes, cluster.DiskNodes) {
		return m, errors.New("RabbitMQ backup requires one node")
	}
	var vhosts []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(metadata["vhosts.json"], &vhosts); err != nil {
		return m, err
	}
	if len(vhosts) != 1 || vhosts[0].Name != "/" {
		return m, errors.New("RabbitMQ backup requires the default virtual host")
	}
	var users []struct {
		User string `json:"user"`
	}
	if err := json.Unmarshal(metadata["users.json"], &users); err != nil {
		return m, err
	}
	if len(users) != 2 || users[0].User == users[1].User {
		return m, errors.New("unsupported RabbitMQ users")
	}
	for _, u := range users {
		if u.User != "admin" && u.User != "app" {
			return m, errors.New("unsupported RabbitMQ user")
		}
	}
	for _, name := range []string{"policies.json", "parameters.json"} {
		var values []json.RawMessage
		if err := json.Unmarshal(metadata[name], &values); err != nil {
			return m, err
		}
		if len(values) != 0 {
			return m, errors.New("RabbitMQ policies and runtime parameters are outside the backup profile")
		}
	}
	if err := json.Unmarshal(metadata["queues.json"], &m.Queues); err != nil {
		return m, err
	}
	if len(m.Queues) > 100 {
		return m, errors.New("RabbitMQ backup supports at most 100 queues")
	}
	normalizePersistence(m.Queues)
	names := map[string]bool{}
	for _, q := range m.Queues {
		if q.Name == "" || names[q.Name] || !q.Durable || q.AutoDelete || (q.Type != "classic" && q.Type != "quorum") || q.Messages < 0 || int64(q.Persistent) != q.Messages {
			return m, errors.New("RabbitMQ backup requires durable classic or quorum queues")
		}
		names[q.Name] = true
		for _, argument := range q.Arguments {
			if len(argument) != 3 || argument[0] != "x-queue-type" || argument[1] != "longstr" || argument[2] != q.Type {
				return m, errors.New("unsupported RabbitMQ queue arguments")
			}
		}
	}
	slices.SortFunc(m.Queues, func(a, b queue) int { return strings.Compare(a.Name, b.Name) })
	return m, nil
}
