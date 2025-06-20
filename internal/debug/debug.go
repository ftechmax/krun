package debug

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
	"golang.org/x/sys/windows"
)

func Enable(service cfg.Service, config cfg.Config) {

	cmd := contracts.PipeCommand{
		Command:     "debug_enable",
		KubeConfig:  config.KubeConfig,
		ServiceName: service.Name,
		ServicePath: filepath.ToSlash(filepath.Join(config.KrunSourceConfig.Path, service.Project, service.Path)),
		ServicePort: service.ContainerPort,
	}

	msg, err := writeCommand(cmd)
	if err != nil {
		fmt.Printf("Error enabling debug mode for service %s: %v\n", service.Name, err)
		return
	}

	if msg != "" {
		fmt.Println(msg)
	}

	fmt.Printf("Debug mode for service %s is now enabled\n", service.Name)
}

func Disable(service cfg.Service, config cfg.Config) {

	cmd := contracts.PipeCommand{
		Command:     "debug_disable",
		KubeConfig:  config.KubeConfig,
		ServiceName: service.Name,
	}

	msg, err := writeCommand(cmd)
	if err != nil {
		fmt.Printf("Error disabling debug mode for service %s: %v\n", service.Name, err)
		return
	}

	if msg != "" {
		fmt.Println(msg)
	}

	fmt.Printf("Debug mode for service %s is now disabled\n", service.Name)
}

func List(config cfg.Config) {

	cmd := contracts.PipeCommand{
		Command:    "debug_list",
		KubeConfig: config.KubeConfig,
	}

	msg, err := writeCommand(cmd)
	if err != nil {
		fmt.Printf("Error listing debug services: %v\n", err)
		return
	}

	if msg != "" {
		fmt.Println(msg)
	} else {
		fmt.Println("No debug services found")
	}
}

func startHelper() error {
	if !isHelperRunning() {
		fmt.Println("Helper not running, starting it...")
		err := startHelperElevated()
		if err != nil {
			fmt.Println("Failed to start helper:", err)
			return err
		}
	}

	return nil
}

func writeCommand(cmd contracts.PipeCommand) (string, error) {
	startHelper()

	pipe, err := connectToPipeWithRetry()
	if err != nil {
		return "", fmt.Errorf("could not connect to pipe: %w", err)
	}
	defer windows.CloseHandle(pipe)

	file := os.NewFile(uintptr(pipe), "pipe")
	if file == nil {
		return "", fmt.Errorf("failed to create file from pipe handle")
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	writer := bufio.NewWriter(file)

	pipeWrite(writer, cmd)
	response, err := pipeRead(reader)

	return response.Message, nil
}
