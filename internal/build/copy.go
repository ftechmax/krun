package build

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ftechmax/krun/internal/utils"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func enablePortForwarding(kubeConfig string, localPort int) (*os.Process, error) {
	cmd := exec.Command("kubectl", "--kubeconfig="+kubeConfig, "port-forward", "pod/"+buildPodName, fmt.Sprintf("%d:22", localPort))
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Wait for the port-forward to be ready
	buf := make([]byte, 4096)
	for {
		n, err := stdoutPipe.Read(buf)
		if n > 0 {
			output := string(buf[:n])
			if strings.Contains(output, "Forwarding from") {
				break
			}
		}
		if n == 0 {
			// EOF reached, port-forward likely failed
			fmt.Println("Port-forward process exited before ready")
			cmd.Process.Kill()
			return nil, fmt.Errorf("port-forward process exited unexpectedly")
		}
		if err != nil {
			cmd.Process.Kill()
			return nil, fmt.Errorf("port-forward failed to start: %w", err)
		}
	}
	return cmd.Process, nil
}

func disablePortforwarding(pfProcess *os.Process) {
	if pfProcess != nil {
		if err := pfProcess.Kill(); err != nil {
			fmt.Println("Failed to kill port forwarding process:", err)
		}
	} else {
		fmt.Println("No port forwarding process to kill")
	}
}

func copySource(kubeConfig string, projectName string, skipWeb bool, password string) (bool, error) {
	filesCopied := 0

	fmt.Printf("Copying project %s to remote server\n", projectName)

	// Port forward the SFTP port to the local machine
	localPort, _ := utils.GetFreePort()
	pfProcess, pfErr := enablePortForwarding(kubeConfig, localPort)
	if pfErr != nil {
		return false, fmt.Errorf("Failed to port forward SFTP port: %w", pfErr)
	}
	defer disablePortforwarding(pfProcess)

	// Use SFTP to incrementally sync the build context to the remote pod
	sftpUser := "user"
	sftpPass := password
	sftpAddr := fmt.Sprintf("%s:%d", "localhost", localPort)

	sshConfig := &ssh.ClientConfig{
		User: sftpUser,
		Auth: []ssh.AuthMethod{
			ssh.Password(sftpPass),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	// Connect to remote server
	var client *ssh.Client
	var err error
	maxRetries := 5
	for i := range maxRetries {
		client, err = ssh.Dial("tcp4", sftpAddr, sshConfig)
		if err == nil {
			break
		}
		fmt.Println(utils.Colorize(fmt.Sprintf("Failed to dial (attempt %d/%d): %s", i+1, maxRetries, err.Error()), utils.Red))
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return false, fmt.Errorf("Failed to dial after %d attempts: %w", maxRetries, err)
	}
	defer client.Close()

	// Create new SFTP client
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return false, fmt.Errorf("Failed to create SFTP client: %w", err)
	}
	defer sftpClient.Close()
	
	remotePath := filepath.ToSlash(filepath.Join(sftpWorkspacePath, projectName))
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
					return nil
				}
			}

			fmt.Println("Copying file:", path)
			filesCopied++

			// Create remote file (Linux path)
			dstFile, err := sftpClient.Create(remoteFile)
			if err != nil {
				return fmt.Errorf("Failed to create remote file: %w", err)
			}
			defer dstFile.Close()

			// Copy local file to remote file
			_, err = io.Copy(dstFile, srcFile)
			if err != nil {
				return fmt.Errorf("Failed to copy file: %w", err)
			}
		}
		return nil
	})

	if err != nil {
		return false, fmt.Errorf("Error walking the path: %w", err)
	}

	if filesCopied > 0 {
		return true, nil
	} else {
		return false, nil
	}
}
