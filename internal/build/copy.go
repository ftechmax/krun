package build

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func copySource(projectName string, skipWeb bool, remotePort int32, password string) bool {
	filesCopied := 0

	fmt.Printf("Copying project %s to remote server\n", projectName)

	// Use SFTP to incrementally sync the build context to the remote pod
	sftpUser := "user"
	sftpPass := password
	sftpAddr := fmt.Sprintf("%s:%d", config.Hostname, remotePort)

	sshConfig := &ssh.ClientConfig{
		User: sftpUser,
		Auth: []ssh.AuthMethod{
			ssh.Password(sftpPass),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	// Connect to remote server
	client, err := ssh.Dial("tcp", sftpAddr, sshConfig)
	if err != nil {
		panic("Failed to dial: " + err.Error())
	}
	defer client.Close()

	// Create new SFTP client
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		panic("Failed to create SFTP client: " + err.Error())
	}
	defer sftpClient.Close()

	remotePath := filepath.ToSlash(filepath.Join(stfpWorkspacePath, projectName))
	serviceDir := filepath.ToSlash(filepath.Join(config.KrunSourceConfig.Path, projectName))

	err = filepath.Walk(serviceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories
		if info.IsDir() {
			base := info.Name()

			switch base {
			case ".github", ".vs", ".git", ".angular", "bin", "obj", "node_modules", "k8s", "docs":
				return filepath.SkipDir
			}

			// Skip web directory if skipWeb is true
			if skipWeb && base == "web" {
				return filepath.SkipDir
			}

			// Create remote directory (Linux path)
			relPath, err := filepath.Rel(serviceDir, path)
			if err != nil {
				fmt.Println("Failed to get relative path:", err)
				return err
			}
			remoteDir := filepath.ToSlash(filepath.Join(remotePath, relPath))
			err = sftpClient.MkdirAll(remoteDir)
			if err != nil {
				fmt.Println("Failed to create remote directory:", err)
				return err
			}
		}

		if !info.IsDir() {
			srcFile, err := os.Open(path)
			if err != nil {
				fmt.Println("Failed to open file:", err)
				return err
			}
			defer srcFile.Close()

			relPath, err := filepath.Rel(serviceDir, path)
			if err != nil {
				fmt.Println("Failed to get relative path:", err)
				return err
			}

			remoteFile := filepath.ToSlash(filepath.Join(remotePath, relPath))

			// Check if remote file exists and is older
			remoteInfo, err := sftpClient.Stat(remoteFile)
			if err == nil {
				if !info.ModTime().After(remoteInfo.ModTime()) {
					fmt.Println("Skipping (up-to-date):", path)
					return nil
				}
			}

			fmt.Println("Copying file:", path)
			filesCopied++

			// Create remote file (Linux path)
			dstFile, err := sftpClient.Create(remoteFile)
			if err != nil {
				panic("Failed to create remote file: " + err.Error())
			}
			defer dstFile.Close()

			// Copy local file to remote file
			_, err = io.Copy(dstFile, srcFile)
			if err != nil {
				panic("Failed to copy file: " + err.Error())
			}
		}
		return nil
	})

	if err != nil {
		fmt.Println("Error walking the path:", err)
	}

	if filesCopied > 0 {
		return true
	} else {
		return false
	}
}
