//go:build unix

package integration

import (
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
}

func TestRunRevokesRightsWhenInterrupted(t *testing.T) {
	for _, interrupt := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(interrupt.String(), func(t *testing.T) {
			first, second, _ := newAttachFakes(t)
			dir := t.TempDir()
			dar := filepath.Join(dir, "x.dar")
			os.WriteFile(dar, []byte("dar"), 0o600)
			suite := filepath.Join(dir, "suite")
			os.Mkdir(suite, 0o755)
			writeSuiteConfig(t, suite, forwardingLitConfig)
			ready := filepath.Join(dir, "lit-started")
			fakeLit := filepath.Join(dir, "lit")
			os.WriteFile(fakeLit, []byte("#!/bin/sh\ntouch \"$LIT_READY\"\nexec sleep 60\n"), 0o755)
			t.Setenv("LIT_READY", ready)
			tokenA, tokenB := writeToken(t, "ta"), writeToken(t, "tb")

			type outcome struct {
				code int
				err  error
			}
			finished := make(chan outcome, 1)
			go func() {
				code, err := Run(io.Discard, io.Discard, Options{
					TestDir: suite, Root: dir, DAR: dar, Lit: fakeLit, Parties: "Alice@1,Bob@2", CantonTimeout: 5,
					LedgerURLs: []string{first.server.URL, second.server.URL}, TokenFiles: []string{tokenA, tokenB},
				})
				finished <- outcome{code, err}
			}()

			waitForFile(t, ready)
			if err := syscall.Kill(os.Getpid(), interrupt); err != nil {
				t.Fatal(err)
			}

			select {
			case got := <-finished:
				if want := 128 + int(interrupt); got.code != want || got.err == nil {
					t.Errorf("code=%d err=%v, want %d and an interruption error", got.code, got.err, want)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("Run did not return after the signal")
			}
			first.mu.Lock()
			second.mu.Lock()
			defer first.mu.Unlock()
			defer second.mu.Unlock()
			if len(first.revoked) != 1 || len(second.revoked) != 1 {
				t.Errorf("rights not revoked after %s: %v / %v", interrupt, first.revoked, second.revoked)
			}
		})
	}
}

func TestRunRevokesEveryGrantWhenSignalledTwice(t *testing.T) {
	first, second, _ := newAttachFakes(t)
	first.revokeDelay, second.revokeDelay = 300*time.Millisecond, 300*time.Millisecond
	dir := t.TempDir()
	dar := filepath.Join(dir, "x.dar")
	os.WriteFile(dar, []byte("dar"), 0o600)
	suite := filepath.Join(dir, "suite")
	os.Mkdir(suite, 0o755)
	writeSuiteConfig(t, suite, forwardingLitConfig)
	ready := filepath.Join(dir, "lit-started")
	fakeLit := filepath.Join(dir, "lit")
	os.WriteFile(fakeLit, []byte("#!/bin/sh\ntouch \"$LIT_READY\"\nexec sleep 60\n"), 0o755)
	t.Setenv("LIT_READY", ready)
	tokenA, tokenB := writeToken(t, "ta"), writeToken(t, "tb")

	finished := make(chan int, 1)
	go func() {
		code, _ := Run(io.Discard, io.Discard, Options{
			TestDir: suite, Root: dir, DAR: dar, Lit: fakeLit, Parties: "Alice@1,Bob@2", CantonTimeout: 5,
			LedgerURLs: []string{first.server.URL, second.server.URL}, TokenFiles: []string{tokenA, tokenB},
		})
		finished <- code
	}()

	waitForFile(t, ready)
	syscall.Kill(os.Getpid(), syscall.SIGINT)
	time.Sleep(150 * time.Millisecond)
	syscall.Kill(os.Getpid(), syscall.SIGINT)
	syscall.Kill(os.Getpid(), syscall.SIGTERM)

	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after the signals")
	}
	first.mu.Lock()
	second.mu.Lock()
	defer first.mu.Unlock()
	defer second.mu.Unlock()
	if len(first.revoked) != 1 || len(second.revoked) != 1 {
		t.Errorf("rights not revoked after repeated signals: %v / %v", first.revoked, second.revoked)
	}
}

func TestRunStopsBootedCantonWhenInterrupted(t *testing.T) {
	dir := t.TempDir()
	dar := filepath.Join(dir, "x.dar")
	os.WriteFile(dar, []byte("dar"), 0o600)
	jar := filepath.Join(dir, "canton.jar")
	os.WriteFile(jar, []byte("jar"), 0o600)
	suite := filepath.Join(dir, "suite")
	os.Mkdir(suite, 0o755)
	writeSuiteConfig(t, suite, forwardingLitConfig)
	bin := filepath.Join(dir, "bin")
	os.Mkdir(bin, 0o755)
	pidFile := filepath.Join(dir, "java.pid")
	os.WriteFile(filepath.Join(bin, "java"), []byte("#!/bin/sh\necho $$ > \"$JAVA_PID\"\nexec sleep 60\n"), 0o755)
	t.Setenv("JAVA_PID", pidFile)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	finished := make(chan int, 1)
	go func() {
		code, _ := Run(io.Discard, io.Discard, Options{
			TestDir: suite, Root: dir, DAR: dar, CantonJar: jar, Parties: "Alice", CantonTimeout: 60,
		})
		finished <- code
	}()

	waitForFile(t, pidFile)
	time.Sleep(200 * time.Millisecond)
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	select {
	case code := <-finished:
		if code != 128+int(syscall.SIGTERM) {
			t.Errorf("code = %d, want %d", code, 128+int(syscall.SIGTERM))
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after the signal")
	}
	pidText, _ := os.ReadFile(pidFile)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidText)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Errorf("booted Canton process %d is still running", pid)
	}
}

func TestInterruptStopsTheWholeLitProcessGroup(t *testing.T) {
	first, second, _ := newAttachFakes(t)
	dir := t.TempDir()
	dar := filepath.Join(dir, "x.dar")
	os.WriteFile(dar, []byte("dar"), 0o600)
	suite := filepath.Join(dir, "suite")
	os.Mkdir(suite, 0o755)
	writeSuiteConfig(t, suite, forwardingLitConfig)
	ready := filepath.Join(dir, "lit-started")
	workerPid := filepath.Join(dir, "worker.pid")
	fakeLit := filepath.Join(dir, "lit")
	os.WriteFile(fakeLit, []byte("#!/bin/sh\n(trap '' TERM; exec sleep 60) &\necho $! > \"$WORKER_PID\"\ntouch \"$LIT_READY\"\nwait\n"), 0o755)
	t.Setenv("LIT_READY", ready)
	t.Setenv("WORKER_PID", workerPid)
	tokenA, tokenB := writeToken(t, "ta"), writeToken(t, "tb")

	finished := make(chan int, 1)
	go func() {
		code, _ := Run(io.Discard, io.Discard, Options{
			TestDir: suite, Root: dir, DAR: dar, Lit: fakeLit, Parties: "Alice@1,Bob@2", CantonTimeout: 5,
			LedgerURLs: []string{first.server.URL, second.server.URL}, TokenFiles: []string{tokenA, tokenB},
		})
		finished <- code
	}()

	waitForFile(t, ready)
	syscall.Kill(os.Getpid(), syscall.SIGTERM)
	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after the signal")
	}

	text, _ := os.ReadFile(workerPid)
	pid, err := strconv.Atoi(strings.TrimSpace(string(text)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("lit's worker process %d outlived the interrupted run", pid)
	}
}

func TestRunAsHelperProcess(t *testing.T) {
	if os.Getenv("DPM_TRACE_TEST_HELPER") != "1" {
		t.Skip("runs only as a subprocess of TestClosedStdoutAfterLitStartedStillRevokesGrants")
	}
	urls := strings.Split(os.Getenv("HELPER_URLS"), ",")
	tokens := strings.Split(os.Getenv("HELPER_TOKENS"), ",")
	code, _ := Run(os.Stdout, os.Stderr, Options{
		TestDir: os.Getenv("HELPER_SUITE"), Root: os.Getenv("HELPER_DIR"), DAR: os.Getenv("HELPER_DAR"), Lit: os.Getenv("HELPER_LIT"),
		Parties: "Alice@1,Bob@2", CantonTimeout: 5, LedgerURLs: urls, TokenFiles: tokens,
	})
	os.Exit(code)
}

func TestClosedStdoutAfterLitStartedStillRevokesGrants(t *testing.T) {
	first, second, _ := newAttachFakes(t)
	dir := t.TempDir()
	dar := filepath.Join(dir, "x.dar")
	os.WriteFile(dar, []byte("dar"), 0o600)
	suite := filepath.Join(dir, "suite")
	os.Mkdir(suite, 0o755)
	writeSuiteConfig(t, suite, forwardingLitConfig)
	ready, proceed := filepath.Join(dir, "lit-started"), filepath.Join(dir, "stdout-closed")
	fakeLit := filepath.Join(dir, "lit")
	script := "#!/bin/sh\ntouch \"$LIT_READY\"\nwhile [ ! -e \"$LIT_PROCEED\" ]; do sleep 0.05; done\ntrap '' PIPE\necho output || exit 7\nexit 0\n"
	os.WriteFile(fakeLit, []byte(script), 0o755)

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(os.Args[0], "-test.run=TestRunAsHelperProcess")
	helper.Env = append(os.Environ(), "DPM_TRACE_TEST_HELPER=1", "LIT_READY="+ready, "LIT_PROCEED="+proceed,
		"HELPER_URLS="+first.server.URL+","+second.server.URL,
		"HELPER_TOKENS="+writeToken(t, "ta")+","+writeToken(t, "tb"),
		"HELPER_SUITE="+suite, "HELPER_DIR="+dir, "HELPER_DAR="+dar, "HELPER_LIT="+fakeLit)
	helper.Stdout = writer
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	waitForFile(t, ready)
	if first.activeRights() == 0 || second.activeRights() == 0 {
		t.Fatalf("grants were not in place when lit started: %d / %d", first.activeRights(), second.activeRights())
	}
	reader.Close()
	os.WriteFile(proceed, nil, 0o600)

	finished := make(chan error, 1)
	go func() { finished <- helper.Wait() }()
	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		helper.Process.Kill()
		t.Fatal("the helper did not exit after its stdout closed")
	}
	if first.activeRights() != 0 || second.activeRights() != 0 {
		t.Errorf("rights left behind after a closed stdout: %d / %d", first.activeRights(), second.activeRights())
	}
}

type pipeWriter struct {
	failAfter string
	failed    bool
}

func (w *pipeWriter) Write(p []byte) (int, error) {
	if w.failed || strings.Contains(string(p), w.failAfter) {
		w.failed = true
		return 0, syscall.EPIPE
	}
	return len(p), nil
}

func TestSigpipeDuringUploadDoesNotInterruptTheRun(t *testing.T) {
	fake := newFakeParticipant(t)
	fake.uploadGate = make(chan struct{})
	fake.uploadArrived = make(chan struct{}, 1)
	dir := t.TempDir()
	dar := filepath.Join(dir, "x.dar")
	os.WriteFile(dar, []byte("dar"), 0o600)
	suite := filepath.Join(dir, "suite")
	os.Mkdir(suite, 0o755)
	writeSuiteConfig(t, suite, forwardingLitConfig)
	fakeLit := filepath.Join(dir, "lit")
	os.WriteFile(fakeLit, []byte("#!/bin/sh\n"), 0o755)
	type outcome struct {
		code int
		err  error
	}
	registered := make(chan chan<- os.Signal, 1)
	registerSignals = func(c chan<- os.Signal, signals ...os.Signal) {
		registered <- c
		signal.Notify(c, signals...)
	}
	defer func() { registerSignals = signal.Notify }()
	finished := make(chan outcome, 1)
	go func() {
		code, err := Run(io.Discard, io.Discard, Options{
			TestDir: suite, Root: dir, DAR: dar, Lit: fakeLit, Parties: "Alice", CantonTimeout: 5,
			LedgerURLs: []string{fake.server.URL},
		})
		finished <- outcome{code, err}
	}()

	select {
	case <-fake.uploadArrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the upload never started")
	}
	(<-registered) <- syscall.SIGPIPE
	time.Sleep(300 * time.Millisecond)
	close(fake.uploadGate)

	select {
	case got := <-finished:
		if got.code != 0 || got.err != nil {
			t.Errorf("code=%d err=%v, want a bare SIGPIPE ignored", got.code, got.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestBrokenOutputInterruptsAndRevokesGrants(t *testing.T) {
	first, second, _ := newAttachFakes(t)
	dir := t.TempDir()
	dar := filepath.Join(dir, "x.dar")
	os.WriteFile(dar, []byte("dar"), 0o600)
	suite := filepath.Join(dir, "suite")
	os.Mkdir(suite, 0o755)
	writeSuiteConfig(t, suite, forwardingLitConfig)
	fakeLit := filepath.Join(dir, "lit")
	os.WriteFile(fakeLit, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755)

	code, err := Run(&pipeWriter{failAfter: "running integration suite"}, io.Discard, Options{
		TestDir: suite, Root: dir, DAR: dar, Lit: fakeLit, Parties: "Alice@1,Bob@2", CantonTimeout: 5,
		LedgerURLs: []string{first.server.URL, second.server.URL}, TokenFiles: []string{writeToken(t, "ta"), writeToken(t, "tb")},
	})

	if code != 128+int(syscall.SIGPIPE) || err == nil {
		t.Errorf("code=%d err=%v, want %d: the runner's own output broke", code, err, 128+int(syscall.SIGPIPE))
	}
	if first.activeRights() != 0 || second.activeRights() != 0 {
		t.Errorf("rights left behind: %d / %d", first.activeRights(), second.activeRights())
	}
}

func TestSignalDuringTeardownStillReturnsTheSignalCode(t *testing.T) {
	first, second, _ := newAttachFakes(t)
	first.revokeDelay = 400 * time.Millisecond
	first.revokeArrived = make(chan struct{}, 1)
	_ = second
	dir := t.TempDir()
	dar := filepath.Join(dir, "x.dar")
	os.WriteFile(dar, []byte("dar"), 0o600)
	suite := filepath.Join(dir, "suite")
	os.Mkdir(suite, 0o755)
	writeSuiteConfig(t, suite, forwardingLitConfig)
	fakeLit := filepath.Join(dir, "lit")
	os.WriteFile(fakeLit, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	finished := make(chan int, 1)
	go func() {
		code, _ := Run(io.Discard, io.Discard, Options{
			TestDir: suite, Root: dir, DAR: dar, Lit: fakeLit, Parties: "Alice@1,Bob@2", CantonTimeout: 5,
			LedgerURLs: []string{first.server.URL, second.server.URL}, TokenFiles: []string{writeToken(t, "ta"), writeToken(t, "tb")},
		})
		finished <- code
	}()

	select {
	case <-first.revokeArrived:
	case <-time.After(10 * time.Second):
		t.Fatal("teardown never started")
	}
	syscall.Kill(os.Getpid(), syscall.SIGTERM)

	select {
	case code := <-finished:
		if code != 128+int(syscall.SIGTERM) {
			t.Errorf("code = %d, want %d", code, 128+int(syscall.SIGTERM))
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestNormalLitExitLeavesItsBackgroundJobsAlone(t *testing.T) {
	fake := newFakeParticipant(t)
	dir := t.TempDir()
	dar := filepath.Join(dir, "x.dar")
	os.WriteFile(dar, []byte("dar"), 0o600)
	suite := filepath.Join(dir, "suite")
	os.Mkdir(suite, 0o755)
	writeSuiteConfig(t, suite, forwardingLitConfig)
	workerPid := filepath.Join(dir, "worker.pid")
	fakeLit := filepath.Join(dir, "lit")
	os.WriteFile(fakeLit, []byte("#!/bin/sh\nsleep 30 >/dev/null 2>&1 &\necho $! > \"$WORKER_PID\"\nexit 0\n"), 0o755)
	t.Setenv("WORKER_PID", workerPid)

	code, err := Run(io.Discard, io.Discard, Options{
		TestDir: suite, Root: dir, DAR: dar, Lit: fakeLit, Parties: "Alice", CantonTimeout: 5,
		LedgerURLs: []string{fake.server.URL},
	})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}

	text, _ := os.ReadFile(workerPid)
	pid, err := strconv.Atoi(strings.TrimSpace(string(text)))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	time.Sleep(200 * time.Millisecond)
	if syscall.Kill(pid, 0) != nil {
		t.Errorf("background job %d was killed after a normal lit exit", pid)
	}
}
