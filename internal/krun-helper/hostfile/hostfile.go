package hostfile

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ftechmax/krun/internal/contracts"
)

const (
	krunHostsBlockStart = "##### KRUN #####"
	krunHostsBlockEnd   = "##### END KRUN #####"
)

var hostsFilePathResolver = defaultHostsFilePath

// Write replaces krun's block. The privileged IPC handler validates entries.
func Write(entries []contracts.HostsEntry) error {
	path, err := hostsFilePathResolver()
	if err != nil {
		return err
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read hosts file: %w", err)
	}

	newline := detectNewline(string(content))
	withoutBlock := stripKrunBlock(string(content))

	if len(entries) == 0 {
		if withoutBlock == string(content) {
			return nil
		}
		return writeHosts(path, withoutBlock)
	}

	block := buildKrunBlock(entries, newline)
	base := strings.TrimRight(withoutBlock, "\r\n")
	result := block
	if strings.TrimSpace(base) != "" {
		result = base + newline + block
	}

	fmt.Printf("Updating hosts file at %s with entries:\n", path)
	for _, entry := range entries {
		fmt.Printf("  %s\t%s\n", entry.IP, entry.Hostname)
	}

	return writeHosts(path, result)
}

func defaultHostsFilePath() (string, error) {
	if runtime.GOOS == "windows" {
		root := strings.TrimSpace(os.Getenv("SystemRoot"))
		if root == "" {
			return "", fmt.Errorf("SystemRoot environment variable is not set")
		}
		return filepath.Join(root, "System32", "drivers", "etc", "hosts"), nil
	}
	return "/etc/hosts", nil
}

func writeHosts(path string, content string) error {
	perm := os.FileMode(0o644)
	info, err := os.Stat(path)
	if err == nil {
		perm = info.Mode()
	}

	dir := filepath.Dir(path)
	tempFile, err := os.CreateTemp(dir, ".krun-hosts-*")
	if err != nil {
		return fmt.Errorf("failed to create temp hosts file: %w", err)
	}
	defer tempFile.Close()
	tempName := tempFile.Name()
	defer os.Remove(tempName)

	if _, err := tempFile.WriteString(content); err != nil {
		return fmt.Errorf("failed to write temp hosts file: %w", err)
	}
	if err := tempFile.Chmod(perm); err != nil {
		return fmt.Errorf("failed to set temp hosts file permissions: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp hosts file: %w", err)
	}

	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace hosts file: %w", err)
	}

	return nil
}

func stripKrunBlock(content string) string {
	start := strings.Index(content, krunHostsBlockStart)
	if start == -1 {
		return content
	}

	afterStart := content[start+len(krunHostsBlockStart):]
	relativeEnd := strings.Index(afterStart, krunHostsBlockEnd)
	if relativeEnd == -1 {
		return strings.TrimRight(content[:start], "\r\n")
	}

	end := start + len(krunHostsBlockStart) + relativeEnd + len(krunHostsBlockEnd)
	for end < len(content) && (content[end] == '\n' || content[end] == '\r') {
		end++
	}

	prefix := strings.TrimRight(content[:start], "\r\n")
	suffix := strings.TrimLeft(content[end:], "\r\n")
	if prefix == "" {
		return suffix
	}
	if suffix == "" {
		return prefix
	}

	newline := detectNewline(content)
	return prefix + newline + suffix
}

func buildKrunBlock(entries []contracts.HostsEntry, newline string) string {
	var builder strings.Builder
	builder.WriteString(krunHostsBlockStart)
	builder.WriteString(newline)
	for _, entry := range entries {
		hostname := strings.TrimSpace(entry.Hostname)
		ip := strings.TrimSpace(entry.IP)
		builder.WriteString(ip)
		builder.WriteString("\t")
		builder.WriteString(hostname)
		builder.WriteString(newline)
	}
	builder.WriteString(krunHostsBlockEnd)
	builder.WriteString(newline)
	return builder.String()
}

func detectNewline(content string) string {
	if strings.Contains(content, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

// HasBlock reports whether the machine's hosts file still carries krun's
// block. Reading needs no privilege, so the CLI can use it to spot a block
// left behind by a helper that died before its session was disabled.
func HasBlock() (bool, error) {
	path, err := hostsFilePathResolver()
	if err != nil {
		return false, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("failed to read hosts file: %w", err)
	}
	return strings.Contains(string(content), krunHostsBlockStart), nil
}
