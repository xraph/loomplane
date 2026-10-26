// Package nats preserves native JetStream stream snapshots, including consumers.
package nats

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	client "github.com/nats-io/nats.go"

	"github.com/xraph/loomplane/internal/backupfiles"
	"github.com/xraph/loomplane/internal/clouds/dbexec"
)

const LabelEngine = "loomplane.nats.engine"
const maxArchive = int64(2 << 30)
const maxStreams = 100
const maxConsumers = 1000

var streamName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type Database struct {
	Recovery bool
	Connect  func(context.Context, bool) (*client.Conn, error)
}

type snapshot struct {
	Config client.StreamConfig `json:"config"`
	State  client.StreamState  `json:"state"`
	Size   int64               `json:"size"`
}
type manifest struct {
	Format  string     `json:"format"`
	Streams []snapshot `json:"streams"`
}
type apiResponse struct {
	Error *struct {
		Code int `json:"code"`
	} `json:"error"`
}

func request(ctx context.Context, nc *client.Conn, subject string, input, output any) error {
	data, err := json.Marshal(input)
	if input == nil {
		data = nil
	}
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	msg, err := nc.RequestWithContext(requestCtx, subject, data)
	if err != nil {
		return err
	}
	var status apiResponse
	if checkErr := json.Unmarshal(msg.Data, &status); checkErr != nil {
		return checkErr
	}
	if status.Error != nil {
		return fmt.Errorf("JetStream request failed (code %d)", status.Error.Code)
	}
	if output != nil {
		return json.Unmarshal(msg.Data, output)
	}
	return nil
}
func names(ctx context.Context, nc *client.Conn) ([]string, error) {
	var all []string
	for {
		var page struct {
			Total   int      `json:"total"`
			Offset  int      `json:"offset"`
			Streams []string `json:"streams"`
		}
		if checkErr := request(ctx, nc, "$JS.API.STREAM.NAMES", map[string]int{"offset": len(all)}, &page); checkErr != nil {
			return nil, checkErr
		}
		if page.Total > maxStreams || page.Total < 0 || page.Offset != len(all) || len(all)+len(page.Streams) > page.Total {
			return nil, errors.New("unsupported JetStream inventory")
		}
		for _, name := range page.Streams {
			if !streamName.MatchString(name) || slices.Contains(all, name) {
				return nil, errors.New("invalid JetStream stream identity")
			}
			all = append(all, name)
		}
		if len(all) == page.Total {
			slices.Sort(all)
			return all, nil
		}
		if len(page.Streams) == 0 {
			return nil, errors.New("incomplete JetStream inventory")
		}
	}
}
func validSnapshot(s snapshot) error {
	if !streamName.MatchString(s.Config.Name) || s.Config.Storage != client.FileStorage || s.Config.Replicas != 1 || s.Config.Mirror != nil || len(s.Config.Sources) > 0 || s.Config.Placement != nil || s.Size <= 0 || s.Size > maxArchive || s.State.Consumers < 0 || s.State.Consumers > maxConsumers {
		return errors.New("JetStream backups require local file streams with one replica and no mirrors, sources or placement, and at most 1000 consumers per stream")
	}
	return nil
}
func (d Database) Probe(ctx context.Context) error {
	nc, err := d.Connect(ctx, true)
	if err != nil {
		return err
	}
	defer nc.Close()
	return request(ctx, nc, "$JS.API.INFO", nil, nil)
}
func (d Database) Export(ctx context.Context, files *backupfiles.Store, key string) (backupfiles.Object, error) {
	nc, err := d.Connect(ctx, false)
	if err != nil {
		return backupfiles.Object{}, err
	}
	defer nc.Close()
	streams, err := names(ctx, nc)
	if err != nil {
		return backupfiles.Object{}, err
	}
	directory, err := os.MkdirTemp("", "loomplane-jetstream-")
	if err != nil {
		return backupfiles.Object{}, err
	}
	defer os.RemoveAll(directory)
	m := manifest{Format: "nats-jetstream-v1"}
	remaining := maxArchive - (2 << 20)
	for i, name := range streams {
		entry, snapshotErr := takeSnapshot(ctx, nc, name, fmt.Sprintf("%s/%d", directory, i), remaining)
		if snapshotErr != nil {
			return backupfiles.Object{}, snapshotErr
		}
		remaining -= entry.Size
		m.Streams = append(m.Streams, entry)
	}
	after, err := names(ctx, nc)
	if err != nil {
		return backupfiles.Object{}, err
	}
	if !slices.Equal(streams, after) {
		return backupfiles.Object{}, errors.New("JetStream streams changed during backup; retry after quiescing writers")
	}
	return dbexec.Export(ctx, files, key, func(_ context.Context, out io.Writer) error {
		tw := tar.NewWriter(out)
		data, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if len(data) > 1<<20 {
			return errors.New("JetStream manifest exceeds 1 MiB")
		}
		if checkErr := tarEntry(tw, "manifest.json", int64(len(data))); checkErr != nil {
			return checkErr
		}
		if _, writeErr := tw.Write(data); writeErr != nil {
			return writeErr
		}
		for i, s := range m.Streams {
			if err != nil {
				break
			}
			err = tarEntry(tw, fmt.Sprintf("%d.snapshot", i), s.Size)
			if err != nil {
				break
			}
			var f *os.File
			f, err = os.Open(fmt.Sprintf("%s/%d", directory, i))
			if err != nil {
				break
			}
			_, err = io.Copy(tw, f)
			err = errors.Join(err, f.Close())
		}
		return errors.Join(err, tw.Close())
	})
}
func tarEntry(tw *tar.Writer, name string, size int64) error {
	return tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, Typeflag: tar.TypeReg})
}
func takeSnapshot(ctx context.Context, nc *client.Conn, name, path string, limit int64) (s snapshot, err error) {
	inbox := client.NewInbox()
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		return s, err
	}
	defer func() { err = errors.Join(err, sub.Unsubscribe()) }()
	if checkErr := sub.SetPendingLimits(256, 16<<20); checkErr != nil {
		return s, checkErr
	}
	flushCtx, cancelFlush := context.WithTimeout(ctx, 10*time.Second)
	defer cancelFlush()
	if checkErr := nc.FlushWithContext(flushCtx); checkErr != nil {
		return s, checkErr
	}
	err = request(ctx, nc, "$JS.API.STREAM.SNAPSHOT."+name, map[string]any{"deliver_subject": inbox, "chunk_size": 64 << 10, "no_consumers": false, "jsck": true}, &s)
	if err != nil {
		return s, err
	}
	s.Size = 1
	if s.Config.Name != name {
		return s, errors.New("snapshot stream identity differs")
	}
	if checkErr := validSnapshot(s); checkErr != nil {
		return s, checkErr
	}
	s.Size = 0
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return s, err
	}
	defer f.Close()
	for {
		nextCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		msg, nextErr := sub.NextMsgWithContext(nextCtx)
		cancel()
		if nextErr != nil {
			return s, nextErr
		}
		if status := msg.Header.Get("Status"); status != "" && status != "204" {
			return s, errors.New("JetStream snapshot transfer failed")
		}
		if len(msg.Data) == 0 {
			break
		}
		s.Size += int64(len(msg.Data))
		if s.Size > limit {
			return s, errors.New("JetStream backup exceeds the 2 GiB archive limit")
		}
		if _, err = f.Write(msg.Data); err != nil {
			return s, err
		}
		if msg.Reply != "" {
			if checkErr := msg.Respond(nil); checkErr != nil {
				return s, checkErr
			}
		}
	}
	return s, validSnapshot(s)
}

func readManifest(tr *tar.Reader) (manifest, error) {
	var m manifest
	h, err := tr.Next()
	if err != nil {
		return m, err
	}
	if h.Name != "manifest.json" || h.Typeflag != tar.TypeReg || h.Size > 1<<20 {
		return m, errors.New("invalid JetStream archive manifest")
	}
	if checkErr := json.NewDecoder(tr).Decode(&m); checkErr != nil {
		return m, checkErr
	}
	if m.Format != "nats-jetstream-v1" || len(m.Streams) > maxStreams {
		return m, errors.New("unsupported JetStream archive")
	}
	seen := map[string]bool{}
	for _, s := range m.Streams {
		if checkErr := validSnapshot(s); checkErr != nil {
			return m, checkErr
		}
		if seen[s.Config.Name] {
			return m, errors.New("duplicate JetStream stream")
		}
		seen[s.Config.Name] = true
	}
	return m, nil
}
func snapshotEntry(tr *tar.Reader, i int, s snapshot) error {
	h, err := tr.Next()
	if err != nil {
		return err
	}
	if h.Name != fmt.Sprintf("%d.snapshot", i) || h.Typeflag != tar.TypeReg || h.Size != s.Size {
		return errors.New("invalid JetStream snapshot entry")
	}
	return nil
}
func (d Database) Restore(ctx context.Context, files *backupfiles.Store, object backupfiles.Object) error {
	if !d.Recovery {
		return errors.New("restore requires an isolated JetStream recovery instance")
	}
	if object.Size > maxArchive {
		return errors.New("JetStream archive exceeds 2 GiB")
	}
	f, err := os.CreateTemp("", "loomplane-jetstream-restore-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if checkErr := files.CopyVerified(ctx, object, f); checkErr != nil {
		return checkErr
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	tr := tar.NewReader(f)
	m, err := readManifest(tr)
	if err != nil {
		return err
	}
	// Validate the entire archive before opening the destination connection.
	for i, s := range m.Streams {
		if checkErr := snapshotEntry(tr, i, s); checkErr != nil {
			return checkErr
		}
		if _, err = io.CopyN(io.Discard, tr, s.Size); err != nil {
			return err
		}
	}
	if _, err = tr.Next(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected JetStream archive entry")
	}
	nc, err := d.Connect(ctx, false)
	if err != nil {
		return err
	}
	defer nc.Close()
	existing, err := names(ctx, nc)
	if err != nil {
		return err
	}
	if len(existing) != 0 {
		return errors.New("JetStream recovery destination must be empty")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	tr = tar.NewReader(f)
	if _, err = readManifest(tr); err != nil {
		return err
	}
	for i, s := range m.Streams {
		if checkErr := snapshotEntry(tr, i, s); checkErr != nil {
			return checkErr
		}
		if checkErr := restoreStream(ctx, nc, s, tr); checkErr != nil {
			return checkErr
		}
	}
	return d.Probe(ctx)
}
func restoreStream(ctx context.Context, nc *client.Conn, s snapshot, r io.Reader) error {
	var target struct {
		Deliver string `json:"deliver_subject"`
	}
	if checkErr := request(ctx, nc, "$JS.API.STREAM.RESTORE."+s.Config.Name, s, &target); checkErr != nil {
		return checkErr
	}
	if target.Deliver == "" {
		return errors.New("JetStream returned no restore subject")
	}
	buffer := make([]byte, 64<<10)
	for {
		count, err := r.Read(buffer)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if count > 0 {
			c, cancel := context.WithTimeout(ctx, 30*time.Second)
			msg, sendErr := nc.RequestWithContext(c, target.Deliver, buffer[:count])
			cancel()
			if sendErr != nil {
				return sendErr
			}
			if len(msg.Data) != 0 {
				return errors.New("JetStream refused a restore chunk")
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if checkErr := request(ctx, nc, target.Deliver, nil, nil); checkErr != nil {
		return checkErr
	}
	var info client.StreamInfo
	if checkErr := request(ctx, nc, "$JS.API.STREAM.INFO."+s.Config.Name, nil, &info); checkErr != nil {
		return checkErr
	}
	if info.State.Msgs != s.State.Msgs || info.State.FirstSeq != s.State.FirstSeq || info.State.LastSeq != s.State.LastSeq || info.State.Consumers != s.State.Consumers {
		return errors.New("restored JetStream state differs from the snapshot")
	}
	return pausePushConsumers(ctx, nc, s.Config.Name, s.State.Consumers)
}

// Native snapshots retain push delivery subjects. Pausing push consumers keeps
// inspection subscriptions from advancing delivery or AckNone retention state.
func pausePushConsumers(ctx context.Context, nc *client.Conn, stream string, expected int) error {
	seen := map[string]bool{}
	until := time.Now().UTC().Add(100 * 365 * 24 * time.Hour)
	for {
		var page struct {
			Total     int                   `json:"total"`
			Offset    int                   `json:"offset"`
			Consumers []client.ConsumerInfo `json:"consumers"`
		}
		if checkErr := request(ctx, nc, "$JS.API.CONSUMER.LIST."+stream, map[string]int{"offset": len(seen)}, &page); checkErr != nil {
			return checkErr
		}
		if page.Total != expected || page.Offset != len(seen) || len(seen)+len(page.Consumers) > expected {
			return errors.New("restored JetStream consumer inventory differs")
		}
		for _, consumer := range page.Consumers {
			if consumer.Name == "" || strings.ContainsAny(consumer.Name, ".*> \t\r\n") || seen[consumer.Name] {
				return errors.New("invalid restored JetStream consumer identity")
			}
			seen[consumer.Name] = true
			if consumer.Config.DeliverSubject == "" {
				continue
			}
			var paused struct {
				Paused bool      `json:"paused"`
				Until  time.Time `json:"pause_until"`
			}
			if checkErr := request(ctx, nc, "$JS.API.CONSUMER.PAUSE."+stream+"."+consumer.Name, map[string]time.Time{"pause_until": until}, &paused); checkErr != nil {
				return checkErr
			}
			if !paused.Paused || !paused.Until.Equal(until) {
				return errors.New("restored JetStream push consumer was not paused")
			}
		}
		if len(seen) == expected {
			return nil
		}
		if len(page.Consumers) == 0 {
			return errors.New("incomplete restored JetStream consumer inventory")
		}
	}
}
