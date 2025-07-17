package debug

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/utils"
	"golang.org/x/sys/windows"
)

const pipeName = `\\.\pipe\krunhelper`

func isHelperRunning() bool {
	output, err := exec.Command("tasklist", "/FI", "IMAGENAME eq krunhelper.exe").Output()
	if err != nil {
		fmt.Println("tasklist failed:", err)
		return false
	}
	return strings.Contains(string(output), "krunhelper.exe")
}

func startHelperElevated() error {
	exePath, _ := utils.GetExecutablePath()
	exeDir := filepath.Dir(exePath)

	cmd := exec.Command("powershell", "-Command", fmt.Sprintf("Start-Process -Verb runAs -FilePath '%s'", filepath.Join(exeDir, "krunhelper.exe")))
	cmd.Dir = exeDir
	return cmd.Run()
}

func connectToPipeWithRetry() (windows.Handle, error) {
	var pipe windows.Handle
	var err error
	start := time.Now()
	for time.Since(start) < 10*time.Second {
		pipe, err = windows.CreateFile(
			windows.StringToUTF16Ptr(pipeName),
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0,
			nil,
			windows.OPEN_EXISTING,
			0,
			0,
		)
		if err == nil {
			return pipe, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return 0, err
}

func pipeRead(reader *bufio.Reader) (contracts.PipeResponse, error) {
	response, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println("Failed to read response from helper:", err)
		return contracts.PipeResponse{}, err
	}

	var pipeResponse contracts.PipeResponse
	err = json.Unmarshal([]byte(response), &pipeResponse)
	if err != nil {
		fmt.Println("Failed to unmarshal response:", err)
		return contracts.PipeResponse{}, err
	}

	return pipeResponse, nil
}

func pipeWrite(writer *bufio.Writer, cmd contracts.PipeCommand) error {
	cmdJSON, _ := json.Marshal(cmd)
	cmdJSON = append(cmdJSON, '\n')
	_, err := writer.Write(cmdJSON)
	writer.Flush()
	if err != nil {
		fmt.Println("Write failed:", err)
	}

	return nil
}