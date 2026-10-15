package detectors

import "testing"

type fakeDetector struct{ name string }

func (f fakeDetector) Name() string { return f.name }

func (f fakeDetector) Detect(Input) []Finding {
	return []Finding{{Detector: f.name, Severity: Warning, Title: "found"}}
}

func TestRegisterAndRun(t *testing.T) {
	Register(fakeDetector{"zz-test-b"})
	Register(fakeDetector{"zz-test-a"})

	var got []string
	for _, f := range Run(Input{}) {
		got = append(got, f.Detector)
	}
	if len(got) < 2 || got[len(got)-2] != "zz-test-a" || got[len(got)-1] != "zz-test-b" {
		t.Fatalf("detectors should run sorted by name, got %v", got)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	Register(fakeDetector{"zz-test-dup"})
	defer func() {
		if recover() == nil {
			t.Fatal("registering a duplicate name should panic")
		}
	}()
	Register(fakeDetector{"zz-test-dup"})
}
