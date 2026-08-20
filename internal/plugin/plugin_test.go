package plugin

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const manifestFixture = `apiVersion: digitalasset.com/v1
kind: SdkManifest
spec:
  components:
    canton-open-source:
      version: 3.5.1
    damlc:
      version: 3.5.1
  assistant:
    version: 1.0.17
  version: 3.5.1
  edition: open-source
`

func writeManifest(t *testing.T, home, sdkVersion string) string {
	t.Helper()
	dir := filepath.Join(home, "cache", "sdk", "open-source")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sdkVersion+".yaml")
	if err := os.WriteFile(path, []byte(manifestFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The entry must land as the last component, immediately before `assistant:`.
func TestRegisterInManifestInsertsBeforeAssistant(t *testing.T) {
	home := t.TempDir()
	manifest := writeManifest(t, home, "3.5.1")

	if err := RegisterInManifest(manifest, "dpm-trace", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")

	entry, assistant := -1, -1
	for i, line := range lines {
		if strings.HasPrefix(line, "    dpm-trace:") {
			entry = i
		}
		if strings.HasPrefix(line, "  assistant:") {
			assistant = i
		}
	}
	if entry < 0 {
		t.Fatalf("component not registered:\n%s", data)
	}
	if entry > assistant {
		t.Errorf("entry at %d is after assistant at %d", entry, assistant)
	}
	if lines[entry+1] != "      version: 0.1.0" {
		t.Errorf("version line = %q", lines[entry+1])
	}
}

// Re-running install must not duplicate the entry.
func TestRegisterInManifestIsIdempotent(t *testing.T) {
	home := t.TempDir()
	manifest := writeManifest(t, home, "3.5.1")

	for i := 0; i < 3; i++ {
		if err := RegisterInManifest(manifest, "dpm-trace", "0.1.0"); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(manifest)
	if count := strings.Count(string(data), "    dpm-trace:"); count != 1 {
		t.Errorf("entry appears %d times, want 1", count)
	}
}

func TestInstallWritesComponentAndBinary(t *testing.T) {
	home := t.TempDir()
	writeManifest(t, home, "3.5.1")

	binary := filepath.Join(t.TempDir(), "dpm-trace")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := Install(&out, Options{
		DPMHome: home, SDKVersion: "3.5.1", ComponentVersion: "0.1.0", BinaryPath: binary,
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	componentDir := filepath.Join(home, "cache", "components", "dpm-trace", "0.1.0")
	descriptor, err := os.ReadFile(filepath.Join(componentDir, "component.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(descriptor), "name: trace") {
		t.Errorf("component.yaml does not declare the trace command:\n%s", descriptor)
	}

	// The installed binary must be executable, or dpm cannot run it.
	info, err := os.Stat(filepath.Join(componentDir, "bin", "dpm-trace"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("installed binary is not executable: %v", info.Mode())
	}
}

func TestInstallReportsMissingManifest(t *testing.T) {
	home := t.TempDir()
	err := Install(&bytes.Buffer{}, Options{DPMHome: home, SDKVersion: "9.9.9", ComponentVersion: "0.1.0"})
	if err == nil {
		t.Fatal("expected an error for a missing manifest")
	}
	if !strings.Contains(err.Error(), "SDK manifest not found") {
		t.Errorf("error = %v", err)
	}
}

func TestDetectSDKVersionPicksHighestInstalled(t *testing.T) {
	home := t.TempDir()
	writeManifest(t, home, "3.4.11")
	writeManifest(t, home, "3.5.1")
	if got := DetectSDKVersion(home); got != "3.5.1" {
		t.Errorf("DetectSDKVersion = %q, want 3.5.1", got)
	}
}

// The DPM home is chosen explicitly, then from DPM_HOME, then ~/.dpm. Getting
// this wrong writes the component where dpm will not look for it.
func TestResolveHomePrecedence(t *testing.T) {
	t.Setenv("DPM_HOME", "/from/env")

	if got, err := resolveHome("/explicit"); err != nil || got != "/explicit" {
		t.Errorf("explicit = %q, %v", got, err)
	}
	if got, err := resolveHome(""); err != nil || got != "/from/env" {
		t.Errorf("env = %q, %v", got, err)
	}

	os.Unsetenv("DPM_HOME")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got, err := resolveHome(""); err != nil || got != filepath.Join(home, ".dpm") {
		t.Errorf("default = %q, %v", got, err)
	}
}

func TestResolveHomeExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	got, err := resolveHome("~/custom-dpm")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(home, "custom-dpm") {
		t.Errorf("got %q", got)
	}
}

// Without an SDK manifest there is nothing to register into, and the error has
// to say how to fix it rather than just failing.
func TestInstallWithoutAnySDKExplainsItself(t *testing.T) {
	home := t.TempDir()
	err := Install(io.Discard, Options{DPMHome: home})
	if err == nil {
		t.Fatal("installing with no SDK returned no error")
	}
	if !strings.Contains(err.Error(), "--sdk-version") {
		t.Errorf("error does not suggest a fix: %v", err)
	}
}

// The binary is written via a temporary file and renamed, so a failed copy
// cannot leave a half-written executable that dpm would then try to run.
func TestCopyExecutableIsAtomicAndExecutable(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source-bin")
	if err := os.WriteFile(source, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "dest-bin")
	if err := copyExecutable(source, dest); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("mode = %v, want it executable", info.Mode().Perm())
	}
	if _, err := os.Stat(dest + ".tmp"); err == nil {
		t.Error("the temporary file was left behind")
	}
	if err := copyExecutable(filepath.Join(dir, "absent"), dest); err == nil {
		t.Error("copying a missing source returned no error")
	}
}

// A home with no manifests at all resolves to no version rather than guessing.
func TestDetectSDKVersionWithoutManifests(t *testing.T) {
	if got := DetectSDKVersion(t.TempDir()); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	home := t.TempDir()
	dir := filepath.Join(home, "cache", "sdk", "open-source")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Non-manifest files must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DetectSDKVersion(home); got != "" {
		t.Errorf("got %q from a directory with no manifests", got)
	}
}

// dpm may be absent or fail; detection must fall back rather than error.
func TestActiveDPMVersionWithoutDPM(t *testing.T) {
	if got := ActiveDPMVersion(t.TempDir()); got != "" {
		t.Errorf("got %q with no dpm binary, want empty", got)
	}
}

// The descriptor has to name the file Install actually wrote. Windows will not
// run an extensionless binary, so the two must agree about the .exe suffix or
// DPM looks for a file that is not there.
func TestComponentYAMLNamesTheInstalledBinary(t *testing.T) {
	descriptor := ComponentYAML()
	want := "path: bin/" + BinaryName()
	if !strings.Contains(descriptor, want) {
		t.Errorf("descriptor does not declare %q:\n%s", want, descriptor)
	}

	expected := ComponentName
	if runtime.GOOS == "windows" {
		expected += ".exe"
	}
	if got := BinaryName(); got != expected {
		t.Errorf("BinaryName() = %q, want %q on %s", got, expected, runtime.GOOS)
	}
}
