package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"context"

	"github.com/jedib0t/go-pretty/table"
	"golang.org/x/sys/windows"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const pipeName = `\\.\pipe\krunhelper`
const krunHostsBlockStart = "##### KRUN #####"
const krunHostsBlockEnd = "##### END KRUN #####"
const windowsHostsFile = `C:\Windows\System32\drivers\etc\hosts`
const windowsHostsTempFile = windowsHostsFile + ".krun.tmp"

type PipeCommand struct {
	Command       string `json:"command"`
	KubeConfig    string `json:"kubeconfig"`
	ServiceName   string `json:"service_name,omitempty"`
	ServicePath   string `json:"service_path,omitempty"`
	ServicePort   int    `json:"service_port,omitempty"`
	Intercept     bool   `json:"intercept,omitempty"`
	InterceptPort int    `json:"intercept_port,omitempty"`
}

type PipeResponse struct {
	Message string `json:"message"`
}

type TelepresenceStatus struct {
	UserDaemon struct {
		Running bool `json:"running"`
	} `json:"user_daemon"`
	RootDaemon struct {
		Running bool `json:"running"`
	} `json:"root_daemon"`
}

type TelepresenceList struct {
	Cmd    string                   `json:"cmd"`
	Stdout []TelepresenceListStdout `json:"stdout"`
}

type TelepresenceListStdout struct {
	Name                 string                          `json:"name"`
	Namespace            string                          `json:"namespace"`
	InterceptInfo        []TelepresenceListInterceptInfo `json:"intercept_info,omitempty"`
	WorkloadResourceType string                          `json:"workload_resource_type"`
	UID                  string                          `json:"uid"`
	AgentVersion         string                          `json:"agent_version,omitempty"`
}

type TelepresenceListInterceptInfo struct {
	Spec             TelepresenceListSpec `json:"spec"`
	ID               string               `json:"id"`
	Disposition      int                  `json:"disposition"`
	PodName          string               `json:"pod_name"`
	APIPort          int                  `json:"api_port"`
	PodIP            string               `json:"pod_ip"`
	SFTPPort         int                  `json:"sftp_port"`
	FTPPort          int                  `json:"ftp_port"`
	ClientMountPoint string               `json:"client_mount_point"`
	MountPoint       string               `json:"mount_point"`
	MechanismArgsDesc string              `json:"mechanism_args_desc"`
	Environment      map[string]string    `json:"environment"`
}

type TelepresenceListSpec struct {
	Name            string `json:"name"`
	Client          string `json:"client"`
	Agent           string `json:"agent"`
	WorkloadKind    string `json:"workload_kind"`
	Namespace       string `json:"namespace"`
	Mechanism       string `json:"mechanism"`
	TargetHost      string `json:"target_host"`
	PortIdentifier  string `json:"port_identifier"`
	ServicePortName string `json:"service_port_name"`
	ServicePort     int    `json:"service_port"`
	ServiceUID      string `json:"service_uid"`
	Protocol        string `json:"protocol"`
	ContainerName   string `json:"container_name"`
	ContainerPort   int    `json:"container_port"`
	TargetPort      int    `json:"target_port"`
	RoundtripLatency int64 `json:"roundtrip_latency"`
	DialTimeout     int64  `json:"dial_timeout"`
	Replace         bool   `json:"replace"`
	NoDefaultPort   bool   `json:"no_default_port"`
}

var (
	infoLog = log.New(os.Stdout, "INFO: ", log.Ldate|log.Ltime)
	warningLog = log.New(os.Stdout, "WARNING: ", log.Ldate|log.Ltime)
	errorLog = log.New(os.Stderr, "ERROR: ", log.Ldate|log.Ltime)
)

func main() {
	// Create a channel to receive OS signals
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	// Channel to notify main loop to exit
	done := make(chan bool, 1)

	// Start a goroutine to listen for signals
	go func() {
		<-sigs
		fmt.Println("\nSignal received! Running shutdown code...")
		runCleanup()
		done <- true
	}()

	infoLog.Println("Helper started and running persistently")

	for {
		// Create a fresh named pipe instance each loop
		handle, err := windows.CreateNamedPipe(
			windows.StringToUTF16Ptr(pipeName),
			windows.PIPE_ACCESS_DUPLEX,
			windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT,
			1,
			1024,
			1024,
			0,
			getPipeSecurityAttributes(),
		)
		if err != nil {
			errorLog.Println("CreateNamedPipe failed:", err)
			time.Sleep(2 * time.Second)
			continue
		}

		infoLog.Println("Waiting for client connection...")
		err = windows.ConnectNamedPipe(handle, nil)
		if err != nil {
			errorLog.Println("ConnectNamedPipe failed:", err)
			windows.CloseHandle(handle)
			continue
		}

		infoLog.Println("Client connected")

		file := os.NewFile(uintptr(handle), "pipe")
		reader := bufio.NewReader(file)
		writer := bufio.NewWriter(file)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					infoLog.Println("Client disconnected")
				} else if strings.Contains(err.Error(), "The handle is invalid") {
					infoLog.Println("Pipe handle is invalid (client likely disconnected)")
				} else {
					errorLog.Println("Error reading from pipe:", err)
				}
				file.Close() // Always close file on error
				break
			}

			var cmd PipeCommand
			err = json.Unmarshal([]byte(line), &cmd)
			if err != nil {
				errorLog.Println("Failed to unmarshal command:", err)
				continue
			}

			startTelepresence(cmd.KubeConfig)

			switch cmd.Command {
			case "debug_enable":
				debugEnable(cmd)
				writeResponseBytes(writer, "")
			case "debug_disable":
				debugDisable(cmd)
				writeResponseBytes(writer, "")
			case "debug_list":
				msg := debugList(cmd)
				writeResponseBytes(writer, msg)
			default:
				warningLog.Printf("Unknown command: %s\n", cmd.Command)
				writeResponseBytes(writer, "")
				continue
			}
		}

		windows.DisconnectNamedPipe(handle)
		windows.CloseHandle(handle)
	}
}

func runCleanup() {
	infoLog.Println("Cleaning up resources...")
	stopTelepresence()
	os.Exit(0)
}

func debugEnable(cmd PipeCommand) {
	infoLog.Printf("Enabling debug mode with kubeconfig: %s\n", cmd.KubeConfig)

	freePort, err := getFreePort()
	if err != nil {
		errorLog.Printf("Failed to get free port: %v\n", err)
		return
	}

	mode := "replace"
	port := fmt.Sprintf("%d:%d", freePort, cmd.ServicePort)
	if cmd.Intercept {
		mode = "intercept"
		port = fmt.Sprintf("%d", cmd.InterceptPort)
	}

	execCmd := exec.Command("telepresence", mode, cmd.ServiceName, "--port", port, "--env-file", filepath.Join(cmd.ServicePath, "appsettings-debug.env"))
	execCmd.Stdout = os.Stdout
	execCmd.Stderr = os.Stderr
	err = execCmd.Run()
	if err != nil {
		errorLog.Printf("Command execution failed: %v\n", err)
	}
}

func debugDisable(cmd PipeCommand) {
	infoLog.Printf("Disabling debug mode with kubeconfig: %s\n", cmd.KubeConfig)

	execCmd := exec.Command("telepresence", "leave", cmd.ServiceName)
	execCmd.Stdout = os.Stdout
	execCmd.Stderr = os.Stderr
	err := execCmd.Run()
	if err != nil {
		errorLog.Printf("Command execution failed: %v\n", err)
	}
}

func debugList(cmd PipeCommand) string {
	infoLog.Printf("Listing debug services with kubeconfig: %s\n", cmd.KubeConfig)

	execCmd := exec.Command("telepresence", "list", "--output", "json")
	output, err := execCmd.Output()
	if err != nil {
		errorLog.Printf("Command execution failed: %v\n", err)
		return ""
	}

	var list TelepresenceList
	err = json.Unmarshal(output, &list)
	if err != nil {
		errorLog.Printf("Failed to unmarshal telepresence list: %v\n", err)
		return ""
	}

	t := table.NewWriter()
	t.AppendHeader(table.Row{"Service", "Replaced", "Forward"})

	for _, service := range list.Stdout {
		forwards := "-"
		replaced := "No"

		if  service.InterceptInfo != nil && len(service.InterceptInfo) > 0 {
			replaced = "Yes"
			for _, intercept := range service.InterceptInfo {
				forwards = fmt.Sprintf("%s:%d->%s:%d", intercept.PodIP, intercept.Spec.ContainerPort, intercept.Spec.TargetHost, intercept.Spec.TargetPort)
			}
		}

		t.AppendRow([]interface{}{service.Name, replaced, forwards})
	}

	return t.Render()
}

func getTelepresenceStatus() (TelepresenceStatus, error) {
	cmd := exec.Command("telepresence", "status", "--output", "json")
	output, err := cmd.Output()
	if err != nil {
		return TelepresenceStatus{}, fmt.Errorf("failed to get telepresence status: %w", err)
	}

	var status TelepresenceStatus
	err = json.Unmarshal(output, &status)
	if err != nil {
		return TelepresenceStatus{}, fmt.Errorf("failed to unmarshal telepresence status: %w", err)
	}

	return status, nil
}

func getFreePort() (port int, err error) {
	var a *net.TCPAddr
	if a, err = net.ResolveTCPAddr("tcp", "localhost:0"); err == nil {
		var l *net.TCPListener
		if l, err = net.ListenTCP("tcp", a); err == nil {
			defer l.Close()
			return l.Addr().(*net.TCPAddr).Port, nil
		}
	}
	return
}

func startTelepresence(kubeConfig string) error {
	status, err := getTelepresenceStatus()
	if err != nil {
		return fmt.Errorf("failed to get telepresence status: %w", err)
	}

	if !status.UserDaemon.Running || !status.RootDaemon.Running {
		execCmd := exec.Command("telepresence", "connect", "--kubeconfig", kubeConfig)
		execCmd.Stdout = os.Stdout
		execCmd.Stderr = os.Stderr
		err := execCmd.Run()
		if err != nil {
			return fmt.Errorf("failed to start telepresence: %w", err)
		}
	}

	infoLog.Println("Telepresence is running")

	// Inject ClusterIP services into hosts file
	if err := injectClusterIPServicesToHosts(kubeConfig); err != nil {
		warningLog.Printf("Failed to inject cluster services into hosts file: %v\n", err)
	}
	return nil
}

func stopTelepresence() error {
	// Remove KRUN block from hosts file
	if err := removeKrunBlockFromHosts(); err != nil {
		warningLog.Printf("Failed to remove KRUN block from hosts file: %v\n", err)
	}

	execCmd := exec.Command("telepresence", "quit")
	execCmd.Stdout = os.Stdout
	execCmd.Stderr = os.Stderr
	err := execCmd.Run()
	if err != nil {
		return fmt.Errorf("failed to stop telepresence: %w", err)
	}

	infoLog.Println("Telepresence has been stopped")
	return nil
}

func injectClusterIPServicesToHosts(kubeConfig string) error {
	config, err := clientcmd.BuildConfigFromFlags("", kubeConfig)
	if err != nil {
		return fmt.Errorf("failed to build kubeconfig: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create k8s client: %w", err)
	}

	services, err := clientset.CoreV1().Services("").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list services: %w", err)
	}

	var block strings.Builder
	block.WriteString(krunHostsBlockStart + "\n")
	for _, svc := range services.Items {
		if svc.Spec.Type == "ClusterIP" && svc.Spec.ClusterIP != "None" && svc.Spec.ClusterIP != "" {
			host := fmt.Sprintf("%s.%s.svc", svc.Name, svc.Namespace)
			block.WriteString(fmt.Sprintf("%s %s\n", svc.Spec.ClusterIP, host))
		}
	}
	block.WriteString(krunHostsBlockEnd + "\n")

	return updateHostsFileWithBlock(block.String())
}

func updateHostsFileWithBlock(block string) error {
	data, err := os.ReadFile(windowsHostsFile)
	if err != nil {
		return fmt.Errorf("failed to read hosts file: %w", err)
	}
	content := string(data)

	startIdx := strings.Index(content, krunHostsBlockStart)
	endIdx := strings.Index(content, krunHostsBlockEnd)

	// Trim any extra newlines at the end of the file
	content = strings.TrimSuffix(content, "\n")

	if startIdx != -1 && endIdx != -1 && endIdx > startIdx {
		// Remove old block
		content = content[:startIdx] + content[endIdx+len(krunHostsBlockEnd):]
	}

	// Ensure trailing newline
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	content += block
	if err := writeHostsFile(content); err != nil {
		return fmt.Errorf("failed to write updated hosts file: %w", err)
	}

	return nil
}

func removeKrunBlockFromHosts() error {
	data, err := os.ReadFile(windowsHostsFile)
	if err != nil {
		return fmt.Errorf("failed to read hosts file: %w", err)
	}
	content := string(data)

	startIdx := strings.Index(content, krunHostsBlockStart)
	endIdx := strings.Index(content, krunHostsBlockEnd)

	if startIdx != -1 && endIdx != -1 && endIdx > startIdx {
		// Remove block
		content = content[:startIdx] + content[endIdx+len(krunHostsBlockEnd):]

		if err := writeHostsFile(content); err != nil {
			return fmt.Errorf("failed to write updated hosts file: %w", err)
		}
	}

	return nil
}

func writeHostsFile(content string) error {
	if err := os.WriteFile(windowsHostsTempFile, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write temp hosts file: %w", err)
	}
	if err := os.Rename(windowsHostsTempFile, windowsHostsFile); err != nil {
		return fmt.Errorf("failed to atomically replace hosts file: %w", err)
	}
	return nil
}

func getPipeSecurityAttributes() *windows.SecurityAttributes {
	sd := "D:(A;OICI;GRGW;;;WD)" // Allow Everyone read/write
	securityDescriptor, err := windows.SecurityDescriptorFromString(sd)
	if err != nil {
		errorLog.Println("Failed to create security descriptor:", err)
		return nil
	}
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: securityDescriptor,
		InheritHandle:      0,
	}
}

func writeResponseBytes(writer *bufio.Writer, message string) {
	response := PipeResponse{
		Message: message,
	}
	responseBytes, err := json.Marshal(response)
	if err != nil {
		errorLog.Println("Failed to marshal response:", err)
		return
	}

	responseBytes = append(responseBytes, '\n')
	writer.Write(responseBytes)
	writer.Flush()
}