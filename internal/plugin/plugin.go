// Package plugin registers the binary as a DPM component so it runs as
// `dpm trace`.
//
// Ports run_install_plugin. The Go port simplifies one thing: the manifest
// points at the binary directly, where the Python implementation had to write a
// bash wrapper that re-entered the installed package with the interpreter that
// ran install-plugin. Removing that runtime dependency is the point of #7.
package plugin

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// ComponentName is the DPM component this binary registers as.
const ComponentName = "dpm-trace"

// BinaryName is the file name the component's executable is installed under.
// Windows will not run an extensionless file, so the suffix is not cosmetic.
func BinaryName() string {
	if runtime.GOOS == "windows" {
		return ComponentName + ".exe"
	}
	return ComponentName
}

// ComponentYAML is the component descriptor DPM reads to find the command. The
// path has to match what Install actually wrote, including the .exe suffix, or
// DPM looks for a file that is not there.
func ComponentYAML() string {
	return `apiVersion: digitalasset.com/v1
kind: Component
spec:
  commands:
    - name: trace
      path: bin/` + BinaryName() + `
      desc: Visualize and compare Canton transactions
`
}

// Options configure installation.
type Options struct {
	DPMHome          string
	SDKVersion       string
	ComponentVersion string
	// BinaryPath is the executable to install. Defaults to the running binary.
	BinaryPath string
}

// Install writes the component into the DPM home and registers it in the SDK
// manifest. Ports run_install_plugin.
func Install(w io.Writer, opts Options) error {
	home, err := resolveHome(opts.DPMHome)
	if err != nil {
		return err
	}

	version := opts.ComponentVersion
	if version == "" {
		version = "0.1.0"
	}

	sdkVersion := opts.SDKVersion
	if sdkVersion == "" {
		sdkVersion = DetectSDKVersion(home)
	}
	if sdkVersion == "" {
		return fmt.Errorf(
			"could not find an installed SDK manifest under %s; pass --sdk-version (e.g. 3.4.11)",
			filepath.Join(home, "cache", "sdk", "open-source"))
	}

	manifest := filepath.Join(home, "cache", "sdk", "open-source", sdkVersion+".yaml")
	if _, err := os.Stat(manifest); err != nil {
		return fmt.Errorf("SDK manifest not found: %s\nInstall the SDK first (e.g. `dpm install %s`)",
			manifest, sdkVersion)
	}

	source := opts.BinaryPath
	if source == "" {
		if source, err = os.Executable(); err != nil {
			return err
		}
	}

	componentDir := filepath.Join(home, "cache", "components", ComponentName, version)
	binDir := filepath.Join(componentDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(componentDir, "component.yaml"), []byte(ComponentYAML()), 0o644); err != nil {
		return err
	}
	if err := copyExecutable(source, filepath.Join(binDir, BinaryName())); err != nil {
		return err
	}
	if err := RegisterInManifest(manifest, ComponentName, version); err != nil {
		return err
	}

	fmt.Fprintf(w, "Registered %s %s as a DPM plugin (SDK %s) in %s.\n", ComponentName, version, sdkVersion, home)
	fmt.Fprintln(w, "Run with:  dpm trace --help")
	return nil
}

func resolveHome(explicit string) (string, error) {
	if explicit != "" {
		return expandUser(explicit)
	}
	if env := os.Getenv("DPM_HOME"); env != "" {
		return expandUser(env)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".dpm"), nil
}

func expandUser(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
}

// copyExecutable installs the binary, writing to a temporary file first so a
// failure cannot leave a half-written executable in place.
func copyExecutable(source, dest string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	temp := dest + ".tmp"
	out, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(temp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(temp)
		return err
	}
	return os.Rename(temp, dest)
}

var (
	assistantLine  = regexp.MustCompile(`^  assistant:\s*$`)
	componentsLine = regexp.MustCompile(`^  components:\s*$`)
)

// RegisterInManifest adds the component under spec.components, leaving an
// existing entry alone. Ports register_component_in_manifest, including where
// the entry goes: last component, immediately before `assistant:`, matching the
// install script's awk.
func RegisterInManifest(manifest, name, version string) error {
	data, err := os.ReadFile(manifest)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")

	existing := regexp.MustCompile(`^    ` + regexp.QuoteMeta(name) + `:`)
	for _, line := range lines {
		if existing.MatchString(line) {
			return nil // already registered
		}
	}

	entry := []string{"    " + name + ":", "      version: " + version}
	var out []string
	added := false
	for _, line := range lines {
		if !added && assistantLine.MatchString(line) {
			out = append(out, entry...)
			added = true
		}
		out = append(out, line)
	}
	if !added {
		for i, line := range lines {
			if componentsLine.MatchString(line) {
				out = append(append(append([]string{}, lines[:i+1]...), entry...), lines[i+1:]...)
				added = true
				break
			}
		}
	}
	if !added {
		out = append(lines, entry...)
	}
	return os.WriteFile(manifest, []byte(strings.Join(out, "\n")+"\n"), 0o644)
}

// DetectSDKVersion picks the SDK manifest to register into: the active one when
// `dpm version` reports it, otherwise the highest installed.
// Ports detect_sdk_version.
func DetectSDKVersion(home string) string {
	dir := filepath.Join(home, "cache", "sdk", "open-source")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var versions []string
	for _, entry := range entries {
		if name := entry.Name(); strings.HasSuffix(name, ".yaml") {
			versions = append(versions, strings.TrimSuffix(name, ".yaml"))
		}
	}
	if len(versions) == 0 {
		return ""
	}
	sort.Strings(versions)

	if active := ActiveDPMVersion(home); active != "" {
		for _, version := range versions {
			if version == active {
				return active
			}
		}
	}
	return versions[len(versions)-1]
}

// ActiveDPMVersion asks the installed dpm which SDK is active. A missing or
// failing dpm is not an error: detection falls back to the highest installed.
// Ports active_dpm_version.
func ActiveDPMVersion(home string) string {
	binary := filepath.Join(home, "bin", "dpm")
	if _, err := os.Stat(binary); err != nil {
		return ""
	}
	cmd := exec.Command(binary, "version")
	done := make(chan struct{})
	var output []byte
	var runErr error
	go func() {
		output, runErr = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		return ""
	}
	if runErr != nil {
		return ""
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, "*") {
			fields := strings.Fields(strings.ReplaceAll(line, "*", " "))
			if len(fields) > 0 {
				return fields[0]
			}
		}
	}
	return ""
}
