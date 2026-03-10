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

func Update(entries []contracts.HostsEntry) error {
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

func Remove() error {
	path, err := hostsFilePathResolver()
	if err != nil {
		return err
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read hosts file: %w", err)
	}

	withoutBlock := stripKrunBlock(string(content))
	if withoutBlock == string(content) {
		return nil
	}

	return writeHosts(path, withoutBlock)
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
	tempName := tempFile.Name()
	defer os.Remove(tempName)

	if _, err := tempFile.WriteString(content); err != nil {
		tempFile.Close()
		return fmt.Errorf("failed to write temp hosts file: %w", err)
	}
	if err := tempFile.Chmod(perm); err != nil {
		tempFile.Close()
		return fmt.Errorf("failed to set temp hosts file permissions: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp hosts file: %w", err)
	}

	if err := os.Rename(tempName, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("failed to replace hosts file: %w", err)
		}
		if retryErr := os.Rename(tempName, path); retryErr != nil {
			return fmt.Errorf("failed to replace hosts file: %w", retryErr)
		}
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
		if hostname == "" || ip == "" {
			continue
		}
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
