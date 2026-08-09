package scripts

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// mutatingBinaries and safeReadonlyBinaries are copied verbatim from
// prototypes/dry-run/mutating.list and safe-readonly.list (FINDINGS.md
// Question 2), with one addition: "mkdir" is added to mutatingBinaries
// because FINDINGS.md's Question 2 branch (b) demonstration found it is a
// genuine unshimmed mutation-risk binary a real script called
// (tdarr-plex-gate's `mkdir -p "$STATE_DIR"`) — this closes that exact,
// concretely-found gap. Both lists are living documents that need curation
// as new real scripts are tried (FINDINGS.md "honest ceiling"), not a
// one-time enumeration; that is why they are declared here, not derived.
var mutatingBinaries = []string{
	"docker", "rm", "mv", "cp", "chmod", "chown", "mkfs", "dd", "rsync", "ln", "tee", "truncate",
	"mkdir",
}

var safeReadonlyBinaries = []string{
	"bash", "sh", "cat", "ls", "grep", "egrep", "fgrep", "find", "echo", "printf",
	"date", "sleep", "test", "[", "awk", "sed", "curl", "head", "tail", "wc", "sort",
	"uniq", "basename", "dirname", "hostname", "id", "whoami", "true", "false",
	"stat", "du", "df", "ps", "pgrep", "jq", "mountpoint", "cut", "xargs", "tr",
	"flock", "logger", "timeout", "realpath",
}

func containsBase(list []string, base string) bool {
	for _, s := range list {
		if s == base {
			return true
		}
	}
	return false
}

// violationLineRE matches the shim's `violation()` output format exactly:
//
//	[2026-08-09T17:20:55.108594383+0800] 52857 VIOLATION cmd=rm target=/boot/x reason=boot-write-forbidden
var violationLineRE = regexp.MustCompile(`^\[([^]]+)]\s+(\d+)\s+VIOLATION\s+cmd=(\S+)\s+target=(\S+)\s+reason=(\S+)$`)

// cmdLineRE matches the shim's `log_line()` CMD format:
//
//	[2026-08-09T17:20:55.108594383+0800] 52857 CMD rm -rf /tmp/x
var cmdLineRE = regexp.MustCompile(`^\[([^]]+)]\s+(\d+)\s+CMD\s+(.*)$`)

const shimTimeLayout = "2006-01-02T15:04:05.999999999-0700"

// parseTranscript reads a shim transcript file into TranscriptEntry rows and
// the raw list of predicate-branch-(c) violation reasons.
func parseTranscript(path string) (entries []TranscriptEntry, violationReasons []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if m := violationLineRE.FindStringSubmatch(line); m != nil {
			ts, _ := time.Parse(shimTimeLayout, m[1])
			pid, _ := strconv.Atoi(m[2])
			entries = append(entries, TranscriptEntry{Time: ts, PID: pid, Kind: "VIOLATION", Raw: line})
			violationReasons = append(violationReasons, fmt.Sprintf(
				"(c) cmd=%s target=%s reason=%s", m[3], m[4], m[5]))
			continue
		}
		if m := cmdLineRE.FindStringSubmatch(line); m != nil {
			ts, _ := time.Parse(shimTimeLayout, m[1])
			pid, _ := strconv.Atoi(m[2])
			entries = append(entries, TranscriptEntry{Time: ts, PID: pid, Kind: "CMD", Raw: m[3]})
		}
	}
	if err := sc.Err(); err != nil {
		return entries, violationReasons, fmt.Errorf("scan transcript: %w", err)
	}
	return entries, violationReasons, nil
}

// execveRE matches strace -f -qq -e trace=execve output:
//
//	1234 execve("/usr/bin/mkdir", ["mkdir", "-p", "/x"], 0x... /* n vars */) = 0
var execveRE = regexp.MustCompile(`execve\("([^"]+)"`)

// unshimmedMutationReasons implements rejection predicate branch (b): an
// execve() against a binary that resolves outside the shim directory AND is
// not on the fixed safeReadonlyBinaries allowlist. This is deliberately an
// allowlist of known-safe, not a blocklist of known-unsafe (FINDINGS.md
// "honest ceiling" — an unenumerated binary is guilty until curated in).
func unshimmedMutationReasons(stracePath, shimDir string) (reasons []string, sawTrace bool) {
	f, err := os.Open(stracePath)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	seen := map[string]struct{}{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		sawTrace = true
		m := execveRE.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		p := m[1]
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}

		base := filepath.Base(p)
		isShim := strings.HasPrefix(p, shimDir+string(filepath.Separator))
		isSafe := containsBase(safeReadonlyBinaries, base)
		if isShim || isSafe {
			continue
		}
		if containsBase(mutatingBinaries, base) {
			reasons = append(reasons, fmt.Sprintf(
				"(b) shimmed command %q was invoked via a path outside the shim dir (%s) — PATH-shim bypass", base, p))
		} else {
			reasons = append(reasons, fmt.Sprintf(
				"(b) unshimmed binary invoked: %s (not on the read-only-safe allowlist, not a known-mutating shim)", p))
		}
	}
	return reasons, sawTrace
}

// verdict is the mechanical "cannot complete a dry run" predicate from
// FINDINGS.md Question 2, ported branch-for-branch from predicate.sh:
//
//	REJECTED iff any of:
//	  (a) the script's own exit code is nonzero, OR it never terminated;
//	  (b) an execve() happened against a binary outside the shim dir and
//	      not on the read-only-safe allowlist;
//	  (c) a shimmed mutating call's target path resolved outside the
//	      sandbox scratch scope, or under /boot.
//
// Any other outcome is APPROVED-FOR-REVIEW — evidence, never proof.
func verdict(transcriptPath, stracePath, shimDir string, exitCode int, timedOut bool) (approved bool, reasons, warnings []string, entries []TranscriptEntry) {
	if timedOut {
		reasons = append(reasons, "(a) did not terminate within the timeout (script was killed) — a dry run that never finishes proves nothing")
	} else if exitCode != 0 {
		reasons = append(reasons, fmt.Sprintf("(a) script exited nonzero: %d", exitCode))
	}

	bReasons, sawTrace := unshimmedMutationReasons(stracePath, shimDir)
	reasons = append(reasons, bReasons...)
	if !sawTrace {
		warnings = append(warnings, "ground-truth exec trace unavailable (strace missing or produced no output): "+
			"absolute-path bypass of the PATH shims cannot be detected for this run")
	}

	var cReasons []string
	entries, cReasons, _ = parseTranscript(transcriptPath)
	reasons = append(reasons, cReasons...)

	return len(reasons) == 0, reasons, warnings, entries
}
