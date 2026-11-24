// Package migrations embeds the source migration manifest for readiness checks.
package migrations

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed *.up.sql
var sources embed.FS

// ReadSource returns the immutable SQL compiled into this migrator.
func ReadSource(name string) ([]byte, error) { return sources.ReadFile(name) }

// VersionsThrough supports explicitly reserved numeric gaps without accepting
// missing applied migrations or requiring unrelated testing-only migrations.
func VersionsThrough(expected string) ([]string, error) {
	entries, err := sources.ReadDir(".")
	if err != nil {
		return nil, err
	}
	versions := []string{}
	found := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".up.sql") || len(name) < 4 {
			continue
		}
		version := name[:4]
		if version == expected {
			found = true
		}
		if version <= expected {
			versions = append(versions, version)
		}
	}
	if !found {
		return nil, fmt.Errorf("unknown schema version %s", expected)
	}
	sort.Strings(versions)
	return versions, nil
}
