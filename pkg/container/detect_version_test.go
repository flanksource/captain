package container

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectGoVersion(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n\ngo 1.24.2\n"), 0o644)

	if v := DetectGoVersion(dir); v != "1.24" {
		t.Errorf("expected 1.24, got %q", v)
	}
}

func TestDetectGoVersionMinor(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n\ngo 1.25\n"), 0o644)

	if v := DetectGoVersion(dir); v != "1.25" {
		t.Errorf("expected 1.25, got %q", v)
	}
}

func TestDetectGoVersionMissing(t *testing.T) {
	if v := DetectGoVersion(t.TempDir()); v != "" {
		t.Errorf("expected empty, got %q", v)
	}
}

func TestDetectPythonVersion(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".python-version"), []byte("3.12.1\n"), 0o644)

	if v := DetectPythonVersion(dir); v != "3.12.1" {
		t.Errorf("expected 3.12.1, got %q", v)
	}
}

func TestDetectPythonVersionMissing(t *testing.T) {
	if v := DetectPythonVersion(t.TempDir()); v != "" {
		t.Errorf("expected empty, got %q", v)
	}
}

func TestDetectRustVersion(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "rust-toolchain.toml"), []byte("[toolchain]\nchannel = \"1.80\"\n"), 0o644)

	if v := DetectRustVersion(dir); v != "1.80" {
		t.Errorf("expected 1.80, got %q", v)
	}
}

func TestDetectRustVersionMissing(t *testing.T) {
	if v := DetectRustVersion(t.TempDir()); v != "" {
		t.Errorf("expected empty, got %q", v)
	}
}

func TestNodeVersionFromPackageJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"engines":{"node":">=18"}}`), 0o644)

	if v := nodeVersionFromPackageJSON(dir); v != "18" {
		t.Errorf("expected 18, got %q", v)
	}
}

func TestNodeVersionFromPackageJSONCaret(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"engines":{"node":"^20.0.0"}}`), 0o644)

	if v := nodeVersionFromPackageJSON(dir); v != "20" {
		t.Errorf("expected 20, got %q", v)
	}
}

func TestNodeVersionFromPackageJSONMissing(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"test"}`), 0o644)

	if v := nodeVersionFromPackageJSON(dir); v != "" {
		t.Errorf("expected empty, got %q", v)
	}
}

func TestDetectVersions(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n\ngo 1.24.2\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"engines":{"node":">=22"}}`), 0o644)

	v := DetectVersions(dir)
	if v["golang"] != "1.24" {
		t.Errorf("expected golang=1.24, got %q", v["golang"])
	}
	if v["node"] != "22" {
		t.Errorf("expected node=22, got %q", v["node"])
	}
	if _, ok := v["python"]; ok {
		t.Error("expected no python version")
	}
}
