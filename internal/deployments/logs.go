package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/xraph/loomplane/internal/realtime"
)

type LogSink interface {
	AppendLog(context.Context, realtime.LogInput, *realtime.Redactor) (*realtime.Log, error)
	SaveCheckpoint(context.Context, realtime.LogInput, string, string, string, *realtime.Redactor) error
	Checkpoint(context.Context, realtime.LogInput) (*realtime.Collection, error)
}

type Collector struct {
	mu       sync.Mutex
	sink     LogSink
	ref      realtime.LogInput
	values   []string
	redactor *realtime.Redactor
	err      error
}

func (j *Job) Collector(operation, attempt string) *Collector {
	c := &Collector{sink: j.Logs, ref: realtime.LogInput{ActionID: j.Action.ID, OperationID: operation, Attempt: attempt}}
	if j.Deployment != nil {
		c.ref.OrgID = j.Deployment.OrgID
		c.ref.ProjectID = j.Deployment.ProjectID
		c.ref.DeploymentID = j.Deployment.ID
	}
	for key, value := range j.Env {
		if j.Secrets[key] {
			c.values = append(c.values, value)
		}
	}
	c.addCredential(j.Credential)
	return c
}

func (j *Job) LogAttempt() string {
	if j.Attempt != "" {
		return j.Attempt
	}
	return "1.1"
}

func (c *Collector) addCredential(raw []byte) {
	c.mu.Lock()
	defer func() { c.redactor = realtime.NewRedactor(c.values); c.mu.Unlock() }()
	c.values = append(c.values, string(raw))
	var value any
	if json.Unmarshal(raw, &value) != nil {
		if err := yaml.Unmarshal(raw, &value); err != nil {
			return
		}
	}
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case string:
			c.values = append(c.values, typed)
		case map[string]any:
			for _, v := range typed {
				walk(v)
			}
		case []any:
			for _, v := range typed {
				walk(v)
			}
		}
	}
	walk(value)
}

func (c *Collector) message(value string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.redactor.Line(value, realtime.MaxLineBytes)
}

func (c *Collector) Emit(ctx context.Context, line buildLine) error {
	if c.sink == nil {
		return nil
	}
	ref := c.ref
	ref.ProviderID, ref.Stream, ref.Source, ref.Level, ref.Time = line.ID, line.Stream, line.Source, line.Level, line.Time
	message, truncated := c.message(line.Message)
	ref.Message, ref.Truncated = message, truncated || line.Truncated
	_, err := c.sink.AppendLog(ctx, ref, nil)
	if err != nil {
		c.mu.Lock()
		if c.err == nil {
			c.err = err
		}
		c.mu.Unlock()
	}
	return err
}

func (c *Collector) State(ctx context.Context, position, state, message string) error {
	if c.sink == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c.mu.Lock()
	failure := c.err
	c.mu.Unlock()
	if state == "complete" && failure != nil {
		state, message = "error", "Some build output could not be retained. Deployment status is independent of log collection."
	}
	message, _ = c.message(message)
	return c.sink.SaveCheckpoint(ctx, c.ref, position, state, message, nil)
}

// RecordState keeps collection failures separate from the deployment result.
func (c *Collector) RecordState(ctx context.Context, position, state, message string) {
	if err := c.State(ctx, position, state, message); err != nil {
		c.mu.Lock()
		if c.err == nil {
			c.err = err
		}
		c.mu.Unlock()
	}
}

func (c *Collector) Saved(ctx context.Context) (*realtime.Collection, error) {
	if c.sink == nil {
		return nil, realtime.ErrNotFound
	}
	return c.sink.Checkpoint(ctx, c.ref)
}

func logAttempt(ctx context.Context, retries int) string {
	return strconv.Itoa(retries+1) + "." + strconv.FormatInt(realtime.ActivityAttempt(ctx), 10)
}

func (c *Collector) safeError(err error) error {
	if err == nil {
		return nil
	}
	message, _ := c.message(err.Error())
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", message, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", message, context.DeadlineExceeded)
	}
	return errors.New(message)
}
