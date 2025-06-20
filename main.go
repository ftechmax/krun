package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jedib0t/go-pretty/table"
	"github.com/voortman-steel-machinery/krun/internal/build"
	cfg "github.com/voortman-steel-machinery/krun/internal/config"
	"github.com/voortman-steel-machinery/krun/internal/debug"
	"github.com/voortman-steel-machinery/krun/internal/deploy"
)

var (
	cacheFile = "krun.cache"
	cacheTtl  = 24 * time.Hour
	config    = cfg.Config{}
	version   = ""              // will be set by the build system
	services  = []cfg.Service{} // map of service name to service struct

	colorReset   = "\033[0m"
	colorRed     = "\033[31m"
	colorGreen   = "\033[32m"
	colorYellow  = "\033[33m"
	colorBlue    = "\033[34m"
	colorMagenta = "\033[35m"
	colorCyan    = "\033[36m"
	colorGray    = "\033[37m"
	colorWhite   = "\033[97m"
)

func printHelp() {
	fmt.Println(`krun.exe [global options] <command> [command options] <service>

Global Options:
  --kubeconfig <path>                            Path to kubeconfig file

Commands:
  help                                           Show this help message
  version                                        Show version information
  list                                           List all services or projects
  build [--skip-web, --force] <project|service>  Build a project or specific service
  deploy [--use-remote-registry] <project>       Deploy a project
  debug list                                     List all services with debug mode status
  debug enable <service>                         Enable debug mode for a service
  debug disable <service>                        Disable debug mode for a service\n`)
}

func main() {
	args := os.Args[1:]
	args, optKubeConfig := parseGlobalOptions(args)

	if len(args) == 0 || args[0] == "help" {
		printHelp()
		return
	}

	// initialize configuration
	initialize(optKubeConfig)

	switch args[0] {
	case "version":
		fmt.Println(version)
		return

	case "list":
		printServices()
		return

	case "build":
		skipWeb := false
		forceBuild := false
		argServiceName := ""
		for i := 1; i < len(args); i++ {
			if args[i] == "--skip-web" {
				skipWeb = true
			} else if args[i] == "--force" {
				forceBuild = true
			} else if argServiceName == "" {
				argServiceName = args[i]
			}
		}
		if argServiceName == "" {
			fmt.Println(colorRed + "Please specify a service name or project name to build." + colorReset)
			return
		}

		serviceName, projectName, err := getServiceNameAndProject(argServiceName)
		if err != nil {
			fmt.Println(colorRed + err.Error() + colorReset)
			return
		}

		servicesToBuild := []cfg.Service{}
		for _, s := range services {
			if serviceName != "" {
				if s.Name == serviceName {
					servicesToBuild = append(servicesToBuild, s)
					break
				}
			} else if projectName != "" {
				if s.Project == projectName {
					servicesToBuild = append(servicesToBuild, s)
				}
			}
		}

		build.Build(projectName, servicesToBuild, skipWeb, forceBuild, config)

		return

	case "deploy":
		argServiceName := ""
		for i := 1; i < len(args); i++ {
			if args[i] == "--use-remote-registry" {
				config.Registry = config.RemoteRegistry
			} else if argServiceName == "" {
				argServiceName = args[i]
			}
		}
		if argServiceName == "" {
			fmt.Println(colorRed + "Missing service name for deploy" + colorReset)
			return
		}

		_, projectName, err := getServiceNameAndProject(argServiceName)
		if err != nil {
			fmt.Println(colorRed + err.Error() + colorReset)
			return
		}

		deploy.Deploy(projectName, config)
		return

	case "debug":
		if len(args) < 2 {
			fmt.Println("Usage: krun.exe debug <enable|disable|list>")
			return
		}

		if args[1] == "list" {
			debug.List(config)
			return
		}

		// Enable or disable debug mode for a specific service
		if len(args) < 3 {
			fmt.Println("Usage: krun.exe debug <enable|disable> <service>")
			return
		}
		action := args[1]
		argServiceName := args[2]

		service := cfg.Service{}
		for _, s := range services {
			if s.Name == argServiceName {
				service = s
				break
			}
		}

		if action == "enable" {
			fmt.Printf("Enabling debug mode for service %s\n", argServiceName)
			debug.Enable(service, config)
		} else if action == "disable" {
			fmt.Printf("Disabling debug mode for service %s\n", argServiceName)
			debug.Disable(service, config)
		} else {
			fmt.Printf(colorRed+"Unknown debug action: %s\n"+colorReset, action)
		}
		return

	default:
		fmt.Printf(colorRed+"Unknown command: %s\n"+colorReset, args[0])
		printHelp()
	}
}

func printServices() {
	if len(services) == 0 {
		fmt.Println(colorYellow + "No services found" + colorReset)
		return
	}

	ts := table.NewWriter()
	ts.SetOutputMirror(os.Stdout)
	ts.AppendHeader(table.Row{"Available services"})
	for _, service := range services {
		ts.AppendRow(table.Row{service.Name})
	}
	ts.Render()

	fmt.Println("")

	tp := table.NewWriter()
	tp.SetOutputMirror(os.Stdout)
	tp.AppendHeader(table.Row{"Available projects"})
	projects := make(map[string]bool)
	for _, service := range services {
		if service.Project != "" {
			projects[service.Project] = true
		}
	}
	for project := range projects {
		tp.AppendRow(table.Row{project})
	}
	tp.Render()
}

func getServiceNameAndProject(name string) (string, string, error) {
	serviceName := ""
	projectName := ""
	for _, s := range services {
		if s.Name == name {
			serviceName = s.Name
			projectName = s.Project
			break
		}
		if s.Project == name {
			projectName = s.Project
			break
		}
	}

	if serviceName == "" && projectName == "" {
		return "", "", fmt.Errorf("Service or project '%s' not found\n", name)
	}

	return serviceName, projectName, nil
}

func parseGlobalOptions(args []string) ([]string, string) {
	kubeconfig := ""
	rest := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--kubeconfig":
			if i+1 < len(args) {
				kubeconfig = args[i+1]
				i++
			}
		default:
			rest = append(rest, args[i])
		}
	}
	return rest, kubeconfig
}

func initialize(optKubeConfig string) {
	krunConfig, err := cfg.ParseKrunConfig()
	if err != nil {
		fmt.Printf(colorRed+"Error parsing krun-config.json: %s\n"+colorReset, err)
		os.Exit(0)
	}

	// Check if User section is present
	if krunConfig.Username == "" || krunConfig.PrivateKey == "" {
		fmt.Println(colorRed + "krun-config.json is missing username and/or private key" + colorReset)
		os.Exit(0)
	}

	config = cfg.Config{
		KrunConfig: krunConfig,
	}

	if optKubeConfig != "" {
		config.KubeConfig = filepath.ToSlash(optKubeConfig)
	} else {
		dirname, err := os.UserHomeDir()
		if err != nil {
			fmt.Printf(colorRed+"Error getting user home directory: %s\n"+colorReset, err)
			os.Exit(0)
		}
		// set default kubeconfig path
		config.KubeConfig = filepath.ToSlash(dirname + "/.kube/config")
	}

	if krunConfig.Hostname == "kube.voortman.net" {
		// Set the registry suffix to the username.
		// This is used to create a unique local registry for the user in hosted environments.
		config.LocalRegistry = fmt.Sprintf("%s/%s", config.LocalRegistry, krunConfig.Username)
	}
	config.Registry = config.LocalRegistry

	services, err = cfg.DiscoverServices(krunConfig.KrunSourceConfig.Path, krunConfig.KrunSourceConfig.SearchDepth, cacheFile, cacheTtl)
	if err != nil {
		fmt.Printf(colorRed+"Error discovering services: %s\n"+colorReset, err)
		os.Exit(0)
	}
}
