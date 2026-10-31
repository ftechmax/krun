package build

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ftechmax/krun/internal/kube"
	"github.com/ftechmax/krun/internal/utils"
)

type remoteEntry struct {
	mtimeNs int64
	size    int64
}

type localFile struct {
	relPath   string
	localPath string
	mtimeNs   int64
	size      int64
}

type syncPlan struct {
	toUpload []localFile
	toDelete []string
}

var excludedDirectories = map[string]struct{}{
	".github":      {},
	".vs":          {},
	".git":         {},
	".angular":     {},
	"bin":          {},
	"obj":          {},
	"node_modules": {},
	"k8s":          {},
	"docs":         {},
}

// requiredFiles are project-relative paths (such as Dockerfiles) synced even when excluded.
func copySource(client *kube.Client, projectName string, localRoot string, skipWeb bool, requiredFiles []string) (bool, error) {
	startTime := time.Now()
	plusSym := utils.Colorize("+", utils.Green)
	minusSym := utils.Colorize("-", utils.Red)
	squigglySym := utils.Colorize("~", utils.Yellow)

	fmt.Printf("Copying project %s to remote server\n", projectName)

	remoteRoot := path.Join(workspacePath, projectName)

	ctx := context.Background()
	remoteFiles, err := listRemoteFiles(ctx, client, remoteRoot)
	if err != nil {
		return false, err
	}

	localFiles, err := collectLocalFiles(localRoot, skipWeb)
	if err != nil {
		return false, fmt.Errorf("walk local source tree: %w", err)
	}
	if err := addRequiredFiles(localFiles, localRoot, requiredFiles); err != nil {
		return false, err
	}

	plan := buildSyncPlan(localFiles, remoteFiles)

	filesAdded := 0
	filesUpdated := 0
	for _, f := range plan.toUpload {
		if _, existed := remoteFiles[f.relPath]; existed {
			filesUpdated++
			fmt.Printf("%s %s\n", squigglySym, f.relPath)
		} else {
			filesAdded++
			fmt.Printf("%s %s\n", plusSym, f.relPath)
		}
	}

	filesDeleted := len(plan.toDelete)
	for _, rel := range plan.toDelete {
		fmt.Printf("%s %s\n", minusSym, rel)
	}

	if len(plan.toUpload) > 0 {
		if err := streamTarUpload(ctx, client, projectName, plan.toUpload); err != nil {
			return false, err
		}
	}
	if len(plan.toDelete) > 0 {
		if err := deleteRemoteFiles(ctx, client, remoteRoot, plan.toDelete); err != nil {
			return false, err
		}
	}

	elapsed := time.Since(startTime)
	shortDur := elapsed.Truncate(10 * time.Millisecond)
	if filesAdded == 0 && filesUpdated == 0 && filesDeleted == 0 {
		fmt.Printf("%s | %s %s\n",
			utils.Colorize("No changes - project up to date", utils.Gray),
			utils.Colorize("Completed sync in", utils.Gray),
			utils.Colorize(shortDur.String(), utils.Gray),
		)
		return false, nil
	}

	addedStr := utils.Colorize(fmt.Sprintf("Added: %d", filesAdded), utils.Green)
	updatedStr := utils.Colorize(fmt.Sprintf("Updated: %d", filesUpdated), utils.Yellow)
	deletedStr := utils.Colorize(fmt.Sprintf("Deleted: %d", filesDeleted), utils.Red)
	timeLabel := utils.Colorize("Completed sync in", utils.Gray)
	timeVal := utils.Colorize(shortDur.String(), utils.Gray)

	fmt.Printf("Sync summary: %s  %s  %s  | %s %s\n", addedStr, updatedStr, deletedStr, timeLabel, timeVal)

	return true, nil
}

func listRemoteFiles(ctx context.Context, client *kube.Client, remoteRoot string) (map[string]remoteEntry, error) {
	stdout, stderr, err := execInBuildPod(ctx, client, []string{
		"find", remoteRoot, "-type", "f", "-exec", "stat", "-c", "%Y %s %n", "{}", "+",
	}, nil)
	if err != nil {
		if isMissingRemoteDirectory(stderr, err) {
			return map[string]remoteEntry{}, nil
		}
		return nil, fmt.Errorf("list remote files: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}
	return parseRemoteStatOutput(stdout, remoteRoot), nil
}

func collectLocalFiles(localRoot string, skipWeb bool) (map[string]localFile, error) {
	localFiles := map[string]localFile{}

	ignore, err := loadDockerignore(localRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dockerignoreFileName, err)
	}

	err = filepath.WalkDir(localRoot, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, err := filepath.Rel(localRoot, p)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)

		if d.IsDir() {
			if relPath == "." {
				return nil
			}
			name := d.Name()
			if _, excluded := excludedDirectories[name]; excluded {
				return filepath.SkipDir
			}
			if skipWeb && name == "web" {
				return filepath.SkipDir
			}
			if ignore.canSkipDir(relPath) {
				return filepath.SkipDir
			}
			return nil
		}

		if ignore.matches(relPath) {
			return nil
		}

		fi, err := d.Info()
		if err != nil {
			return err
		}
		localFiles[relPath] = localFile{
			relPath:   relPath,
			localPath: p,
			mtimeNs:   fi.ModTime().UnixNano(),
			size:      fi.Size(),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return localFiles, nil
}

// addRequiredFiles adds files the build needs even if exclusions dropped them.
// Like Docker, an ignored Dockerfile is still sent. Missing files are left for the build to report.
func addRequiredFiles(localFiles map[string]localFile, localRoot string, requiredFiles []string) error {
	for _, rel := range requiredFiles {
		rel = path.Clean(rel)
		if _, ok := localFiles[rel]; ok || !filepath.IsLocal(filepath.FromSlash(rel)) {
			continue
		}
		p := filepath.Join(localRoot, filepath.FromSlash(rel))
		fi, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("stat required file %s: %w", rel, err)
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		localFiles[rel] = localFile{
			relPath:   rel,
			localPath: p,
			mtimeNs:   fi.ModTime().UnixNano(),
			size:      fi.Size(),
		}
	}
	return nil
}

func buildSyncPlan(localFiles map[string]localFile, remoteFiles map[string]remoteEntry) syncPlan {
	plan := syncPlan{
		toUpload: []localFile{},
		toDelete: []string{},
	}

	localKeys := make([]string, 0, len(localFiles))
	for rel := range localFiles {
		localKeys = append(localKeys, rel)
	}
	sort.Strings(localKeys)

	for _, rel := range localKeys {
		lf := localFiles[rel]
		remote, ok := remoteFiles[rel]
		if !ok || shouldUpload(lf, remote) {
			plan.toUpload = append(plan.toUpload, lf)
		}
	}

	for rel := range remoteFiles {
		if _, exists := localFiles[rel]; !exists {
			plan.toDelete = append(plan.toDelete, rel)
		}
	}
	sort.Strings(plan.toDelete)

	return plan
}

func shouldUpload(local localFile, remote remoteEntry) bool {
	if local.size != remote.size {
		return true
	}
	// Compare whole seconds; subsecond differences do not trigger an upload.
	return local.mtimeNs/1_000_000_000 > remote.mtimeNs/1_000_000_000
}

func streamTarUpload(ctx context.Context, client *kube.Client, projectName string, files []localFile) error {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)

	go func() {
		errCh <- writeTarStream(pw, projectName, files)
	}()

	_, stderr, execErr := execInBuildPod(ctx, client, []string{
		"tar", "xzf", "-", "-C", workspacePath,
	}, pr)
	_ = pr.CloseWithError(execErr)
	streamErr := <-errCh

	if streamErr != nil {
		return streamErr
	}
	if execErr != nil {
		return fmt.Errorf("extract tar in build pod: %w (stderr: %s)", execErr, strings.TrimSpace(stderr))
	}
	return nil
}

func writeTarStream(pw *io.PipeWriter, projectName string, files []localFile) (err error) {
	gzw := gzip.NewWriter(pw)
	tw := tar.NewWriter(gzw)
	defer func() {
		err = errors.Join(err, tw.Close(), gzw.Close())
		_ = pw.CloseWithError(err)
	}()

	for _, f := range files {
		fi, err := os.Lstat(f.localPath)
		if err != nil {
			return fmt.Errorf("stat upload file %s: %w", f.relPath, err)
		}

		linkTarget := ""
		if fi.Mode()&os.ModeSymlink != 0 {
			linkTarget, err = os.Readlink(f.localPath)
			if err != nil {
				return fmt.Errorf("read symlink %s: %w", f.relPath, err)
			}
		}

		hdr, err := tar.FileInfoHeader(fi, linkTarget)
		if err != nil {
			return fmt.Errorf("create tar header %s: %w", f.relPath, err)
		}
		hdr.Name = path.Join(projectName, f.relPath)
		hdr.Mode = int64(fi.Mode().Perm())
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write tar header %s: %w", f.relPath, err)
		}

		if fi.Mode().IsRegular() {
			r, err := os.Open(f.localPath)
			if err != nil {
				return fmt.Errorf("open upload file %s: %w", f.relPath, err)
			}
			if _, err := io.Copy(tw, r); err != nil {
				_ = r.Close()
				return fmt.Errorf("write tar payload %s: %w", f.relPath, err)
			}
			_ = r.Close()
		}
	}

	return nil
}

func deleteRemoteFiles(ctx context.Context, client *kube.Client, remoteRoot string, relPaths []string) error {
	safeRoot := path.Clean(remoteRoot) + "/"
	var input bytes.Buffer
	for _, rel := range relPaths {
		abs := path.Clean(path.Join(remoteRoot, rel))
		if !strings.HasPrefix(abs+"/", safeRoot) {
			return fmt.Errorf("refusing to delete path outside project root: %s", rel)
		}
		input.WriteString(abs)
		input.WriteByte(0)
	}

	_, stderr, err := execInBuildPod(ctx, client, []string{
		"/bin/sh", "-c", "xargs -0 -r rm -rf --",
	}, bytes.NewReader(input.Bytes()))
	if err != nil {
		return fmt.Errorf("delete remote files: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}
	return nil
}

// parses "<mtime seconds> <size> <absolute path>" lines and keys them by path
func parseRemoteStatOutput(output string, remoteRoot string) map[string]remoteEntry {
	prefix := path.Clean(remoteRoot) + "/"
	entries := map[string]remoteEntry{}
	for line := range strings.SplitSeq(output, "\n") {
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			continue
		}
		rel, ok := strings.CutPrefix(parts[2], prefix)
		if !ok || rel == "" {
			continue
		}

		mtimeSec, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			continue
		}
		size, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		entries[rel] = remoteEntry{mtimeNs: mtimeSec * 1_000_000_000, size: size}
	}
	return entries
}

func isMissingRemoteDirectory(stderr string, execErr error) bool {
	if execErr == nil {
		return false
	}
	msg := strings.ToLower(stderr + " " + execErr.Error())
	return strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "cannot access")
}
