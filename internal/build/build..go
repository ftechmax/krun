package build

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/pkg/sftp"
	cfg "github.com/voortman-steel-machinery/krun/internal/config"
	"github.com/voortman-steel-machinery/krun/internal/utils"
	"golang.org/x/crypto/ssh"
)

var config cfg.Config

func Build(projectName string, servicesToBuild []cfg.Service, skipWeb bool, force bool, cfg cfg.Config) {
	config = cfg
	
	fmt.Printf("Building project %s\n", projectName)
		
	needsBuild := copySource(projectName, skipWeb)
	if needsBuild || force {
		startBuildContainer(config.KubeConfig)
		for _, s := range servicesToBuild {
			if skipWeb && filepath.Base(s.Path) == "web" {
				continue
			}
			buildService(s)
		}
	} else {
		fmt.Printf("No changes detected in project %s, skipping build\n", projectName)
	}
}

func buildService(service cfg.Service) {
	fmt.Printf("Building service %s\n", service.Name)
	buildAndPushImagesDind(service, config.Registry, config.KubeConfig)
}

func startBuildContainer(kubeConfig string) {
	fmt.Println("\033[32mCreating build container\033[0m")
	createBuildPod(kubeConfig)
    time.Sleep(5 * time.Second) // Give docker daemon some time to spin up
}

func buildAndPushImagesDind(service cfg.Service, registry string, kubeConfig string) {
	contextPath := filepath.ToSlash(filepath.Join("/workspace", service.Project, service.Context))
	dockerfilePath := filepath.ToSlash(filepath.Join("/workspace", service.Project, service.Path, service.Dockerfile, "Dockerfile"))

    cmd := fmt.Sprintf(
        "docker buildx build -t %s/%s -f %s %s ; docker push %s/%s:latest",
        registry, service.Name, dockerfilePath, contextPath, registry, service.Name,
    )
    utils.RunCmd("kubectl", "--kubeconfig="+kubeConfig, "exec", "pod/docker-build", "--", "/bin/sh", "-c", cmd)
}

func copySource(projectName string, skipWeb bool) bool {
	filesCopied := 0

	fmt.Printf("Copying project %s to remote server\n", projectName)

	key, err := os.ReadFile(config.PrivateKey)
	if err != nil {
		fmt.Errorf("unable to read private key: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		fmt.Errorf("unable to parse private key: %v", err)
	}

	sshConfig := &ssh.ClientConfig{
		User: config.Username,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	// Connect to remote server
	client, err := ssh.Dial("tcp", fmt.Sprintf("%s:22", config.Hostname), sshConfig)
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

	remotePath := fmt.Sprintf("/workspace/%s", projectName)
	serviceDir := fmt.Sprintf("%s/%s", config.KrunSourceConfig.Path, projectName)

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