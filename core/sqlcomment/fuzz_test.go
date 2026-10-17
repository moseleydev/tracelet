package sqlcomment

import "testing"

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"SELECT 1 /*route='%2Fx',traceparent='00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01'*/",
		"SELECT 1 /*a='\\'',b='%'*/;", "/**/", "x /*='*/", "x /*a='*/", "*/",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, q string) {
		stripped, fields := Parse(q)
		if fields == nil && stripped != q {
			t.Fatalf("query changed without a comment: %q -> %q", q, stripped)
		}
		fields.TraceID()
		fields.Route()
	})
}
