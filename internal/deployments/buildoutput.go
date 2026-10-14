package deployments

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type buildLine struct {
	ID, Stream, Source, Level, Message string
	Key                                string
	Truncated                          bool
	Time                               time.Time
}

type wireField struct {
	bytes []byte
	value uint64
}

// BuildKit's Engine API trace is StatusResponse, not JSON log text. Read only
// the stable vertex/log fields and skip unknown fields for forward compatibility.
// Wire contract: moby/buildkit api/services/control/control.proto.
func traceFields(data []byte) (map[protowire.Number][]wireField, error) {
	out := map[protowire.Number][]wireField{}
	for len(data) > 0 {
		number, kind, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		data = data[n:]
		field := wireField{}
		switch kind {
		case protowire.BytesType:
			field.bytes, n = protowire.ConsumeBytes(data)
		case protowire.VarintType:
			field.value, n = protowire.ConsumeVarint(data)
		default:
			n = protowire.ConsumeFieldValue(number, kind, data)
		}
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		out[number] = append(out[number], field)
		data = data[n:]
	}
	return out, nil
}

func firstField(fields map[protowire.Number][]wireField, number protowire.Number) wireField {
	if len(fields[number]) == 0 {
		return wireField{}
	}
	return fields[number][0]
}

func buildkitLines(data []byte, seen map[string]string) ([]buildLine, error) {
	fields, err := traceFields(data)
	if err != nil {
		return nil, err
	}
	out := []buildLine{}
	for _, vertex := range fields[1] {
		v, decodeErr := traceFields(vertex.bytes)
		if decodeErr != nil {
			return nil, decodeErr
		}
		id, name := string(firstField(v, 1).bytes), string(firstField(v, 3).bytes)
		state := "starting"
		if len(v[6]) > 0 {
			state = "complete"
		}
		if firstField(v, 4).value != 0 {
			state = "cached"
		}
		message, level := name+": "+state, "info"
		if failure := string(firstField(v, 7).bytes); failure != "" {
			state = "failed"
			message, level = name+": "+failure, "error"
		}
		if name != "" && seen[id] != state {
			if len(seen) < 4096 {
				seen[id] = state
			}
			out = append(out, buildLine{Stream: "system", Source: "buildkit", Level: level, Message: message})
		}
	}
	for _, log := range fields[3] {
		v, decodeErr := traceFields(log.bytes)
		if decodeErr != nil {
			return nil, decodeErr
		}
		stream := "stdout"
		if firstField(v, 3).value == 2 {
			stream = "stderr"
		}
		var timestamp timestamppb.Timestamp
		if raw := firstField(v, 2).bytes; len(raw) > 0 {
			if timestampErr := proto.Unmarshal(raw, &timestamp); timestampErr != nil {
				return nil, timestampErr
			}
		}
		out = append(out, buildLine{Stream: stream, Source: "buildkit", Level: "info", Key: string(firstField(v, 1).bytes), Message: string(firstField(v, 4).bytes), Time: timestamp.AsTime()})
	}
	for _, warning := range fields[4] {
		v, decodeErr := traceFields(warning.bytes)
		if decodeErr != nil {
			return nil, decodeErr
		}
		out = append(out, buildLine{Stream: "system", Source: "buildkit", Level: "warning", Message: string(firstField(v, 3).bytes)})
	}
	return out, nil
}

// readBuildOutput drains to the terminal result even when retaining a line
// fails. The collector records its own error separately from deployment state.
func readBuildOutput(input io.Reader, emit func(buildLine) error) (string, error) {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	seen := map[string]string{}
	image := ""
	sequence := uint64(0)
	pending := map[string]buildLine{}
	discard := map[string]bool{}
	write := func(line buildLine) {
		sequence++
		line.ID = strconv.FormatUint(sequence, 10)
		if err := emit(line); err != nil {
			return
		}
	}
	flush := func() {
		for _, line := range pending {
			if line.Message != "" {
				write(line)
			}
		}
	}
	defer flush()
	for scanner.Scan() {
		var frame struct {
			ID, Stream, Status, Error string
			Aux                       json.RawMessage
			ErrorDetail               struct{ Message string }
		}
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			return image, fmt.Errorf("decode Docker build output: %w", err)
		}
		if frame.ErrorDetail.Message != "" {
			frame.Error = frame.ErrorDetail.Message
		}
		if frame.Error != "" {
			write(buildLine{Stream: "system", Source: "docker", Level: "error", Message: frame.Error})
			return image, fmt.Errorf("remote Docker build: %s", frame.Error)
		}
		lines := []buildLine{}
		switch {
		case frame.ID == "moby.buildkit.trace":
			var encoded string
			if err := json.Unmarshal(frame.Aux, &encoded); err != nil {
				return image, fmt.Errorf("decode BuildKit trace: %w", err)
			}
			data, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return image, err
			}
			lines, err = buildkitLines(data, seen)
			if err != nil {
				return image, err
			}
		case frame.ID == "moby.image.id" || (len(frame.Aux) > 0 && frame.Aux[0] == '{'):
			var result struct{ ID, Digest string }
			if err := json.Unmarshal(frame.Aux, &result); err != nil {
				return image, err
			}
			if result.ID != "" {
				image = result.ID
			}
			if result.Digest != "" {
				image = result.Digest
			}
		case frame.Stream != "":
			lines = append(lines, buildLine{Stream: "stdout", Source: "docker", Level: "info", Message: frame.Stream})
		case frame.Status != "":
			// Docker 29 can report a successful push in the final status
			// record instead of an aux.Digest object.
			fields := strings.Fields(frame.Status)
			for index, field := range fields {
				if field == "digest:" && index+1 < len(fields) && imageDigest.MatchString(fields[index+1]) {
					image = fields[index+1]
				}
			}
			lines = append(lines, buildLine{Stream: "system", Source: "docker", Level: "info", Message: frame.ID + " " + frame.Status})
		}
		for _, line := range lines {
			if line.Stream == "system" {
				write(line)
				continue
			}
			key := line.Source + "/" + line.Stream + "/" + line.Key
			if _, found := pending[key]; !found && len(pending) >= 128 {
				continue
			}
			if discard[key] {
				_, rest, found := strings.Cut(line.Message, "\n")
				if !found {
					continue
				}
				line.Message = rest
				delete(discard, key)
			}
			if old, found := pending[key]; found {
				line.Message = old.Message + line.Message
			}
			for {
				message, rest, found := strings.Cut(line.Message, "\n")
				if !found {
					break
				}
				complete := line
				complete.Message = message
				write(complete)
				line.Message = rest
			}
			// Omit an oversized unterminated line as a whole. Splitting it
			// before redaction could expose a secret across chunk boundaries.
			if len(line.Message) > 1<<20 {
				write(buildLine{Stream: "system", Source: line.Source, Level: "warning", Message: "Build output line exceeded its capture limit and was omitted.", Truncated: true})
				line.Message = ""
				discard[key] = true
			}
			pending[key] = line
			total := 0
			for _, buffer := range pending {
				total += len(buffer.Message)
			}
			if total > 4<<20 {
				write(buildLine{Stream: "system", Source: line.Source, Level: "warning", Message: "Build output buffers reached their capture limit. An unterminated line was omitted.", Truncated: true})
				line.Message = ""
				pending[key] = line
				discard[key] = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return image, fmt.Errorf("read remote build output: %w", err)
	}
	if sequence == 0 && image == "" {
		return "", errors.New("the builder returned no output or image")
	}
	return image, nil
}
