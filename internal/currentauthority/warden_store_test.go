package currentauthority

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xraph/warden"
	"github.com/xraph/warden/store"
)

type recordingStore struct {
	method string
	args   []any
	calls  int
	err    error
	value  any
	hook   func(context.Context)
}

func (s *recordingStore) record(method string, args ...any) error {
	s.calls++
	s.method, s.args = method, args
	if s.hook != nil {
		ctx, ok := args[0].(context.Context)
		if !ok {
			panic("context missing")
		}
		s.hook(ctx)
	}
	return s.err
}
func (s *recordingStore) Close() error { return s.record("Close") }
func recordedValue[T any](s *recordingStore) T {
	var zero T
	if s.value == nil {
		return zero
	}
	value, ok := s.value.(T)
	if !ok {
		panic("unexpected recorded result type")
	}
	return value
}

func routerAttempt(t *testing.T, reader store.Store) (*Coordinator, *Attempt) {
	t.Helper()
	c := &Coordinator{reader: reader, profile: &profile{}}
	ctx, cancel := context.WithTimeout(context.Background(), AttemptLimit)
	t.Cleanup(cancel)
	a := &Attempt{owner: c, profile: c.profile, fenced: true, gate: make(chan struct{}, 1), cancel: cancel}
	a.ctx = context.WithValue(ctx, scopeKey{}, a)
	a.gate <- struct{}{}
	c.active = map[*Attempt]struct{}{a: {}}
	return c, a
}

type methodContract struct {
	Method    string `json:"method"`
	Signature string `json:"signature"`
	Current   string `json:"current"`
	Ordinary  string `json:"ordinary"`
	Absence   string `json:"current_expected_absence"`
}

//go:embed testdata/warden_store_methods.json
var storeMethodManifest []byte

func methodContracts(t *testing.T) []methodContract {
	t.Helper()
	data := storeMethodManifest
	var manifest struct {
		Methods []methodContract `json:"methods"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest.Methods
}

func sampleValue(typ reflect.Type, n int) reflect.Value {
	v := reflect.New(typ).Elem()
	switch typ.Kind() {
	case reflect.String:
		v.SetString(fmt.Sprintf("arg-%d", n))
	case reflect.Int, reflect.Int64:
		v.SetInt(int64(n + 1))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Pointer:
		v.Set(reflect.New(typ.Elem()))
	case reflect.Slice:
		v.Set(reflect.MakeSlice(typ, 1, 1))
		v.Index(0).Set(sampleValue(typ.Elem(), n))
	case reflect.Map:
		v.Set(reflect.MakeMap(typ))
		v.SetMapIndex(sampleValue(typ.Key(), n), sampleValue(typ.Elem(), n))
	case reflect.Struct:
		if typ == reflect.TypeFor[time.Time]() {
			v.Set(reflect.ValueOf(time.Unix(int64(n+1), 0)))
		}
	}
	return v
}

func TestEveryStoreRoute(t *testing.T) {
	contracts := methodContracts(t)
	iface := reflect.TypeFor[store.Store]()
	if len(contracts) != 81 || iface.NumMethod() != len(contracts) {
		t.Fatalf("manifest=%d installed=%d", len(contracts), iface.NumMethod())
	}
	seen := map[string]bool{}
	classifications := map[string]int{}
	for _, m := range contracts {
		if seen[m.Method] {
			t.Fatal("duplicate contract", m.Method)
		}
		seen[m.Method] = true
		method, ok := iface.MethodByName(m.Method)
		if !ok {
			t.Fatal("unknown contract", m.Method)
		}
		if got, want := method.Type.String(), contractSignature(t, m.Signature); got != want {
			t.Fatalf("%s signature=%s manifest=%s", m.Method, got, want)
		}
		classifications[m.Current]++
		if m.Method == "Close" {
			if m.Current != "context_free_close" || m.Ordinary != "coordinator_close_only" {
				t.Fatal("wrong Close contract")
			}
			continue
		}
		if (m.Current != "read" && m.Current != "reject_and_poison") || m.Ordinary != "delegate_original" {
			t.Fatal("unclassified method", m.Method)
		}
		for _, mode := range []string{"ordinary", "current", "expired", "foreign", "unfenced", "canceled"} {
			t.Run(m.Method+"/"+mode, func(t *testing.T) {
				ordinary, reader := &recordingStore{}, &recordingStore{}
				c, a := routerAttempt(t, reader)
				r := NewStoreRouter(c, ordinary)
				ctx := context.Background()
				if mode != "ordinary" {
					ctx = a.ctx
				}
				switch mode {
				case "expired":
					a.phase = sealed
				case "foreign":
					r = NewStoreRouter(&Coordinator{}, ordinary)
				case "unfenced":
					a.fenced = false
				case "canceled":
					a.cancel()
				}
				method := reflect.ValueOf(r).MethodByName(m.Method)
				args := []reflect.Value{reflect.ValueOf(ctx)}
				for i := 1; i < method.Type().NumIn(); i++ {
					args = append(args, sampleValue(method.Type().In(i), i))
				}
				var wantValue any
				if method.Type().NumOut() == 2 {
					wantValue = sampleValue(method.Type().Out(0), 99).Interface()
					ordinary.value = wantValue
					reader.value = wantValue
				}
				ordinary.err = errors.New("ordinary error must retain identity")
				results := method.Call(args)
				err, _ := results[len(results)-1].Interface().(error)
				expected := ordinary
				switch mode {
				case "ordinary":
					if err != ordinary.err { //nolint:errorlint // Forwarding must preserve the exact error object.
						t.Fatalf("ordinary error=%v", err)
					}
				case "current":
					if m.Current == "read" {
						expected = reader
						if err != nil {
							t.Fatal(err)
						}
					} else {
						if !errors.Is(err, ErrReadOnly) || a.poison == nil || ordinary.calls+reader.calls != 0 {
							t.Fatalf("mutation=%v calls=%d", err, ordinary.calls+reader.calls)
						}
						return
					}
				default:
					if err == nil || ordinary.calls+reader.calls != 0 {
						t.Fatalf("invalid scope=%v calls=%d", err, ordinary.calls+reader.calls)
					}
					return
				}
				if expected.calls != 1 || expected.method != m.Method || ordinary.calls+reader.calls != 1 {
					t.Fatal("wrong backend", ordinary.method, reader.method)
				}
				if mode == "ordinary" && expected.args[0] != ctx {
					t.Fatal("ordinary context replaced")
				}
				if mode == "current" {
					got, ok := expected.args[0].(context.Context)
					if !ok || got.Value(scopeKey{}) != a {
						t.Fatal("scope missing")
					}
					deadline, ok := got.Deadline()
					end, _ := a.ctx.Deadline()
					if !ok || deadline.After(end) {
						t.Fatal("unbounded reader context")
					}
				}
				for i := 1; i < len(args); i++ {
					if !reflect.DeepEqual(expected.args[i], args[i].Interface()) {
						t.Fatalf("argument %d changed", i)
					}
				}
				if len(results) == 2 && !reflect.DeepEqual(results[0].Interface(), wantValue) {
					t.Fatal("result changed")
				}
				if a.pending != 0 || len(a.gate) != 1 {
					t.Fatal("reader ownership leaked")
				}
			})
		}
	}
	if classifications["read"] != 40 || classifications["reject_and_poison"] != 40 || classifications["context_free_close"] != 1 {
		t.Fatal("classification totals", classifications)
	}
}

func TestReaderAbsenceAndBackendFailure(t *testing.T) {
	sentinel := map[string]error{
		"ErrRoleNotFound": warden.ErrRoleNotFound, "ErrPermissionNotFound": warden.ErrPermissionNotFound,
		"ErrAssignmentNotFound": warden.ErrAssignmentNotFound, "ErrRelationNotFound": warden.ErrRelationNotFound,
		"ErrPolicyNotFound": warden.ErrPolicyNotFound, "ErrResourceTypeNotFound": warden.ErrResourceTypeNotFound, "ErrCheckLogNotFound": warden.ErrCheckLogNotFound,
	}
	for _, m := range methodContracts(t) {
		if m.Current != "read" {
			continue
		}
		for _, joined := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/joined=%v", m.Method, joined), func(t *testing.T) {
				absence := sentinel[m.Absence]
				if absence == nil {
					absence = warden.ErrRoleNotFound
				}
				failure := fmt.Errorf("wrapped: %w", absence)
				if joined {
					failure = errors.Join(failure, errors.New("backend failure"))
				}
				reader := &recordingStore{err: failure}
				c, a := routerAttempt(t, reader)
				method := reflect.ValueOf(NewStoreRouter(c, &recordingStore{})).MethodByName(m.Method)
				args := []reflect.Value{reflect.ValueOf(a.ctx)}
				for i := 1; i < method.Type().NumIn(); i++ {
					args = append(args, reflect.Zero(method.Type().In(i)))
				}
				values := method.Call(args)
				if values[len(values)-1].Interface() != failure { //nolint:errorlint // The read must return the original backend error object.
					t.Fatal("error changed")
				}
				shouldPoison := m.Absence == "" || joined
				if (a.poison != nil) != shouldPoison {
					t.Fatalf("absence %q poison=%v", m.Absence, a.poison)
				}
			})
		}
	}
}

func TestRouterCloseOnlyOwnedResources(t *testing.T) {
	ordinary := &recordingStore{}
	c, a := routerAttempt(t, &recordingStore{})
	router := NewStoreRouter(c, ordinary)
	if err := router.Close(); !errors.Is(err, ErrBusy) || a.poison == nil {
		t.Fatalf("active Close=%v", err)
	}
	c.release(a)
	if err := router.Close(); err != nil {
		t.Fatal(err)
	}
	if err := router.Close(); err != nil {
		t.Fatal(err)
	}
	if ordinary.calls != 0 {
		t.Fatal("shared main closed")
	}
}

func TestReaderPanicAndPendingOwnership(t *testing.T) {
	t.Run("panic", func(t *testing.T) {
		reader := &recordingStore{hook: func(context.Context) { panic("backend panic") }}
		c, a := routerAttempt(t, reader)
		func() {
			defer func() {
				if recover() == nil {
					t.Error("panic lost")
				}
			}()
			_ = NewStoreRouter(c, &recordingStore{}).Ping(a.ctx)
		}()
		if a.pending != 0 || len(a.gate) != 1 || a.poison == nil {
			t.Fatal("panic leaked reader")
		}
	})
	t.Run("pending", func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		reader := &recordingStore{hook: func(context.Context) { close(entered); <-release }}
		c, a := routerAttempt(t, reader)
		r := NewStoreRouter(c, &recordingStore{})
		done := make(chan error, 1)
		go func() { done <- r.Ping(a.ctx) }()
		<-entered
		if err := r.Ping(a.ctx); !errors.Is(err, ErrPending) {
			t.Fatalf("concurrent read=%v", err)
		}
		close(release)
		<-done
		if a.pending != 0 || len(a.gate) != 1 {
			t.Fatal("reader did not drain")
		}
	})
}

func contractSignature(t *testing.T, signature string) string {
	t.Helper()
	expr, err := parser.ParseExpr("interface {" + signature + "}")
	if err != nil {
		t.Fatal(err)
	}
	contract, ok := expr.(*ast.InterfaceType)
	if !ok || len(contract.Methods.List) != 1 {
		t.Fatal("invalid contract")
	}
	function, ok := contract.Methods.List[0].Type.(*ast.FuncType)
	if !ok {
		t.Fatal("invalid function contract")
	}
	aliases := strings.NewReplacer("id.RoleID", "id.ID", "id.PermissionID", "id.ID", "id.AssignmentID", "id.ID", "id.RelationID", "id.ID", "id.PolicyID", "id.ID", "id.ResourceTypeID", "id.ID", "id.CheckLogID", "id.ID")
	types := func(fields *ast.FieldList) []string {
		var result []string
		for _, field := range fields.List {
			var text bytes.Buffer
			if err := format.Node(&text, token.NewFileSet(), field.Type); err != nil {
				t.Fatal(err)
			}
			count := max(len(field.Names), 1)
			for range count {
				result = append(result, aliases.Replace(text.String()))
			}
		}
		return result
	}
	result := "func(" + strings.Join(types(function.Params), ", ") + ")"
	returns := types(function.Results)
	if len(returns) == 1 {
		result += " " + returns[0]
	} else if len(returns) > 1 {
		result += " (" + strings.Join(returns, ", ") + ")"
	}
	return result
}

type spoofAbsenceError struct{}

func (spoofAbsenceError) Error() string { return "spoof" }
func (spoofAbsenceError) Is(error) bool { return true }

type cyclicAbsenceError struct{}

func (c *cyclicAbsenceError) Error() string { return "cycle" }
func (c *cyclicAbsenceError) Unwrap() error { return c }

func TestExpectedAbsenceRejectsOpaqueJoinedAndWrongSubsystemErrors(t *testing.T) {
	for _, err := range []error{sql.ErrNoRows, warden.ErrPermissionNotFound, spoofAbsenceError{}, errors.Join(warden.ErrRoleNotFound), &cyclicAbsenceError{}} {
		if expectedAbsence(err, warden.ErrRoleNotFound) {
			t.Fatalf("unexpected absence exception for %T", err)
		}
	}
	if !expectedAbsence(fmt.Errorf("role missing: %w", warden.ErrRoleNotFound), warden.ErrRoleNotFound) {
		t.Fatal("installed single-chain absence rejected")
	}
}
