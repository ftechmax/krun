package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/krun/helper"
	"github.com/ftechmax/krun/internal/krun/helperclient"
	krunruntime "github.com/ftechmax/krun/internal/krun/runtime"
	"github.com/ftechmax/krun/internal/utils"
	"github.com/spf13/cobra"
)

var (
	config   = cfg.Config{}
	version  = "debug"         // will be set by the build system
	services = []cfg.Service{} // map of service name to service struct
)

var (
	kubeConfigPath   string
	installOwnerName string
)

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
			PreRun:  preRunKubeConfigOnly,
			Run:     handleList,
			Example: "krun list",
		},
	)

	buildCmd := &cobra.Command{
		Use:    "build <project|service>",
		Short:  "Build a project or specific service",
		Args:   cobra.MinimumNArgs(1),
		PreRun: preRunKubeConfigOnly,
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
		PreRun: preRunKubeConfigOnly,
		Run:    handleDeploy,
	}
	deployCmd.Flags().Bool("use-remote-registry", false, "Use remote registry for deploy")
	deployCmd.Flags().Bool("no-restart", false, "Skip rollout restart after apply")
	rootCmd.AddCommand(deployCmd)

	deleteCmd := &cobra.Command{
		Use:     "delete <project>",
		Short:   "Delete a project",
		Args:    cobra.MinimumNArgs(1),
		PreRun:  preRunKubeConfigOnly,
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
	installCmd.Flags().StringVar(&installOwnerName, "owner", "", "Owner user for elevated helper service")
	_ = installCmd.Flags().MarkHidden("owner")
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
	response, err := helperclient.WorkspaceList(config)
	if err != nil {
		fmt.Println(utils.Colorize(err.Error(), utils.Red))
		os.Exit(1)
	}

	if len(response.Services) == 0 {
		fmt.Println(utils.Colorize("No services found", utils.Yellow))
		return
	}

	fmt.Println("Services")
	fmt.Println("--------")
	for _, service := range response.Services {
		fmt.Println(service.Name)
	}
	fmt.Println("")

	fmt.Println("Projects")
	fmt.Println("--------")
	for _, project := range response.Projects {
		fmt.Println(project)
	}
	fmt.Println("")
}

func handleBuild(cmd *cobra.Command, args []string) {
	skipWeb, _ := cmd.Flags().GetBool("skip-web")
	forceBuild, _ := cmd.Flags().GetBool("force")
	flush, _ := cmd.Flags().GetBool("flush")

	err := helperclient.WorkspaceBuild(config, contracts.BuildRequest{
		Target:  args[0],
		SkipWeb: skipWeb,
		Force:   forceBuild,
		Flush:   flush,
	}, os.Stdout)
	if err != nil {
		fmt.Println(utils.Colorize(err.Error(), utils.Red))
		os.Exit(1)
	}
}

func handleDeploy(cmd *cobra.Command, args []string) {
	useRemote, _ := cmd.Flags().GetBool("use-remote-registry")
	noRestart, _ := cmd.Flags().GetBool("no-restart")

	err := helperclient.WorkspaceDeploy(config, contracts.DeployRequest{
		Target:            args[0],
		UseRemoteRegistry: useRemote,
		NoRestart:         noRestart,
	}, os.Stdout)
	if err != nil {
		fmt.Println(utils.Colorize(err.Error(), utils.Red))
		os.Exit(1)
	}
}

func handleDelete(cmd *cobra.Command, args []string) {
	err := helperclient.WorkspaceDelete(config, contracts.DeleteRequest{
		Target: args[0],
	}, os.Stdout)
	if err != nil {
		fmt.Println(utils.Colorize(err.Error(), utils.Red))
		os.Exit(1)
	}
}

func handleDebugList(cmd *cobra.Command, args []string) {
	sessions, err := helperclient.HelperDebugSessionsList(config)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("cannot list debug sessions via helper: %v", err), utils.Red))
		return
	}

	if len(sessions) == 0 {
		fmt.Println(utils.Colorize("No active debug sessions", utils.Yellow))
		return
	}

	fmt.Println("Active debug sessions")
	fmt.Println("---------------------")
	for _, session := range sessions {
		serviceName := strings.TrimSpace(session.Context.ServiceName)
		namespace := strings.TrimSpace(session.Context.Namespace)
		if namespace == "" {
			namespace = "default"
		}

		fmt.Printf("Service: %s (namespace: %s)\n", serviceName, namespace)
		fmt.Printf("Intercept port: %d\n", session.Context.InterceptPort)
		fmt.Println("Service dependencies:")

		if len(session.Context.ServiceDependencies) == 0 {
			fmt.Println("  (none)")
		} else {
			for _, dependency := range session.Context.ServiceDependencies {
				host := strings.TrimSpace(dependency.Host)
				if host == "" {
					service := strings.TrimSpace(dependency.Service)
					dependencyNamespace := strings.TrimSpace(dependency.Namespace)
					if dependencyNamespace == "" {
						dependencyNamespace = "default"
					}
					if service == "" {
						host = "(unknown)"
					} else {
						host = fmt.Sprintf("%s.%s.svc", service, dependencyNamespace)
					}
				}
				fmt.Printf("  - %s:%d\n", host, dependency.Port)
			}
		}
		fmt.Println("")
	}
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

	response, err := helperclient.HelperDebugEnable(config, buildDebugServiceContext(service), containerName)
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("cannot apply debug enable via helper: %v", err), utils.Red))
		return
	}
	if !response.Success {
		fmt.Println(utils.Colorize(fmt.Sprintf("helper refused debug enable: %s", response.Message), utils.Red))
		return
	}

	fmt.Println(utils.Colorize("Session created", utils.Green))
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

	response, err := helperclient.HelperDebugDisable(config, buildDebugServiceContext(service))
	if err != nil {
		fmt.Println(utils.Colorize(fmt.Sprintf("cannot apply debug disable via helper: %v", err), utils.Red))
		return
	}
	if !response.Success {
		fmt.Println(utils.Colorize(fmt.Sprintf("helper refused debug disable: %s", response.Message), utils.Red))
		return
	}

	if response.Message == "no active session" {
		fmt.Println(utils.Colorize("No active debug session found", utils.Yellow))
	} else {
		fmt.Println(utils.Colorize("Session removed", utils.Green))
	}
}

func handleDebugHelperStop(cmd *cobra.Command, args []string) {
	helper.HelperStop()
}

func handleInstall(cmd *cobra.Command, args []string) {
	if helper.HelperServiceRequiresElevation() {
		rerunArgs, err := elevatedCommandArgs("install")
		if err != nil {
			fmt.Println(utils.Colorize(err.Error(), utils.Red))
			os.Exit(1)
		}
		if err := helper.RerunKrunCommandElevated(rerunArgs); err != nil {
			fmt.Println(utils.Colorize(fmt.Sprintf("failed to re-run install with elevation: %v", err), utils.Red))
			os.Exit(1)
		}
		return
	}

	helper.HelperInstall(config, installOwnerName)
	krunruntime.RuntimeInstall(config, version)
}

func handleUninstall(cmd *cobra.Command, args []string) {
	if helper.HelperServiceInstalled() && helper.HelperServiceRequiresElevation() {
		rerunArgs, err := elevatedCommandArgs("uninstall")
		if err != nil {
			fmt.Println(utils.Colorize(err.Error(), utils.Red))
			os.Exit(1)
		}
		if err := helper.RerunKrunCommandElevated(rerunArgs); err != nil {
			fmt.Println(utils.Colorize(fmt.Sprintf("failed to re-run uninstall with elevation: %v", err), utils.Red))
			os.Exit(1)
		}
		return
	}

	krunruntime.RuntimeUninstall(config, version)
	helper.HelperUninstall()
}

func elevatedCommandArgs(commandName string) ([]string, error) {
	absKubeConfigPath, err := filepath.Abs(strings.TrimSpace(config.KubeConfig))
	if err != nil {
		return nil, fmt.Errorf("cannot resolve kubeconfig path: %w", err)
	}

	rerunArgs := []string{commandName, "--kubeconfig", filepath.ToSlash(absKubeConfigPath)}
	if commandName == "install" {
		trimmedOwnerName := strings.TrimSpace(installOwnerName)
		if trimmedOwnerName == "" {
			trimmedOwnerName = helper.ResolveServiceOwnerName()
		}
		if trimmedOwnerName != "" {
			rerunArgs = append(rerunArgs, "--owner", trimmedOwnerName)
		}
	}
	return rerunArgs, nil
}

func handleStatus(cmd *cobra.Command, args []string) {
	fmt.Println("krun-helper")
	fmt.Println("-----------")
	helper.HelperStatus(config)
	fmt.Println("")
	fmt.Println("traffic-manager")
	fmt.Println("---------------")
	krunruntime.RuntimeStatus(config)
}

func buildDebugServiceContext(service cfg.Service) contracts.DebugServiceContext {
	dependencies := make([]contracts.DebugServiceDependencyContext, 0, len(service.ServiceDependencies))
	for _, dependency := range service.ServiceDependencies {
		dependencies = append(dependencies, contracts.DebugServiceDependencyContext{
			Host:      dependency.Host,
			Namespace: dependency.Namespace,
			Service:   dependency.Service,
			Port:      dependency.Port,
			Aliases:   dependency.Aliases,
		})
	}

	return contracts.DebugServiceContext{
		Project:             service.Project,
		ServiceName:         service.Name,
		Namespace:           service.Namespace,
		ContainerPort:       service.ContainerPort,
		InterceptPort:       service.InterceptPort,
		ServiceDependencies: dependencies,
	}
}
