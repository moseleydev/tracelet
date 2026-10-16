package sqlcomment

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/moseleydev/tracelet/core"
)

type vectors struct {
	Cases []struct {
		Name      string            `json:"name"`
		SQL       string            `json:"sql"`
		Fields    map[string]string `json:"fields"`
		Commented string            `json:"commented"`
		TraceID   string            `json:"trace_id"`
		Route     string            `json:"route"`
	} `json:"cases"`
}

// The same cases the Node and Python SDKs write, so the formats agree.
func TestSharedVectors(t *testing.T) {
	raw, err := os.ReadFile("../../sdks/testdata/sqlcommenter.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Cases {
		stripped, f := Parse(c.Commented)
		if f.TraceID() != c.TraceID || f.Route() != c.Route {
			t.Errorf("%s: trace %q route %q, want %q %q", c.Name, f.TraceID(), f.Route(), c.TraceID, c.Route)
		}
		if c.Route != "" {
			want := c.SQL
			if want[len(want)-1] == ';' {
				want = want[:len(want)-1]
			}
			if stripped != want {
				t.Errorf("%s: stripped = %q, want %q", c.Name, stripped, want)
			}
			for k, val := range c.Fields {
				if f[k] != val {
					t.Errorf("%s: field %s = %q, want %q", c.Name, k, f[k], val)
				}
			}
		}
	}
}

func TestParseRejectsOrdinaryComments(t *testing.T) {
	for _, q := range []string{
		"SELECT 1",
		"SELECT 1 /* just a note */",
		"SELECT 1 /*route='unterminated*/",
		"SELECT 1 /*route=noquotes*/",
		"/*route='x'*/ SELECT 1", // leading comments are not sqlcommenter
		"SELECT 1 /*a='1' b='2'*/",
	} {
		if s, f := Parse(q); f != nil || s != q {
			t.Errorf("Parse(%q) = %q, %v; want unchanged, nil", q, s, f)
		}
	}
}

func TestTraceIDValidation(t *testing.T) {
	for tp, want := range map[string]string{
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01": "4bf92f3577b34da6a3ce929d0e0e4736",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01": "", // invalid all-zero id
		"00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01": "", // must be lowercase
		"garbage": "",
	} {
		if got := (Fields{"traceparent": tp}).TraceID(); got != want {
			t.Errorf("TraceID(%q) = %q, want %q", tp, got, want)
		}
	}
}

func TestAnnotate(t *testing.T) {
	e := core.Event{Query: "SELECT 1 /*route='%2Fx',traceparent='00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'*/"}
	if key := Annotate(&e); key != "SELECT 1" || e.Route != "/x" || e.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("Annotate: key %q, event %+v", key, e)
	}
	plain := core.Event{Query: "SELECT 2"}
	if key := Annotate(&plain); key != "SELECT 2" || plain.Route != "" {
		t.Fatalf("plain query changed: %q %+v", key, plain)
	}
}
