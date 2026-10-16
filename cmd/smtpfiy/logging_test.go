package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLevelFilteringSuppressesBelowThreshold(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, nil, "info")

	logger.Debug("debug message")
	logger.Info("info message")
	logger.Warn("warn message")
	logger.Error("error message")

	out := buf.String()
	if strings.Contains(out, "debug message") {
		t.Fatalf("debug message should be suppressed at info level, got: %q", out)
	}
	for _, want := range []string{"info message", "warn message", "error message"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output, got: %q", want, out)
		}
	}
}

func TestDebugVisibleAtDebugLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, nil, "debug")
	logger.Debug("debug message")
	if !strings.Contains(buf.String(), "debug message") {
		t.Fatalf("debug message should appear at debug level, got: %q", buf.String())
	}
}

func TestDualOutputStdioAndFile(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "smtpfiy.log")

	var stdio bytes.Buffer
	logger, err := NewFromFile(&stdio, logFile, "info")
	if err != nil {
		t.Fatalf("NewFromFile: %v", err)
	}
	logger.Info("dual write message")
	logger.Close()

	fileContent, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}

	if !strings.Contains(stdio.String(), "dual write message") {
		t.Fatalf("stdio missing message: %q", stdio.String())
	}
	if !strings.Contains(string(fileContent), "dual write message") {
		t.Fatalf("file missing message: %q", string(fileContent))
	}
}

func TestSensitiveDataMaskedInOutput(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, nil, "debug")

	logger.MaskAndInfo("api key is %s", "sk_live_secret12345")

	out := buf.String()
	if strings.Contains(out, "sk_live_secret12345") {
		t.Fatalf("API key leaked into logs: %q", out)
	}
	if !strings.Contains(out, "***") {
		t.Fatalf("expected masked marker in output, got: %q", out)
	}
}

func TestFileWriteErrorHandledGracefully(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "nope", "smtpfiy.log")

	var stdio bytes.Buffer
	logger, err := NewFromFile(&stdio, logFile, "info")
	if err != nil {
		t.Fatalf("NewFromFile should not fail on bad path, got: %v", err)
	}
	logger.Info("still logs to stdio")
	if !strings.Contains(stdio.String(), "still logs to stdio") {
		t.Fatalf("expected stdio output despite file error, got: %q", stdio.String())
	}
}

func TestConfigDrivenInitialization(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "smtpfiy.log")

	var stdio bytes.Buffer
	logger, err := NewFromConfig(&stdio, "warn", logFile)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	logger.Info("suppressed")
	logger.Warn("shown")
	logger.Close()

	if strings.Contains(stdio.String(), "suppressed") {
		t.Fatalf("info should be suppressed at warn level: %q", stdio.String())
	}
	if !strings.Contains(stdio.String(), "shown") {
		t.Fatalf("warn should be shown: %q", stdio.String())
	}
}
