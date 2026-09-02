package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestDoctorReportCountsAndRendering(t *testing.T) {
	var report doctorReport
	report.add("project", doctorOK, "demo (/tmp/demo)", "")
	report.add("api-key", doctorFail, "no API key visible", "set OPENAI_API_KEY")
	report.add("index", doctorWarn, "not built", "run `vecgrep index`")
	if report.Failed != 1 || report.Warned != 1 {
		t.Fatalf("counts = failed %d warned %d", report.Failed, report.Warned)
	}

	var buf bytes.Buffer
	printDoctorReport(&buf, report)
	out := buf.String()
	for _, want := range []string{"✗  api-key", "set OPENAI_API_KEY", "!  index", "1 check(s) failed, 1 warning(s)."} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestLogServeEnvironmentWithoutProject(t *testing.T) {
	var buf bytes.Buffer
	logServeEnvironment(&buf, "")
	if !strings.Contains(buf.String(), "no project detected") {
		t.Fatalf("unexpected output: %q", buf.String())
	}
}

func TestCollectDoctorReportOutsideProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	report := collectDoctorReport(context.Background(), doctorOptions{StartDir: home, Ping: false})
	var sawProject, sawPingSkipped bool
	for _, c := range report.Checks {
		if c.Name == "project" && c.Status == doctorWarn {
			sawProject = true
		}
		if c.Name == "provider-ping" && strings.Contains(c.Detail, "skipped") {
			sawPingSkipped = true
		}
	}
	if !sawProject || !sawPingSkipped {
		t.Fatalf("checks = %+v", report.Checks)
	}
}
