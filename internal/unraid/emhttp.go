package unraid

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// emhttpDir is Unraid's own fixed OS path for its live-state INI files — not
// operator configuration (CONVENTIONS: no config knob for a value that never
// changes on the platform this binary targets). A package var, not a const,
// solely so tests can point it at a temp fixture directory.
var emhttpDir = "/var/local/emhttp"

// userSharesRoot is Unraid's fixed mount root for user shares. Same
// var-not-const reasoning as emhttpDir.
var userSharesRoot = "/mnt/user"

// parseINI parses Unraid's `["section"]` / `key="value"` emhttp format.
// bufio + strings only (no ini library — CONVENTIONS rule 3): the format has
// no nesting, no comments, and no line continuation in the files this source
// reads, confirmed by direct inspection of the real files
// (docs/research/unraid-state-sources.md, "Gaps and their direct sources").
func parseINI(r io.Reader) (map[string]map[string]string, error) {
	sections := map[string]map[string]string{}
	var current string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = strings.Trim(line, `[]"`)
			sections[current] = map[string]string{}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || current == "" {
			continue // stray line outside any section: ignore rather than guess
		}
		sections[current][strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(val), `"`)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return sections, nil
}

// readINI opens and parses one emhttp INI file. ctx is checked up front
// (rule 4): a canceled/expired context short-circuits before touching disk,
// even though a local file read on the host's own tmpfs has no useful way to
// be interrupted mid-read.
func readINI(ctx context.Context, name string) (map[string]map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Join(emhttpDir, name)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("unraid emhttp: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	sections, err := parseINI(f)
	if err != nil {
		return nil, fmt.Errorf("unraid emhttp: parse %s: %w", path, err)
	}
	return sections, nil
}

// shareGaps is the two fields GraphQL's Share type never exposes (confirmed
// via live introspection: Share has no pool-name or export-visibility
// field), keyed by share name.
type shareGaps struct {
	cachePool string
	exported  bool
}

// readShareGaps fills the CachePool/Exported gap in core.Share from
// shares.ini (cachePool field, confirmed present in a live capture) and
// sec.ini (export field: "-" means not exported, any other value — "e" was
// the only other value observed live — means exported).
func readShareGaps(ctx context.Context) (map[string]shareGaps, error) {
	shares, err := readINI(ctx, "shares.ini")
	if err != nil {
		return nil, err
	}
	sec, err := readINI(ctx, "sec.ini")
	if err != nil {
		return nil, err
	}
	gaps := make(map[string]shareGaps, len(shares))
	for name, fields := range shares {
		g := shareGaps{cachePool: fields["cachePool"]}
		if secFields, ok := sec[name]; ok {
			g.exported = secFields["export"] != "-" && secFields["export"] != ""
		}
		gaps[name] = g
	}
	return gaps, nil
}

// shareAccessible reports whether a share's mount directory currently stats
// successfully — a directly-observed signal (not an ini field, not a
// fabricated default) for whether the share is actually reachable right now,
// as opposed to merely configured.
func shareAccessible(name string) bool {
	_, err := os.Stat(filepath.Join(userSharesRoot, name))
	return err == nil
}
