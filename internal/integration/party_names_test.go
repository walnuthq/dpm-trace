package integration

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectReservedPartyNamesTable(t *testing.T) {
	cases := []struct {
		name    string
		parties string
		clash   string
	}{
		{"token file", "Alice,Token_File", "DPM_TRACE_IT_TOKEN_FILE"},
		{"token file of the second participant", "Alice,token_file2@2", "DPM_TRACE_IT_TOKEN_FILE2"},
		{"ledger", "ledger", "DPM_TRACE_IT_LEDGER"},
		{"ledger of the third participant", "Ledger3", "DPM_TRACE_IT_LEDGER3"},
		{"dar", "Dar", "DPM_TRACE_IT_DAR"},
		{"daml", "Daml", "DPM_TRACE_IT_DAML"},
		{"daml yaml", "daml_yaml", "DPM_TRACE_IT_DAML_YAML"},
		{"dpm", "DPM", "DPM_TRACE_IT_DPM"},
		{"python", "Python", "DPM_TRACE_IT_PYTHON"},
		{"src", "src", "DPM_TRACE_IT_SRC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := RejectReservedPartyNames(ParsePlacements(tc.parties))

			if err == nil || !strings.Contains(err.Error(), tc.clash) {
				t.Errorf("err = %v, want a rejection naming %s", err, tc.clash)
			}
		})
	}
}

func TestRejectReservedPartyNamesAcceptsOrdinaryNames(t *testing.T) {
	for _, parties := range []string{"Alice,Bob", "Alice@2,Bob@1", "Token,Ledgers,Tokenfile_x,Dar2"} {
		if err := RejectReservedPartyNames(ParsePlacements(parties)); err != nil {
			t.Errorf("%q: %v", parties, err)
		}
	}
}

func TestRunRejectsAPartyWhoseExportNameClashesBeforeAnyProvisioning(t *testing.T) {
	dir := t.TempDir()
	suite := filepath.Join(dir, "suite")
	if err := os.Mkdir(suite, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSuiteConfig(t, suite, forwardingLitConfig)

	t.Run("attach", func(t *testing.T) {
		fake := newFakeParticipant(t)

		code, err := Run(io.Discard, io.Discard, Options{
			TestDir: suite, Root: dir, DAR: filepath.Join(dir, "x.dar"), Parties: "Alice,Token_File",
			LedgerURLs: []string{fake.server.URL},
		})

		assertPartyClash(t, code, err)
		if fake.uploads != 0 {
			t.Errorf("the DAR was uploaded %d time(s) before the party was rejected", fake.uploads)
		}
	})
	t.Run("boot", func(t *testing.T) {
		code, err := Run(io.Discard, io.Discard, Options{
			TestDir: suite, Root: dir, DAR: filepath.Join(dir, "x.dar"), Parties: "Alice,Token_File",
			CantonJar: filepath.Join(dir, "missing.jar"),
		})

		assertPartyClash(t, code, err)
	})
}

func assertPartyClash(t *testing.T, code int, err error) {
	t.Helper()
	if code != 2 || err == nil || !strings.Contains(err.Error(), "Token_File") || !strings.Contains(err.Error(), "DPM_TRACE_IT_TOKEN_FILE") {
		t.Errorf("code=%d err=%v, want exit 2 naming the party and the clash", code, err)
	}
}
