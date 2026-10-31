package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/krun/build"
	"github.com/ftechmax/krun/internal/krun/debug"
	"github.com/ftechmax/krun/internal/krun/deploy"
	"github.com/ftechmax/krun/internal/utils"
	"github.com/spf13/cobra"
)

var (
	config   = cfg.Config{}
	version  = "debug" // will be set by the build system
	services = []cfg.Service{}
)

var kubeConfigPath string
var environment string

func main() {
	rootCmd := &cobra.Command{
		Use:   "krun",
		Short: "krun CLI",
		Long:  `krun [global options] <command> [command options] <service>`,
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
			Use:     "list",
			Short:   "List all services or projects",
			PreRun:  initialize,
			Run:     handleList,
			Example: "krun list",
		},
	)

	buildCmd := &cobra.Command{
		Use:    "build <project|service>",
		Short:  "Build a project or specific service",
		Args:   cobra.MinimumNArgs(1),
		PreRun: initialize,
		Run:    handleBuild,
	}
	buildCmd.Flags().Bool("force", false, "Force build even if up to date")
	buildCmd.Flags().Bool("flush", false, "Delete build cache")
	rootCmd.AddCommand(buildCmd)

	deployCmd := &cobra.Command{
		Use:    "deploy <project>",
		Short:  "Deploy a project",
		Args:   cobra.MinimumNArgs(1),
		PreRun: initialize,
		Run:    handleDeploy,
	}
	deployCmd.Flags().Bool("use-remote-registry", false, "Use remote registry for deploy")
	deployCmd.Flags().Bool("no-restart", false, "Skip rollout restart after apply")
	deployCmd.Flags().Bool("create-namespace", false, "Create target namespaces if they do not exist")
	deployCmd.Flags().StringVar(&environment, "environment", "local", "Deployment environment: local, development, production")
	rootCmd.AddCommand(deployCmd)

	deleteCmd := &cobra.Command{
		Use:     "delete <project>",
		Short:   "Delete a project",
		Args:    cobra.MinimumNArgs(1),
		PreRun:  initialize,
		Run:     handleDelete,
		Example: "krun delete myproject",
	}
	deleteCmd.Flags().StringVar(&environment, "environment", "local", "Deployment environment: local, development, production")
	rootCmd.AddCommand(deleteCmd)

	debugCmd := &cobra.Command{
		Use:              "debug",
		Short:            "Debug commands",
		PersistentPreRun: initialize,
	}
	debugListCmd := &cobra.Command{
		Use:   "list",
		Short: "List active debug sessions",
		Run:   func(*cobra.Command, []string) { debug.List(config) },
	}
	debugEnableCmd := &cobra.Command{
		Use:   "enable <service>",
		Short: "Enable debug mode for a service",
		Args:  cobra.MinimumNArgs(1),
		Run:   handleDebugEnable,
	}
	debugEnableCmd.Flags().String("container", "", "Name of the target container in the workload")
	debugDisableCmd := &cobra.Command{
		Use:   "disable <service>",
		Short: "Disable debug mode for a service",
		Args:  cobra.MinimumNArgs(1),
		Run:   handleDebugDisable,
	}
	debugHelperCmd := &cobra.Command{
		Use:   "helper",
		Short: "Inspect local debug helper daemon",
	}
	debugHelperStatusCmd := &cobra.Command{
		Use:   "status",
		Short: "Check local debug helper daemon status",
		Args:  cobra.NoArgs,
		Run:   func(*cobra.Command, []string) { debug.HelperStatus(config) },
	}
	debugRuntimeCmd := &cobra.Command{
		Use:   "runtime",
		Short: "Manage krun debug runtime components",
	}
	debugRuntimeInstallCmd := &cobra.Command{
		Use:   "install",
		Short: "Install or upgrade debug runtime in the cluster",
		Args:  cobra.NoArgs,
		Run:   func(*cobra.Command, []string) { debug.RuntimeInstall(config, version) },
	}
	debugRuntimeStatusCmd := &cobra.Command{
		Use:   "status",
		Short: "Check debug runtime status in the cluster",
		Args:  cobra.NoArgs,
		Run:   func(*cobra.Command, []string) { debug.RuntimeStatus(config) },
	}
	debugRuntimeUninstallCmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall debug runtime from the cluster",
		Args:  cobra.NoArgs,
		Run:   func(*cobra.Command, []string) { debug.RuntimeUninstall(config, version) },
	}
	debugRuntimeCmd.AddCommand(debugRuntimeInstallCmd, debugRuntimeStatusCmd, debugRuntimeUninstallCmd)
	debugHelperStopCmd := &cobra.Command{
		Use:              "stop",
		Short:            "Stop the local debug helper daemon",
		Args:             cobra.NoArgs,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {},
		Run:              func(*cobra.Command, []string) { debug.HelperStop() },
	}
	debugHelperCmd.AddCommand(debugHelperStatusCmd, debugHelperStopCmd)
	debugCmd.AddCommand(debugListCmd, debugEnableCmd, debugDisableCmd, debugHelperCmd, debugRuntimeCmd)
	rootCmd.AddCommand(debugCmd)

	if err := rootCmd.Execute(); err != nil {
		utils.Errf("%s", err)
		os.Exit(1)
	}
}

func initialize(_ *cobra.Command, _ []string) {
	krunConfig, err := cfg.ParseKrunConfig()
	if err != nil {
		utils.Errf("Error parsing krun-config.json: %s", err)
		os.Exit(1)
	}

	config = cfg.Config{
		KrunConfig:  krunConfig,
		Version:     version,
		Environment: environment,
	}

	if kubeConfigPath != "" {
		config.KubeConfig = filepath.ToSlash(kubeConfigPath)
	} else {
		dirname, err := os.UserHomeDir()
		if err != nil {
			utils.Errf("Error getting user home directory: %s", err)
			os.Exit(0)
		}
		// set default kubeconfig path
		config.KubeConfig = filepath.ToSlash(dirname + "/.kube/config")
	}

	config.Registry = config.LocalRegistry

	var projectPaths map[string]string
	services, projectPaths, err = cfg.DiscoverServices(krunConfig.KrunSourceConfig.Path, krunConfig.KrunSourceConfig.SearchDepth)
	if err != nil {
		utils.Errf("Error discovering services: %s", err)
		os.Exit(0)
	}
	config.ProjectPaths = projectPaths
}

func handleList(cmd *cobra.Command, args []string) {
	if len(services) == 0 {
		utils.Warnf("No services found")
		return
	}

	fmt.Println("Services")
	fmt.Println("--------")
	for _, service := range services {
		fmt.Println(service.Name)
	}
	fmt.Println("")

	fmt.Println("Projects")
	fmt.Println("--------")
	for _, project := range slices.Sorted(maps.Keys(config.ProjectPaths)) {
		fmt.Println(project)
	}
	fmt.Println("")
}

func handleBuild(cmd *cobra.Command, args []string) {
	forceBuild, _ := cmd.Flags().GetBool("force")
	flush, _ := cmd.Flags().GetBool("flush")
	argServiceName := args[0]
	serviceName, projectName, err := getServiceNameAndProject(argServiceName)
	if err != nil {
		utils.Errf("%s", err)
		return
	}
	servicesToBuild := []cfg.Service{}
	for _, s := range services {
		if s.Project == projectName && (serviceName == "" || s.Name == serviceName) {
			servicesToBuild = append(servicesToBuild, s)
		}
	}
	if err := build.Build(projectName, servicesToBuild, forceBuild, flush, config); err != nil {
		fmt.Println(utils.Colorize(err.Error(), utils.Red))
		os.Exit(1)
	}
}

func handleDeploy(cmd *cobra.Command, args []string) {
	useRemote, _ := cmd.Flags().GetBool("use-remote-registry")
	noRestart, _ := cmd.Flags().GetBool("no-restart")
	createNamespace, _ := cmd.Flags().GetBool("create-namespace")
	argServiceName := args[0]
	if useRemote {
		config.Registry = config.RemoteRegistry
	}
	_, projectName, err := getServiceNameAndProject(argServiceName)
	if err != nil {
		utils.Errf("%s", err)
		return
	}
	deploy.Deploy(projectName, config, !noRestart, createNamespace)
}

func handleDelete(cmd *cobra.Command, args []string) {
	argServiceName := args[0]
	_, projectName, err := getServiceNameAndProject(argServiceName)
	if err != nil {
		utils.Errf("%s", err)
		return
	}
	deploy.Delete(projectName, config)
}

func handleDebugEnable(cmd *cobra.Command, args []string) {
	containerName, _ := cmd.Flags().GetString("container")

	service, ok := findService(args[0])
	if !ok {
		return
	}
	fmt.Printf("Enabling debug mode for service %s\n", service.Name)
	debug.Enable(service, config, containerName)
}

func handleDebugDisable(cmd *cobra.Command, args []string) {
	service, ok := findService(args[0])
	if !ok {
		return
	}
	fmt.Printf("Disabling debug mode for service %s\n", service.Name)
	debug.Disable(service, config)
}

func findService(name string) (cfg.Service, bool) {
	for _, service := range services {
		if service.Name == name {
			return service, true
		}
	}
	utils.Errf("Service not found: %s", name)
	return cfg.Service{}, false
}

func getServiceNameAndProject(name string) (string, string, error) {
	for _, service := range services {
		if service.Name == name {
			return service.Name, service.Project, nil
		}
		if service.Project == name {
			return "", service.Project, nil
		}
	}
	return "", "", fmt.Errorf("Service or project '%s' not found.\nRun krun list to show available options", name)
}
