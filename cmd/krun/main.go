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
			PreRun:  preRunInit,
			Run:     handleList,
			Example: "krun list",
		},
	)

	buildCmd := &cobra.Command{
		Use:    "build <project|service>",
		Short:  "Build a project or specific service",
		Args:   cobra.MinimumNArgs(1),
		PreRun: preRunInit,
		Run:    handleBuild,
	}
	buildCmd.Flags().Bool("skip-web", false, "Skip building the web component")
	buildCmd.Flags().Bool("force", false, "Force build even if up to date")
	buildCmd.Flags().Bool("flush", false, "Delete build cache")
	rootCmd.AddCommand(buildCmd)

	deployCmd := &cobra.Command{
		Use:    "deploy <project>",
		Short:  "Deploy a project",
		Args:   cobra.MinimumNArgs(1),
		PreRun: preRunInit,
		Run:    handleDeploy,
	}
	deployCmd.Flags().Bool("use-remote-registry", false, "Use remote registry for deploy")
	deployCmd.Flags().Bool("no-restart", false, "Skip rollout restart after apply")
	rootCmd.AddCommand(deployCmd)

	deleteCmd := &cobra.Command{
		Use:     "delete <project>",
		Short:   "Delete a project",
		Args:    cobra.MinimumNArgs(1),
		PreRun:  preRunInit,
		Run:     handleDelete,
		Example: "krun delete myproject",
	}
	rootCmd.AddCommand(deleteCmd)

	installCmd := &cobra.Command{
		Use:              "install",
		Short:            "Install or update krun-helper service and in-cluster runtime",
		Args:             cobra.NoArgs,
		PersistentPreRun: preRunKubeConfigOnly,
		Run:              handleInstall,
	}
	uninstallCmd := &cobra.Command{
		Use:              "uninstall",
		Short:            "Uninstall krun-helper service and in-cluster runtime",
		Args:             cobra.NoArgs,
		PersistentPreRun: preRunKubeConfigOnly,
		Run:              handleUninstall,
	}
	statusCmd := &cobra.Command{
		Use:              "status",
		Short:            "Show health and version of krun-helper and in-cluster runtime",
		Args:             cobra.NoArgs,
		PersistentPreRun: preRunKubeConfigOnly,
		Run:              handleStatus,
	}
	rootCmd.AddCommand(installCmd, uninstallCmd, statusCmd)

	rootCmd.AddCommand(
		&cobra.Command{
			Use:              "__helper-install",
			Hidden:           true,
			Args:             cobra.NoArgs,
			PersistentPreRun: preRunKubeConfigOnly,
			Run:              func(cmd *cobra.Command, args []string) { debug.HelperInstall(config) },
		},
		&cobra.Command{
			Use:    "__helper-uninstall",
			Hidden: true,
			Args:   cobra.NoArgs,
			Run:    func(cmd *cobra.Command, args []string) { debug.HelperUninstall() },
		},
	)

	debugCmd := &cobra.Command{
		Use:              "debug",
		Short:            "Debug commands",
		PersistentPreRun: preRunInit,
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
	debugHelperStopCmd := &cobra.Command{
		Use:              "stop",
		Short:            "Stop the local debug helper daemon",
		Args:             cobra.NoArgs,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {},
		Run:              handleDebugHelperStop,
	}
	debugHelperCmd.AddCommand(debugHelperStopCmd)
	debugCmd.AddCommand(debugListCmd, debugEnableCmd, debugDisableCmd, debugHelperCmd)
	rootCmd.AddCommand(debugCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(utils.Colorize(err.Error(), utils.Red))
		os.Exit(1)
	}
}

func preRunInit(cmd *cobra.Command, args []string) {
	initialize(cmd, kubeConfigPath)
}

func preRunKubeConfigOnly(cmd *cobra.Command, args []string) {
	initializeKubeConfig(kubeConfigPath)
}

func initialize(_ *cobra.Command, optKubeConfig string) {
	initializeKubeConfig(optKubeConfig)

	krunConfig, err := cfg.ParseKrunConfig()
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Error parsing krun-config.json: %s", err), utils.Red))
		os.Exit(1)
	}

	config.KrunConfig = krunConfig

	config.Registry = config.LocalRegistry

	var projectPaths map[string]string
	services, projectPaths, err = cfg.DiscoverServices(krunConfig.Path, krunConfig.SearchDepth)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Error discovering services: %s", err), utils.Red))
		os.Exit(0)
	}
	config.ProjectPaths = projectPaths
}

func initializeKubeConfig(optKubeConfig string) {
	config = cfg.Config{}

	if optKubeConfig != "" {
		config.KubeConfig = filepath.ToSlash(optKubeConfig)
		return
	}

	dirname, err := os.UserHomeDir()
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("Error getting user home directory: %s", err), utils.Red))
		os.Exit(0)
	}

	// set default kubeconfig path
	config.KubeConfig = filepath.ToSlash(dirname + "/.kube/config")
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

func handleDebugHelperStop(cmd *cobra.Command, args []string) {
	debug.HelperStop()
}

func handleInstall(cmd *cobra.Command, args []string) {
	debug.HelperInstall(config)
	debug.RuntimeInstall(config, version)
}

func handleUninstall(cmd *cobra.Command, args []string) {
	debug.RuntimeUninstall(config, version)
	debug.HelperUninstall()
}

func handleStatus(cmd *cobra.Command, args []string) {
	fmt.Println("krun-helper")
	fmt.Println("-----------")
	debug.HelperStatus(config)
	fmt.Println("")
	fmt.Println("traffic-manager")
	fmt.Println("---------------")
	debug.RuntimeStatus(config)
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
		return "", "", fmt.Errorf("service or project '%s' not found\nrun krun list to show available options", name)
	}

	return serviceName, projectName, nil
}
