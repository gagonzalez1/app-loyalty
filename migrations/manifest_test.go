package migrations

import (
	"slices"
	"sort"
	"testing"
)

func TestManifestSupportsReservedVersions(t *testing.T) {
	got, err := VersionsThrough("0052")
	if err != nil || len(got) != 52 || got[len(got)-1] != "0052" || !slices.Contains(got, "0035") || !sort.StringsAreSorted(got) {
		t.Fatalf("manifest=%v err=%v", got, err)
	}
	legacy, err := VersionsThrough("0022")
	if err != nil || len(legacy) != 22 {
		t.Fatalf("historical manifest err=%v", err)
	}
	if _, err = VersionsThrough("9999"); err == nil {
		t.Fatal("accepted unknown schema version")
	}
}
