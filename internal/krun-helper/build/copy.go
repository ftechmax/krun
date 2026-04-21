package build

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
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
	".claude":      {},
}

func copySource(ctx context.Context, out io.Writer, kubeConfig string, projectName string, projectPath string, skipWeb bool) (bool, error) {
	if out == nil {
		out = io.Discard
	}

	startTime := time.Now()
	plusSym := utils.Colorize("+", utils.Green)
	minusSym := utils.Colorize("-", utils.Red)
	squigglySym := utils.Colorize("~", utils.Yellow)

	fmt.Fprintf(out, "Copying project %s to remote server\n", projectName)

	projectRelPath := projectName
	if projectPath != "" {
		projectRelPath = projectPath
	}
	localRoot := filepath.ToSlash(filepath.Join(config.Path, filepath.FromSlash(projectRelPath)))
	remoteRoot := path.Join(workspacePath, projectName)

	client, err := kube.NewClient(kubeConfig)
	if err != nil {
		return false, fmt.Errorf("create kube client: %w", err)
	}

	remoteFiles, err := listRemoteFiles(ctx, client, remoteRoot)
	if err != nil {
		return false, err
	}

	localFiles, err := collectLocalFiles(localRoot, skipWeb)
	if err != nil {
		return false, fmt.Errorf("walk local source tree: %w", err)
	}

	plan := buildSyncPlan(localFiles, remoteFiles)

	filesAdded, filesUpdated := reportUploadChanges(out, plan.toUpload, remoteFiles, plusSym, squigglySym)
	filesDeleted := reportDeletedChanges(out, plan.toDelete, minusSym)

	if err := applySyncPlan(ctx, client, projectName, remoteRoot, plan); err != nil {
		return false, err
	}

	return summarizeSyncResult(out, startTime, filesAdded, filesUpdated, filesDeleted), nil
}

func reportUploadChanges(out io.Writer, uploadFiles []localFile, remoteFiles map[string]remoteEntry, addSymbol string, updateSymbol string) (added int, updated int) {
	for _, file := range uploadFiles {
		if _, existed := remoteFiles[file.relPath]; existed {
			updated++
			fmt.Fprintf(out, "%s %s\n", updateSymbol, file.relPath)
			continue
		}
		added++
		fmt.Fprintf(out, "%s %s\n", addSymbol, file.relPath)
	}
	return added, updated
}

func reportDeletedChanges(out io.Writer, deletedFiles []string, deleteSymbol string) int {
	for _, relPath := range deletedFiles {
		fmt.Fprintf(out, "%s %s\n", deleteSymbol, relPath)
	}
	return len(deletedFiles)
}

func applySyncPlan(ctx context.Context, client *kube.Client, projectName string, remoteRoot string, plan syncPlan) error {
	if len(plan.toUpload) > 0 {
		if err := streamTarUpload(ctx, client, projectName, plan.toUpload); err != nil {
			return err
		}
	}
	if len(plan.toDelete) > 0 {
		if err := deleteRemoteFiles(ctx, client, remoteRoot, plan.toDelete); err != nil {
			return err
		}
	}
	return nil
}

func summarizeSyncResult(out io.Writer, startTime time.Time, filesAdded int, filesUpdated int, filesDeleted int) bool {
	elapsed := time.Since(startTime)
	shortDur := elapsed.Truncate(10 * time.Millisecond)
	if filesAdded == 0 && filesUpdated == 0 && filesDeleted == 0 {
		fmt.Fprintf(out, "%s | %s %s\n",
			utils.Colorize("No changes - project up to date", utils.Gray),
			utils.Colorize("Completed sync in", utils.Gray),
			utils.Colorize(shortDur.String(), utils.Gray),
		)
		return false
	}

	addedStr := utils.Colorize(fmt.Sprintf("Added: %d", filesAdded), utils.Green)
	updatedStr := utils.Colorize(fmt.Sprintf("Updated: %d", filesUpdated), utils.Yellow)
	deletedStr := utils.Colorize(fmt.Sprintf("Deleted: %d", filesDeleted), utils.Red)
	timeLabel := utils.Colorize("Completed sync in", utils.Gray)
	timeVal := utils.Colorize(shortDur.String(), utils.Gray)

	fmt.Fprintf(out, "Sync summary: %s  %s  %s  | %s %s\n", addedStr, updatedStr, deletedStr, timeLabel, timeVal)
	return true
}

func listRemoteFiles(ctx context.Context, client *kube.Client, remoteRoot string) (map[string]remoteEntry, error) {
	stdout, stderr, err := execInBuildPod(ctx, client, []string{
		"find", remoteRoot, "-type", "f", "-printf", "%P\t%T@\t%s\n",
	}, nil)
	if err != nil {
		if isMissingRemoteDirectory(stderr, err) {
			return map[string]remoteEntry{}, nil
		}
		return nil, fmt.Errorf("list remote files: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}
	return parseRemoteFindOutput(stdout), nil
}

func collectLocalFiles(localRoot string, skipWeb bool) (map[string]localFile, error) {
	localFiles := map[string]localFile{}

	err := filepath.WalkDir(localRoot, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			if _, excluded := excludedDirectories[name]; excluded {
				return filepath.SkipDir
			}
			if skipWeb && name == "web" {
				return filepath.SkipDir
			}
			return nil
		}

		fi, err := d.Info()
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(localRoot, p)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)
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

func buildSyncPlan(localFiles map[string]localFile, remoteFiles map[string]remoteEntry) syncPlan {
	plan := syncPlan{
		toUpload: []localFile{},
		toDelete: []string{},
	}
	seenRemote := make(map[string]struct{}, len(localFiles))

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
		seenRemote[rel] = struct{}{}
	}

	for rel := range remoteFiles {
		if _, seen := seenRemote[rel]; !seen {
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
	// Match prior SFTP behavior: if local file is not newer than remote, skip upload.
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
	streamErr := <-errCh

	if streamErr != nil {
		return streamErr
	}
	if execErr != nil {
		return fmt.Errorf("extract tar in build pod: %w (stderr: %s)", execErr, strings.TrimSpace(stderr))
	}
	return nil
}

func writeTarStream(pw *io.PipeWriter, projectName string, files []localFile) error {
	gzw := gzip.NewWriter(pw)
	tw := tar.NewWriter(gzw)

	for _, f := range files {
		fi, err := os.Lstat(f.localPath)
		if err != nil {
			_ = tw.Close()
			_ = gzw.Close()
			_ = pw.CloseWithError(err)
			return fmt.Errorf("stat upload file %s: %w", f.relPath, err)
		}

		linkTarget := ""
		if fi.Mode()&os.ModeSymlink != 0 {
			linkTarget, err = os.Readlink(f.localPath)
			if err != nil {
				_ = tw.Close()
				_ = gzw.Close()
				_ = pw.CloseWithError(err)
				return fmt.Errorf("read symlink %s: %w", f.relPath, err)
			}
		}

		hdr, err := tar.FileInfoHeader(fi, linkTarget)
		if err != nil {
			_ = tw.Close()
			_ = gzw.Close()
			_ = pw.CloseWithError(err)
			return fmt.Errorf("create tar header %s: %w", f.relPath, err)
		}
		hdr.Name = path.Join(projectName, f.relPath)
		hdr.Mode = int64(fi.Mode().Perm())
		if err := tw.WriteHeader(hdr); err != nil {
			_ = tw.Close()
			_ = gzw.Close()
			_ = pw.CloseWithError(err)
			return fmt.Errorf("write tar header %s: %w", f.relPath, err)
		}

		if fi.Mode().IsRegular() {
			r, err := os.Open(f.localPath)
			if err != nil {
				_ = tw.Close()
				_ = gzw.Close()
				_ = pw.CloseWithError(err)
				return fmt.Errorf("open upload file %s: %w", f.relPath, err)
			}
			if _, err := io.Copy(tw, r); err != nil {
				_ = r.Close()
				_ = tw.Close()
				_ = gzw.Close()
				_ = pw.CloseWithError(err)
				return fmt.Errorf("write tar payload %s: %w", f.relPath, err)
			}
			_ = r.Close()
		}
	}

	if err := tw.Close(); err != nil {
		_ = gzw.Close()
		_ = pw.CloseWithError(err)
		return fmt.Errorf("close tar writer: %w", err)
	}
	if err := gzw.Close(); err != nil {
		_ = pw.CloseWithError(err)
		return fmt.Errorf("close gzip writer: %w", err)
	}
	if err := pw.Close(); err != nil {
		return fmt.Errorf("close pipe writer: %w", err)
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

func parseRemoteFindOutput(output string) map[string]remoteEntry {
	entries := map[string]remoteEntry{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 || parts[0] == "" {
			continue
		}

		mtimeNs, err := parseFindMtime(parts[1])
		if err != nil {
			continue
		}
		size, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			continue
		}
		entries[parts[0]] = remoteEntry{mtimeNs: mtimeNs, size: size}
	}
	return entries
}

func parseFindMtime(value string) (int64, error) {
	secFrac := strings.SplitN(value, ".", 2)
	sec, err := strconv.ParseInt(secFrac[0], 10, 64)
	if err != nil {
		return 0, err
	}
	frac := int64(0)
	if len(secFrac) == 2 {
		f := secFrac[1]
		for len(f) < 9 {
			f += "0"
		}
		frac, err = strconv.ParseInt(f[:9], 10, 64)
		if err != nil {
			return 0, err
		}
	}
	return sec*1_000_000_000 + frac, nil
}

func isMissingRemoteDirectory(stderr string, execErr error) bool {
	if execErr == nil {
		return false
	}
	msg := strings.ToLower(stderr + " " + execErr.Error())
	return strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "cannot access")
}
