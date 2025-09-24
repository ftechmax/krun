package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/ftechmax/krun/internal/build"
	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/debug"
	"github.com/ftechmax/krun/internal/deploy"
	"github.com/ftechmax/krun/internal/utils"
	"github.com/spf13/cobra"
)

var (
	cacheFile   = "krun.cache"
	cacheTtl    = 8 * time.Hour
	config      = cfg.Config{}
	version     = "debug" // will be set by the build system
	services    = []cfg.Service{} // map of service name to service struct
)

var kubeConfigPath string

func main() {
	rootCmd := &cobra.Command{
		Use:   "krun",
		Short: "krun CLI",
		Long:  `krun [global options] <command> [command options] <service>`,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			initialize(kubeConfigPath)
		},
	}
	rootCmd.PersistentFlags().StringVar(&kubeConfigPath, "kubeconfig", "", "Path to kubeconfig file")

	rootCmd.AddCommand(
		&cobra.Command{
			Use:   "version",
			Short: "Show version information",
			Run: func(cmd *cobra.Command, args []string) {
				fmt.Println(version)
			},
		},
		&cobra.Command{
			Use:   "list",
			Short: "List all services or projects",
			Run: handleList,
			Example: "krun list",
		},
	)

	buildCmd := &cobra.Command{
		Use:   "build <project|service>",
		Short: "Build a project or specific service",
		Args:  cobra.MinimumNArgs(1),
		Run: handleBuild,
	}
	buildCmd.Flags().Bool("skip-web", false, "Skip building the web component")
	buildCmd.Flags().Bool("force", false, "Force build even if up to date")
	buildCmd.Flags().Bool("flush", false, "Delete build cache")
	rootCmd.AddCommand(buildCmd)

	deployCmd := &cobra.Command{
		Use:   "deploy <project>",
		Short: "Deploy a project",
		Args:  cobra.MinimumNArgs(1),
		Run: handleDeploy,
	}
	deployCmd.Flags().Bool("use-remote-registry", false, "Use remote registry for deploy")
	rootCmd.AddCommand(deployCmd)

	deleteCmd := &cobra.Command{
		Use:   "delete <project>",
		Short: "Delete a project",
		Args:  cobra.MinimumNArgs(1),
		Run: handleDelete,
		Example: "krun delete myproject",
	}
	rootCmd.AddCommand(deleteCmd)

	debugCmd := &cobra.Command{
		Use:   "debug",
		Short: "Debug commands",
	}
	debugListCmd := &cobra.Command{
		Use:   "list",
		Short: "List all services with debug mode status",
		Run: handleDebugList,
	}
	debugEnableCmd := &cobra.Command{
		Use:   "enable <service>",
		Short: "Enable debug mode for a service",
		Args:  cobra.MinimumNArgs(1),
		Run: handleDebugEnable,
	}
	debugEnableCmd.Flags().Bool("intercept", false, "Use intercept instead of replace")
	debugDisableCmd := &cobra.Command{
		Use:   "disable <service>",
		Short: "Disable debug mode for a service",
		Args:  cobra.MinimumNArgs(1),
		Run: handleDebugDisable,
	}
	debugCmd.AddCommand(debugListCmd, debugEnableCmd, debugDisableCmd)
	rootCmd.AddCommand(debugCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(utils.Colorize(err.Error(), utils.Red))
		os.Exit(1)
	}
}

func handleBuild(cmd *cobra.Command, args []string) {
	skipWeb, _ := cmd.Flags().GetBool("skip-web")
	forceBuild, _ := cmd.Flags().GetBool("force")
	flush, _ := cmd.Flags().GetBool("flush")
	argServiceName := args[0]
	serviceName, projectName, err := getServiceNameAndProject(argServiceName)
	if err != nil {
		fmt.Println(utils.Colorize(err.Error(), utils.Red))
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
	build.Build(projectName, servicesToBuild, skipWeb, forceBuild, flush, config)
}

func handleDeploy(cmd *cobra.Command, args []string) {
	useRemote, _ := cmd.Flags().GetBool("use-remote-registry")
	argServiceName := args[0]
	if useRemote {
		config.Registry = config.RemoteRegistry
	}
	_, projectName, err := getServiceNameAndProject(argServiceName)
	if err != nil {
		fmt.Println(utils.Colorize(err.Error(), utils.Red))
		return
	}
	deploy.Deploy(projectName, config)
}

func handleDelete(cmd *cobra.Command, args []string) {
	argServiceName := args[0]
	_, projectName, err := getServiceNameAndProject(argServiceName)
	if err != nil {
		fmt.Println(utils.Colorize(err.Error(), utils.Red))
		return
	}
	deploy.Delete(projectName, config)
}

func handleDebugList(cmd *cobra.Command, args []string) {
	debug.List(config)
}

func handleDebugEnable(cmd *cobra.Command, args []string) {
	// Disable replace if --intercept flag is set
	useIntercept,_ := cmd.Flags().GetBool("intercept")

	argServiceName := args[0]
	service := cfg.Service{}
	for _, s := range services {
		if s.Name == argServiceName {
			service = s
			break
		}
	}
	if service.Name == "" {
		fmt.Println(utils.Colorize(fmt.Sprintf("Service not found: %s", argServiceName), utils.Red))
		return
	}
	fmt.Printf("Enabling debug mode for service %s\n", argServiceName)
	debug.Enable(service, config, useIntercept)
}

func handleDebugDisable(cmd *cobra.Command, args []string) {
	argServiceName := args[0]
	service := cfg.Service{}
	for _, s := range services {
		if s.Name == argServiceName {
			service = s
			break
		}
	}
	if service.Name == "" {
		fmt.Println(utils.Colorize(fmt.Sprintf("Service not found: %s", argServiceName), utils.Red))
		return
	}
	fmt.Printf("Disabling debug mode for service %s\n", argServiceName)
	debug.Disable(service, config)
}

func handleList(cmd *cobra.Command, args []string) {
	if len(services) == 0 {
		fmt.Println(utils.Colorize("No services found", utils.Yellow))
		return
	}

	rows := [][]string{}
	for _, service := range services {
		rows = append(rows, []string{
			service.Name,
		})
	}

	t := table.New().
		Border(lipgloss.ASCIIBorder()).
		Headers("Available services").
		Rows(rows...)
	fmt.Println(t)
	fmt.Println("")

	rows = [][]string{}
	projects := make(map[string]bool)
	for _, service := range services {
		if service.Project != "" {
			projects[service.Project] = true
		}
	}
	for project := range projects {
		rows = append(rows, []string{project})
	}
	projectsTable := table.New().
		Border(lipgloss.ASCIIBorder()).
		Headers("Available projects").
		Rows(rows...)
	fmt.Println(projectsTable)
	fmt.Println("")
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
		return "", "", fmt.Errorf("Service or project '%s' not found.\nRun krun list to show available options", name)
	}

	return serviceName, projectName, nil
}

func initialize(optKubeConfig string) {
	krunConfig, err := cfg.ParseKrunConfig()
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Error parsing krun-config.json: %s", err), utils.Red))
		os.Exit(1)
    }

	config = cfg.Config{
		KrunConfig: krunConfig,
	}

	if optKubeConfig != "" {
		config.KubeConfig = filepath.ToSlash(optKubeConfig)
	} else {
		dirname, err := os.UserHomeDir()
		if err != nil {
			fmt.Println(utils.Colorize(fmt.Sprintf("Error getting user home directory: %s", err), utils.Red))
			os.Exit(0)
		}
		// set default kubeconfig path
		config.KubeConfig = filepath.ToSlash(dirname + "/.kube/config")
	}
	
	config.Registry = config.LocalRegistry

	services, err = cfg.DiscoverServices(krunConfig.KrunSourceConfig.Path, krunConfig.KrunSourceConfig.SearchDepth, cacheFile, cacheTtl)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Error discovering services: %s", err), utils.Red))
		os.Exit(0)
	}
}
