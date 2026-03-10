package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/krun/build"
	"github.com/ftechmax/krun/internal/krun/debug"
	"github.com/ftechmax/krun/internal/krun/deploy"
	"github.com/ftechmax/krun/internal/utils"
	"github.com/spf13/cobra"
)

var (
	config   = cfg.Config{}
	version  = "debug"         // will be set by the build system
	services = []cfg.Service{} // map of service name to service struct
)

var kubeConfigPath string

func main() {
	rootCmd := &cobra.Command{
		Use:   "krun",
		Short: "krun CLI",
		Long:  `krun [global options] <command> [command options] <service>`,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			initialize(cmd, kubeConfigPath)
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
			Use:     "list",
			Short:   "List all services or projects",
			Run:     handleList,
			Example: "krun list",
		},
	)

	buildCmd := &cobra.Command{
		Use:   "build <project|service>",
		Short: "Build a project or specific service",
		Args:  cobra.MinimumNArgs(1),
		Run:   handleBuild,
	}
	buildCmd.Flags().Bool("skip-web", false, "Skip building the web component")
	buildCmd.Flags().Bool("force", false, "Force build even if up to date")
	buildCmd.Flags().Bool("flush", false, "Delete build cache")
	rootCmd.AddCommand(buildCmd)

	deployCmd := &cobra.Command{
		Use:   "deploy <project>",
		Short: "Deploy a project",
		Args:  cobra.MinimumNArgs(1),
		Run:   handleDeploy,
	}
	deployCmd.Flags().Bool("use-remote-registry", false, "Use remote registry for deploy")
	deployCmd.Flags().Bool("no-restart", false, "Skip rollout restart after apply")
	rootCmd.AddCommand(deployCmd)

	deleteCmd := &cobra.Command{
		Use:     "delete <project>",
		Short:   "Delete a project",
		Args:    cobra.MinimumNArgs(1),
		Run:     handleDelete,
		Example: "krun delete myproject",
	}
	rootCmd.AddCommand(deleteCmd)

	debugCmd := &cobra.Command{
		Use:   "debug",
		Short: "Debug commands",
	}
	debugListCmd := &cobra.Command{
		Use:   "list",
		Short: "List active debug sessions",
		Run:   handleDebugList,
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
		Run:   handleDebugHelperStatus,
	}
	debugRuntimeCmd := &cobra.Command{
		Use:   "runtime",
		Short: "Manage krun debug runtime components",
	}
	debugRuntimeInstallCmd := &cobra.Command{
		Use:   "install",
		Short: "Install or upgrade debug runtime in the cluster",
		Args:  cobra.NoArgs,
		Run:   handleDebugRuntimeInstall,
	}
	debugRuntimeStatusCmd := &cobra.Command{
		Use:   "status",
		Short: "Check debug runtime status in the cluster",
		Args:  cobra.NoArgs,
		Run:   handleDebugRuntimeStatus,
	}
	debugRuntimeUninstallCmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall debug runtime from the cluster",
		Args:  cobra.NoArgs,
		Run:   handleDebugRuntimeUninstall,
	}
	debugRuntimeCmd.AddCommand(debugRuntimeInstallCmd, debugRuntimeStatusCmd, debugRuntimeUninstallCmd)
	debugHelperStopCmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the local debug helper daemon",
		Args:  cobra.NoArgs,
		Run:   handleDebugHelperStop,
	}
	debugHelperCmd.AddCommand(debugHelperStatusCmd, debugHelperStopCmd)
	debugCmd.AddCommand(debugListCmd, debugEnableCmd, debugDisableCmd, debugHelperCmd, debugRuntimeCmd)
	rootCmd.AddCommand(debugCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(utils.Colorize(err.Error(), utils.Red))
		os.Exit(1)
	}
}

func initialize(_ *cobra.Command, optKubeConfig string) {
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

	var projectPaths map[string]string
	services, projectPaths, err = cfg.DiscoverServices(krunConfig.KrunSourceConfig.Path, krunConfig.KrunSourceConfig.SearchDepth)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Error discovering services: %s", err), utils.Red))
		os.Exit(0)
	}
	config.ProjectPaths = projectPaths
}

func handleList(cmd *cobra.Command, args []string) {
	if len(services) == 0 {
		fmt.Println(utils.Colorize("No services found", utils.Yellow))
		return
	}

	fmt.Println("Services")
	fmt.Println("--------")
	for _, service := range services {
		fmt.Println(service.Name)
	}
	fmt.Println("")

	projects := make(map[string]bool)
	for _, service := range services {
		if service.Project != "" {
			projects[service.Project] = true
		}
	}

	projectNames := make([]string, 0, len(projects))
	for project := range projects {
		projectNames = append(projectNames, project)
	}
	sort.Strings(projectNames)

	fmt.Println("Projects")
	fmt.Println("--------")
	for _, project := range projectNames {
		fmt.Println(project)
	}
	fmt.Println("")
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
	noRestart, _ := cmd.Flags().GetBool("no-restart")
	argServiceName := args[0]
	if useRemote {
		config.Registry = config.RemoteRegistry
	}
	_, projectName, err := getServiceNameAndProject(argServiceName)
	if err != nil {
		fmt.Println(utils.Colorize(err.Error(), utils.Red))
		return
	}
	deploy.Deploy(projectName, config, !noRestart)
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
	containerName, _ := cmd.Flags().GetString("container")

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
	debug.Enable(service, config, containerName)
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

func handleDebugHelperStatus(cmd *cobra.Command, args []string) {
	debug.HelperStatus(config)
}

func handleDebugHelperStop(cmd *cobra.Command, args []string) {
	debug.HelperStop()
}

func handleDebugRuntimeInstall(cmd *cobra.Command, args []string) {
	debug.RuntimeInstall(config, version)
}

func handleDebugRuntimeStatus(cmd *cobra.Command, args []string) {
	debug.RuntimeStatus(config)
}

func handleDebugRuntimeUninstall(cmd *cobra.Command, args []string) {
	debug.RuntimeUninstall(config, version)
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
