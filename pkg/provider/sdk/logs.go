package sdk

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const MaxLogLine = 16 << 10
const MaxOperationLogBytes = 1 << 20
const MaxOperationLogLines = 1024
const MaxLogOperations = 32
const MaxLogPage = 100

// OperationLogger opts a provider into the operation-log endpoint. Existing
// providers keep their progress callback and do not advertise this capability.
type OperationLogger interface{ OperationLogs() bool }

type LogLine struct {
	ID        string    `json:"id"`
	Stream    string    `json:"stream"`
	Message   string    `json:"message"`
	Time      time.Time `json:"time"`
	Truncated bool      `json:"truncated,omitempty"`
}

// LogPage returns stable IDs and an opaque cursor. Gap means some earlier
// output is unavailable; callers retain their existing output and show the gap.
type LogPage struct {
	Lines  []LogLine `json:"lines"`
	Cursor string    `json:"cursor"`
	More   bool      `json:"more"`
	Status string    `json:"status"`
	Gap    bool      `json:"gap,omitempty"`
	Reason string    `json:"reason,omitempty"`
}

type operationOutput struct {
	epoch      string
	lines      []LogLine
	seq        uint64
	bytes      int
	generation uint64
}

func logEpoch() string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(random[:])
}

func (s *server) enableOutput(id string) {
	if logger, ok := s.p.(OperationLogger); !ok || !logger.OperationLogs() {
		return
	}
	if s.logs == nil {
		s.logs = map[string]*operationOutput{}
	}
	if len(s.logs) >= MaxLogOperations {
		oldest := ""
		generation := ^uint64(0)
		for key, output := range s.logs {
			if output.generation < generation {
				oldest, generation = key, output.generation
			}
		}
		delete(s.logs, oldest)
	}
	s.logGeneration++
	s.logs[id] = &operationOutput{epoch: logEpoch(), generation: s.logGeneration}
}

func (s *server) appendOutput(id, epoch, stream, message string) {
	if stream != "stdout" && stream != "stderr" && stream != "system" {
		stream = "system"
	}
	truncated := len(message) > MaxLogLine
	if truncated {
		message = "[Log line omitted: exceeds the provider output limit.]"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	output := s.logs[id]
	if output == nil || output.epoch != epoch || s.ops[id].Status != StatusRunning {
		return
	}
	output.seq++
	line := LogLine{ID: output.epoch + ":" + strconv.FormatUint(output.seq, 10), Stream: stream, Message: message, Time: time.Now().UTC(), Truncated: truncated}
	output.lines = append(output.lines, line)
	output.bytes += len(message)
	for len(output.lines) > MaxOperationLogLines || output.bytes > MaxOperationLogBytes {
		output.bytes -= len(output.lines[0].Message)
		output.lines[0] = LogLine{}
		output.lines = output.lines[1:]
	}
}

func (s *server) operationLogs(w http.ResponseWriter, r *http.Request) {
	limit := MaxLogPage
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > MaxLogPage {
			fail(w, 400, errors.New("log page limit must be between 1 and 100"))
			return
		}
		limit = value
	}
	s.mu.Lock()
	id := r.PathValue("id")
	op := s.ops[id]
	if op == nil {
		s.mu.Unlock()
		fail(w, 404, errors.New("no such operation; start it again"))
		return
	}
	output := s.logs[id]
	page := LogPage{Lines: []LogLine{}, Status: op.Status}
	if output == nil {
		page.Gap, page.Reason = true, "expired"
		s.mu.Unlock()
		reply(w, 200, page)
		return
	}
	after := uint64(0)
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		if len(cursor) > 128 {
			s.mu.Unlock()
			fail(w, 400, errors.New("operation log cursor is too long"))
			return
		}
		epoch, sequence, found := strings.Cut(cursor, ":")
		parsed, err := strconv.ParseUint(sequence, 10, 64)
		switch {
		case !found || err != nil:
			page.Gap, page.Reason = true, "invalid"
		case epoch != output.epoch:
			page.Gap, page.Reason = true, "restarted"
		case parsed > output.seq:
			page.Gap, page.Reason = true, "future"
		default:
			after = parsed
		}
	}
	first := output.seq - uint64(len(output.lines)) + 1
	if after+1 < first {
		page.Gap = true
		if page.Reason == "" {
			page.Reason = "expired"
		}
		after = first - 1
	}
	for index := range output.lines {
		sequence := first + uint64(index)
		if sequence <= after {
			continue
		}
		if len(page.Lines) >= limit {
			page.More = true
			break
		}
		page.Lines = append(page.Lines, output.lines[index])
		after = sequence
	}
	page.Cursor = output.epoch + ":" + strconv.FormatUint(after, 10)
	s.mu.Unlock()
	reply(w, 200, page)
}
